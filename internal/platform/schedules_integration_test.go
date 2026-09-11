package platform

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func scheduleFixture(t *testing.T, a *App, p *Principal, repeat string, scope Object) (Object, time.Time) {
	t.Helper()
	due := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	s := responseObject(t, apiRequest(t, a, p, "POST", "/api/pipeline/schedules/", Object{
		"name": "定时验收", "run_at": due.Format(time.RFC3339), "repeat": repeat, "scope": scope,
	}), 201)
	return s, due
}

func storedSchedule(t *testing.T, a *App, id any) Object {
	t.Helper()
	s, err := one(context.Background(), a.Pool, "SELECT row_to_json(s) FROM platform_processing_schedules s WHERE id=$1", id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestScheduledRunSurvivesRestartAndConcurrentClaims(t *testing.T) {
	a := isolatedInboxApp(t, false)
	f := newPipelineFixtureWithApp(t, a)
	s, due := scheduleFixture(t, a, f.p, "once", Object{"candidate_ids": []any{f.candidate["id"]}, "force_reprocess": true})
	if claimed, err := a.triggerSchedule(context.Background(), due.Add(-time.Second)); err != nil || claimed {
		t.Fatalf("triggered early: %v %v", claimed, err)
	}
	// A fresh App instance uses only durable database state to recover the trigger.
	restarted, err := New(context.Background(), a.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err = restarted.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := restarted.triggerSchedule(context.Background(), due.Add(time.Hour))
			if err != nil {
				t.Error(err)
			}
			if claimed {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	s = storedSchedule(t, a, s["id"])
	if claims.Load() != 1 || s["status"] != "completed" || s["next_run_at"] != nil || num(s["trigger_count"]) != 1 {
		t.Fatalf("not exactly one durable trigger: claims=%d schedule=%v", claims.Load(), s)
	}
	run, err := a.get(context.Background(), a.Pool, "core_processingrun", s["last_run_id"])
	if err != nil || obj(run["scope"])["source"] != "schedule" || num(obj(run["scope"])["schedule_id"]) != num(s["id"]) {
		t.Fatalf("missing scheduled provenance: %v %v", run, err)
	}
	f.executeJob(t, run, context.Background())
	run, err = a.get(context.Background(), a.Pool, "core_processingrun", run["id"])
	if err != nil || run["status"] != "success" || f.analyses.Load() != 1 {
		t.Fatalf("scheduled job failed through existing pipeline: %v %v", brief(run, "status", "error"), err)
	}
}

func TestScheduleDynamicScopeAndOverlap(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p := adminPrincipal(t, a)
	s, due := scheduleFixture(t, a, p, "daily", Object{"system_statuses": []any{"raw"}})
	// Candidates imported after creation belong to the next dynamic scope.
	candidate := mustSave(t, a, "core_candidate", Object{"name": "后导入", "identity_hash": token(32)})
	mustSave(t, a, "core_resume", Object{"candidate_id": candidate["id"], "apply_id": token(12)})
	if claimed, err := a.triggerSchedule(context.Background(), due); err != nil || !claimed {
		t.Fatalf("trigger failed: %v %v", claimed, err)
	}
	s = storedSchedule(t, a, s["id"])
	var count int
	if err := a.Pool.QueryRow(context.Background(), "SELECT count(*) FROM core_processingrunscopeitem WHERE run_id=$1 AND candidate_id=$2", s["last_run_id"], candidate["id"]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new import missing from dynamic scope: %d %v", count, err)
	}
	firstRun := s["last_run_id"]
	if claimed, err := a.triggerSchedule(context.Background(), due.Add(3*24*time.Hour)); err != nil || !claimed {
		t.Fatalf("missed trigger failed: %v %v", claimed, err)
	}
	s = storedSchedule(t, a, s["id"])
	next, _ := time.Parse(time.RFC3339Nano, str(s["next_run_at"]))
	if !strings.Contains(str(s["last_message"]), "上一轮") || s["last_run_id"] != firstRun || !next.Equal(due.Add(4*24*time.Hour)) {
		t.Fatalf("overlap was not skipped/coalesced: %v", s)
	}
}

func TestScheduleEmptyScopeCancelAndPermissionRevocation(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p := adminPrincipal(t, a)
	s, due := scheduleFixture(t, a, p, "daily", Object{"system_statuses": []any{"raw"}})
	if _, err := a.triggerSchedule(context.Background(), due); err != nil {
		t.Fatal(err)
	}
	s = storedSchedule(t, a, s["id"])
	if s["status"] != "active" || s["last_run_id"] != nil || !strings.Contains(str(s["last_message"]), "没有符合") {
		t.Fatalf("empty scope should be skipped: %v", s)
	}
	responseObject(t, apiRequest(t, a, p, "POST", "/api/pipeline/schedules/"+str(s["id"])+"/cancel/", nil), 200)
	if claimed, err := a.triggerSchedule(context.Background(), due.Add(24*time.Hour)); err != nil || claimed {
		t.Fatalf("cancelled schedule triggered: %v %v", claimed, err)
	}
	s, due = scheduleFixture(t, a, p, "daily", Object{"system_statuses": []any{"raw"}})
	// Removing the administrator role and superuser flag revokes execution rights.
	if _, err := a.Pool.Exec(context.Background(), "DELETE FROM accounts_user_groups WHERE user_id=$1", p.User["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pool.Exec(context.Background(), "UPDATE accounts_user SET is_superuser=false WHERE id=$1", p.User["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.triggerSchedule(context.Background(), due); err != nil {
		t.Fatal(err)
	}
	s = storedSchedule(t, a, s["id"])
	if s["status"] != "failed" || s["next_run_at"] != nil || s["last_run_id"] != nil {
		t.Fatalf("revoked permission did not stop schedule: %v", s)
	}
	responseObject(t, apiRequest(t, a, p, "GET", "/api/pipeline/schedules/", nil), 403)
	responseObject(t, apiRequest(t, a, p, "POST", "/api/pipeline/schedules/", Object{}), 403)
}

func TestScheduleDispatchRollsBackAndRetries(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p := adminPrincipal(t, a)
	c := mustSave(t, a, "core_candidate", Object{"name": "事务候选人", "identity_hash": token(32)})
	s, due := scheduleFixture(t, a, p, "once", Object{"candidate_ids": []any{c["id"]}})
	ctx := context.Background()
	if _, err := a.Pool.Exec(ctx, `CREATE FUNCTION reject_schedule_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture queue failure'; END $$;
 CREATE TRIGGER reject_schedule_job BEFORE INSERT ON platform_go_jobs FOR EACH ROW EXECUTE FUNCTION reject_schedule_job();`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.triggerSchedule(ctx, due); err == nil {
		t.Fatal("queue failure was ignored")
	}
	s = storedSchedule(t, a, s["id"])
	var count int
	if err := a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_processingrun").Scan(&count); err != nil || count != 0 || s["status"] != "active" || num(s["trigger_count"]) != 0 {
		t.Fatalf("partial dispatch escaped rollback: count=%d schedule=%v err=%v", count, s, err)
	}
	if _, err := a.Pool.Exec(ctx, "DROP TRIGGER reject_schedule_job ON platform_go_jobs"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := a.triggerSchedule(ctx, due.Add(time.Second)); err != nil || !claimed {
		t.Fatalf("rolled-back trigger not retryable: %v %v", claimed, err)
	}
}

func TestScheduleOwnershipAndDeletedCandidate(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p := adminPrincipal(t, a)
	c := mustSave(t, a, "core_candidate", Object{"name": "将删除", "identity_hash": token(32)})
	s, due := scheduleFixture(t, a, p, "once", Object{"candidate_ids": []any{c["id"]}})
	user := mustSave(t, a, "accounts_user", Object{"username": token(10), "password": "!", "date_joined": now(), "is_active": true})
	if _, err := a.Pool.Exec(context.Background(), `INSERT INTO accounts_user_user_permissions(user_id,permission_id)
 SELECT $1,p.id FROM auth_permission p JOIN django_content_type c ON c.id=p.content_type_id WHERE c.app_label='accounts' AND p.codename IN ('pipeline__view','pipeline__run','resume__view')`, user["id"]); err != nil {
		t.Fatal(err)
	}
	other, err := a.userPrincipal(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	listed := responseObject(t, apiRequest(t, a, other, "GET", "/api/pipeline/schedules/", nil), 200)
	if truth(obj(list(listed["results"])[0])["can_cancel"]) {
		t.Fatal("another user can cancel schedule")
	}
	responseObject(t, apiRequest(t, a, other, "POST", "/api/pipeline/schedules/"+str(s["id"])+"/cancel/", nil), 403)
	if _, err := a.Pool.Exec(context.Background(), "DELETE FROM core_candidate WHERE id=$1", c["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.triggerSchedule(context.Background(), due); err != nil {
		t.Fatal(err)
	}
	if storedSchedule(t, a, s["id"])["status"] != "failed" {
		t.Fatal("deleted selected candidate should stop schedule explicitly")
	}
}
