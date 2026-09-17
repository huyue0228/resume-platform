package platform

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type preparationQueries struct {
	mu  sync.Mutex
	sql []string
}

func (q *preparationQueries) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sql = append(q.sql, data.SQL)
	return ctx
}
func (*preparationQueries) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (q *preparationQueries) take() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	sql := q.sql
	q.sql = nil
	return sql
}
func tracePreparationQueries(t *testing.T, a *App) *preparationQueries {
	t.Helper()
	q := &preparationQueries{}
	cfg := a.Pool.Config()
	cfg.ConnConfig.Tracer = q
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Pool.Close()
	a.Pool = pool
	return q
}
func preparationCandidate(t *testing.T, f *pipelineFixture) Object {
	t.Helper()
	c := mustSave(t, f.a, "core_candidate", Object{"name": token(6), "phone": "13812345678", "identity_hash": token(32), "household_province": "上海", "highest_education": "bachelor"})
	mustSave(t, f.a, "core_resume", Object{"candidate_id": c["id"], "apply_id": token(6), "entity": "YLS", "position_name": f.job["public_name"], "resume_file": f.resume["resume_file"]})
	return c
}

func TestPreparationPublicQueriesDoNotScaleWithCandidatesOrJobs(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	a, ctx := f.a, context.Background()
	trace := tracePreparationQueries(t, a)
	sizes := []int{2, 12}
	if os.Getenv("TEST_PREPARATION_LARGE_BATCH") == "1" {
		sizes = append(sizes, 479)
	}
	for _, size := range sizes {
		// Grow both dimensions: public reads must stay constant, not candidates * jobs.
		for i := 0; i < min(size, 100); i++ {
			job := mustSave(t, a, "core_job", Object{"public_name": token(8), "position_name": token(8), "entity": "YLS", "responsibilities": "批量查询测试", "department_id": f.job["department_id"]})
			mustSave(t, a, "core_jobmajor", Object{"job_id": job["id"], "major": "计算机"})
		}
		ids := []any{}
		for i := 0; i < size; i++ {
			ids = append(ids, preparationCandidate(t, f)["id"])
		}
		run := f.submit(t, ids...)
		if err := a.beginStage(ctx, run["id"], "preparing"); err != nil {
			t.Fatal(err)
		}
		trace.take()
		started := time.Now()
		if err := a.prepareRunSnapshots(ctx, run, Object{}); err != nil {
			t.Fatal(err)
		}
		queries := trace.take()
		for _, from := range []string{`FROM "core_school"`, `FROM "core_schooltag"`, `FROM "core_department"`, "FROM core_job j", "FROM core_jobmajor m", "FROM core_schooltagrule r", "FROM core_schooltagruletag l", "FROM core_schooltagruleeducation l"} {
			count := 0
			for _, sql := range queries {
				if strings.Contains(sql, from) {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("batch size=%d: public read %q ran %d times, want once", size, from, count)
			}
		}
		if len(queries) > 20+5*size {
			t.Fatalf("preparation queries grew beyond one public load plus per-candidate work: size=%d queries=%d", size, len(queries))
		}
		stage, err := one(ctx, a.Pool, "SELECT row_to_json(s) FROM core_processingrunstage s WHERE run_id=$1 AND step='preparing'", run["id"])
		if err != nil || num(stage["processed_count"]) != int64(size) || stage["status"] != "running" {
			t.Fatalf("progress must be visible before finishStage: %v %v", stage, err)
		}
		t.Logf("prepared %d candidates with %d SQL calls in %s", size, len(queries), time.Since(started))
	}
	if f.extractions.Load() != 0 || f.analyses.Load() != 0 {
		t.Fatal("snapshot preparation extracted text or called the model")
	}
}

func TestPreparationPreservesAdmissionJobHashesAndRefreshesNextPass(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	a, ctx := f.a, context.Background()
	tag := mustSave(t, a, "core_schooltag", Object{"code": token(6), "name": token(6)})
	school := mustSave(t, a, "core_school", Object{"name": token(8), "province": "上海", "school_tag_id": tag["id"]})
	candidate, err := a.save(ctx, a.Pool, "core_candidate", f.candidate["id"], Object{"first_degree_school": school["name"], "highest_degree_school": school["name"]})
	if err != nil {
		t.Fatal(err)
	}
	rule := mustSave(t, a, "core_schooltagrule", Object{"name": token(8), "priority": -1})
	for _, degree := range []string{"first", "highest"} {
		mustSave(t, a, "core_schooltagruletag", Object{"rule_id": rule["id"], "school_tag_id": tag["id"], "degree_type": degree})
	}
	mustSave(t, a, "core_schooltagruleeducation", Object{"rule_id": rule["id"], "education": "bachelor"})
	for _, major := range []string{"软件工程", "计算机科学"} {
		mustSave(t, a, "core_jobmajor", Object{"job_id": f.job["id"], "major": major})
	}
	data, err := a.loadPreparationData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Compare bulk content with the live validation path, including ordered majors.
	for _, value := range data.jobs {
		job := obj(value)
		live, err := a.get(ctx, a.Pool, "core_job", data.jobIDs[str(job["ref"])])
		if err != nil {
			t.Fatal(err)
		}
		content, err := a.jobContent(ctx, a.Pool, live)
		if err != nil || fingerprint(content) != job["content_hash"] {
			t.Fatal("batch-loaded job hash differs from live validation")
		}
	}
	run := Object{"scope": Object{}}
	frozen, err := a.freezeCase(ctx, run, candidate, Object{}, data)
	if err != nil {
		t.Fatal(err)
	}
	preflight := obj(frozen["preflight"])
	if preflight["status"] != "ready" || preflight["admission_rule_ref"] != a.ref("rule", rule["id"]) || preflight["current_volunteer_ref"] != a.ref("volunteer", f.resume["id"]) || len(list(preflight["job_refs"])) != 1 || str(list(preflight["job_refs"])[0]) != "standard_"+str(f.job["id"]) {
		t.Fatalf("admission, volunteer or allowed job changed: %v", preflight)
	}
	if _, err = a.save(ctx, a.Pool, "core_job", f.job["id"], Object{"responsibilities": "新的职责"}); err != nil {
		t.Fatal(err)
	}
	before := fingerprint(data.jobs)
	other := clone(candidate)
	other["highest_education"] = "master"
	rejected, err := a.freezeCase(ctx, run, other, Object{}, data)
	if err != nil || obj(rejected["preflight"])["status"] != "education_not_eligible" {
		t.Fatal("candidate-specific admission was shared between candidates")
	}
	if before != fingerprint(data.jobs) || rejected["task_id"] == frozen["task_id"] {
		t.Fatal("shared public data was mutated or candidate task IDs were reused")
	}
	next, err := a.loadPreparationData(ctx)
	if err != nil || fingerprint(next.jobs) == before {
		t.Fatal("a new preparation pass did not read updated job data")
	}
}

func TestPreparationProgressSurvivesInterruptedSnapshotWrite(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	a, ctx := f.a, context.Background()
	second := preparationCandidate(t, f)
	run := f.submit(t, f.candidate["id"], second["id"])
	if err := a.beginStage(ctx, run["id"], "preparing"); err != nil {
		t.Fatal(err)
	}
	lock, err := a.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, "SELECT id FROM core_processingrunscopeitem WHERE run_id=$1 AND candidate_id=$2 FOR UPDATE", run["id"], second["id"]); err != nil {
		t.Fatal(err)
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.prepareRunSnapshots(workCtx, run, Object{}) }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var processed int
		if err = a.Pool.QueryRow(ctx, "SELECT processed_count FROM core_processingrunstage WHERE run_id=$1 AND step='preparing'", run["id"]).Scan(&processed); err != nil {
			t.Fatal(err)
		}
		if processed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first committed snapshot was not reflected while the second was blocked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("preparation did not cancel: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("blocked preparation ignored cancellation")
	}
	// pgx may report cancellation before PostgreSQL receives its cancel packet.
	// Keep the row lock until the blocked writer has stopped on the server.
	deadline = time.Now().Add(8 * time.Second)
	for {
		var waiting int
		if err = a.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND state='active' AND query LIKE 'UPDATE "core_processingrunscopeitem"%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled snapshot writer remained active")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := one(ctx, a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1 AND candidate_id=$2", run["id"], f.candidate["id"])
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash between persisting a snapshot and refreshing its counter.
	if _, err = a.Pool.Exec(ctx, "UPDATE core_processingrunstage SET processed_count=0 WHERE run_id=$1 AND step='preparing'", run["id"]); err != nil {
		t.Fatal(err)
	}
	trace := tracePreparationQueries(t, a)
	if err = a.prepareRunSnapshots(ctx, run, Object{}); err != nil {
		t.Fatal(err)
	}
	saved := 0
	for _, sql := range trace.take() {
		if strings.HasPrefix(sql, `UPDATE "core_processingrunscopeitem"`) {
			saved++
		}
	}
	if saved != 1 {
		t.Fatalf("resume rewrote completed snapshots: writes=%d, want 1", saved)
	}
	var processed, prepared int
	if err = a.Pool.QueryRow(ctx, "SELECT processed_count FROM core_processingrunstage WHERE run_id=$1 AND step='preparing'", run["id"]).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if err = a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_processingrunscopeitem WHERE run_id=$1 AND kernel_snapshot<>'{}'::jsonb", run["id"]).Scan(&prepared); err != nil || processed != 2 || prepared != 2 {
		t.Fatalf("recovered counts differ from durable snapshots: progress=%d prepared=%d err=%v", processed, prepared, err)
	}
	reloaded, err := a.get(ctx, a.Pool, "core_processingrunscopeitem", first["id"])
	if err != nil || obj(reloaded["kernel_snapshot"])["task_id"] != obj(first["kernel_snapshot"])["task_id"] {
		t.Fatal("resume replaced the existing candidate snapshot")
	}
}
