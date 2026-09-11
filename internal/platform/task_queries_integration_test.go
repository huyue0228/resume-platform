package platform

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestTaskQueryPagesWholeHistoryAndCountsBeyondVisiblePage(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p := adminPrincipal(t, a)
	for i := 0; i < 25; i++ {
		status := "success"
		if i == 0 {
			status = "running"
		}
		if i == 1 {
			status = "failed"
		}
		run := mustSave(t, a, "core_processingrun", Object{"step": "step2", "status": status, "created_by_id": p.User["id"], "created_by_username_snapshot": p.User["username"], "scope": Object{"task_name": fmt.Sprintf("Night %d", i), "source": "schedule", "schedule_id": 9}})
		if _, err := a.Pool.Exec(context.Background(), "UPDATE core_processingrun SET created_at=$2 WHERE id=$1", run["id"], time.Date(2026, 9, 10, 16, i, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	mustSave(t, a, "core_processingrun", Object{"step": "step2", "status": "failed", "scope": Object{"task_name": "Night manual"}})
	path := "/api/pipeline/runs/?search=Night&source=schedule&schedule_id=9&include_summary=true&created_from=2026-09-11&created_to=2026-09-11"
	first := responseObject(t, apiRequest(t, a, p, "GET", path, nil), 200)
	if num(first["count"]) != 25 || len(list(first["results"])) != 20 || num(obj(first["summary"])["active"]) != 1 || num(obj(first["summary"])["attention"]) != 1 {
		t.Fatalf("history count or unpaged summary incorrect: %v", first["summary"])
	}
	for _, value := range list(first["results"]) {
		if obj(value)["status"] != "success" {
			t.Fatal("fixture active records should be outside first page")
		}
	}
	second := responseObject(t, apiRequest(t, a, p, "GET", path+"&page=2", nil), 200)
	if len(list(second["results"])) != 5 || second["next"] != nil || second["previous"] == nil {
		t.Fatal("history pagination failed")
	}
	filtered := responseObject(t, apiRequest(t, a, p, "GET", path+"&state=attention", nil), 200)
	if num(filtered["count"]) != 1 || num(obj(filtered["summary"])["total"]) != 25 {
		t.Fatal("state filter changed scope totals")
	}
	before := responseObject(t, apiRequest(t, a, p, "GET", "/api/pipeline/runs/?source=schedule&created_to=2026-09-10", nil), 200)
	if num(before["count"]) != 0 {
		t.Fatal("date end did not use Beijing calendar boundary")
	}
	for _, query := range []string{"state=invalid", "source=unknown", "schedule_id=0", "status=invalid", "created_from=2026-09-12&created_to=2026-09-11"} {
		responseObject(t, apiRequest(t, a, p, "GET", "/api/pipeline/runs/?"+query, nil), 400)
	}
}

func TestNamedManualTaskAndLiteralSearch(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p := adminPrincipal(t, a)
	c := mustSave(t, a, "core_candidate", Object{"name": "任务候选人", "identity_hash": token(32)})
	reply := responseObject(t, apiRequest(t, a, p, "POST", "/api/pipeline/run/", Object{"name": "50%_test", "step": "step2", "scope": Object{"candidate_ids": []any{c["id"]}}}), 202)
	run := obj(list(reply["processing_runs"])[0])
	if obj(run["scope_summary"])["task_name"] != "50%_test" {
		t.Fatal("task name was not frozen")
	}
	listed := responseObject(t, apiRequest(t, a, p, "GET", "/api/pipeline/runs/?search=50%25_test&source=manual&mine=true", nil), 200)
	if num(listed["count"]) != 1 {
		t.Fatal("literal task search or creator filter failed")
	}
	for _, query := range []string{"id=" + str(run["id"]), "ids=" + str(run["id"]), "step=step2", "step_in=step2,all", "created_by=" + str(p.User["id"]), "created_by_username=" + str(p.User["username"])} {
		listed = responseObject(t, apiRequest(t, a, p, "GET", "/api/pipeline/runs/?"+query, nil), 200)
		if num(listed["count"]) != 1 {
			t.Fatalf("existing task filter lost matching run: %s", query)
		}
	}
	for _, query := range []string{"id=99999", "ids=99998,99999", "step=all", "mode=unknown", "status_in=success,failed", "created_by=99999"} {
		listed = responseObject(t, apiRequest(t, a, p, "GET", "/api/pipeline/runs/?"+query, nil), 200)
		if num(listed["count"]) != 0 {
			t.Fatalf("existing task filter was silently ignored: %s", query)
		}
	}
	responseObject(t, apiRequest(t, a, p, "POST", "/api/pipeline/run/", Object{"step": "step2", "scope": Object{"candidate_ids": []any{c["id"]}, "schedule_id": 1}}), 400)
}

func TestScheduleFiltersPauseResumeAndStableScope(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p := adminPrincipal(t, a)
	scope := Object{"system_statuses": []any{"raw"}, "candidate_filters": Object{"name": "研发", "highest_major_in": []any{"软件工程"}, "current_apply_date_from": "2026-09-01"}}
	s, due := scheduleFixture(t, a, p, "daily", scope)
	path := "/api/pipeline/schedules/" + str(s["id"])
	responseObject(t, apiRequest(t, a, p, "POST", path+"/pause/", nil), 200)
	if claimed, err := a.triggerSchedule(context.Background(), due.Add(72*time.Hour)); err != nil || claimed {
		t.Fatalf("paused schedule was dispatched: %v %v", claimed, err)
	}
	listed := responseObject(t, apiRequest(t, a, p, "GET", "/api/pipeline/schedules/?state=paused&repeat=daily&mine=true&search=定时验收", nil), 200)
	if num(listed["count"]) != 1 || !truth(obj(list(listed["results"])[0])["can_resume"]) {
		t.Fatal("paused plan query lost record or permissions")
	}
	// Resume after a missed daily slot advances to the next original slot.
	if _, err := a.Pool.Exec(context.Background(), "UPDATE platform_processing_schedules SET next_run_at=now()-interval '3 days' WHERE id=$1", s["id"]); err != nil {
		t.Fatal(err)
	}
	responseObject(t, apiRequest(t, a, p, "POST", path+"/resume/", nil), 200)
	stored := storedSchedule(t, a, s["id"])
	next, _ := time.Parse(time.RFC3339Nano, str(stored["next_run_at"]))
	if stored["status"] != "active" || !next.After(time.Now()) || obj(obj(stored["scope"])["candidate_filters"])["name"] != "研发" {
		t.Fatal("resume changed scope or scheduled a catchup storm")
	}
	responseObject(t, apiRequest(t, a, p, "POST", path+"/cancel/", nil), 200)
	responseObject(t, apiRequest(t, a, p, "POST", path+"/resume/", nil), 409)
}
