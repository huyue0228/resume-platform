package platform

import (
	"context"
	"testing"
	"time"
)

func waitSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(8 * time.Second):
		t.Fatal(label)
	}
}

func TestCancelDuringModelPreventsResultWrite(t *testing.T) {
	f := newPipelineFixture(t)
	entered, stopped := make(chan struct{}), make(chan struct{})
	f.analyzeHook = func(ctx context.Context, _ Object) error {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	run := f.submit(t)
	done := make(chan struct{})
	go func() { defer close(done); f.executeJob(t, run, context.Background()) }()
	waitSignal(t, entered, "model did not start")
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/runs/"+str(run["id"])+"/cancel/", Object{}), 200)
	waitSignal(t, stopped, "model request ignored cancellation")
	waitSignal(t, done, "cancelled analysis did not settle")
	assertNoDecision(t, f.a, run)
	current, err := f.a.get(context.Background(), f.a.Pool, "core_processingrun", run["id"])
	if err != nil || current["status"] != "cancelled" {
		t.Fatalf("cancellation status=%v err=%v", current["status"], err)
	}
}

func TestWorkerShutdownThenLeaseRecoveryWritesOnce(t *testing.T) {
	f := newPipelineFixture(t)
	entered := make(chan struct{})
	f.analyzeHook = func(ctx context.Context, _ Object) error { close(entered); <-ctx.Done(); return ctx.Err() }
	run := f.submit(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.executeJob(t, run, ctx) }()
	waitSignal(t, entered, "first worker did not reach model")
	cancel()
	waitSignal(t, done, "worker shutdown did not finish")
	assertNoDecision(t, f.a, run)
	ctx = context.Background()
	// 模拟进程消失后租约到期，不篡改材料或业务结果。
	if _, err := f.a.Pool.Exec(ctx, "UPDATE core_candidateworkflow SET active_processing_expires_at=now()-interval '1 second' WHERE candidate_id=$1", f.candidate["id"]); err != nil {
		t.Fatal(err)
	}
	f.analyzeHook = nil
	f.executeJob(t, run, ctx)
	current, err := f.a.get(ctx, f.a.Pool, "core_processingrun", run["id"])
	if err != nil || current["status"] != "success" {
		t.Fatalf("recovery status=%v err=%v", brief(current, "status", "error"), err)
	}
	var count int
	if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_agentdispatchdecision WHERE processing_run_id=$1", run["id"]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("decisions=%d err=%v", count, err)
	}
	if f.extractions.Load() != 1 || f.analyses.Load() != 2 {
		t.Fatalf("recovery did not reuse text: extract=%d analyze=%d", f.extractions.Load(), f.analyses.Load())
	}
	f.executeJob(t, run, ctx)
	if f.analyses.Load() != 2 {
		t.Fatal("finished job was executed again")
	}
}

func TestManualAssignmentDuringAnalysisWins(t *testing.T) {
	f := newPipelineFixture(t)
	entered := make(chan struct{})
	f.analyzeHook = func(ctx context.Context, _ Object) error { close(entered); <-ctx.Done(); return ctx.Err() }
	run := f.submit(t)
	done := make(chan struct{})
	go func() { defer close(done); f.executeJob(t, run, context.Background()) }()
	waitSignal(t, entered, "model did not start")
	at, err := f.a.manualAssign(context.Background(), f.resume["id"], f.job["department_id"], "并发人工分配验收", f.p)
	if err != nil {
		t.Fatal(err)
	}
	waitSignal(t, done, "manual change did not stop analysis")
	assertNoDecision(t, f.a, run)
	current, err := f.a.get(context.Background(), f.a.Pool, "core_assignmentattempt", at["id"])
	if err != nil || current["status"] != "pending_dispatch" || current["source"] != "manual" {
		t.Fatal("analysis overwrote manual assignment")
	}
	item, err := one(context.Background(), f.a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1", run["id"])
	if err != nil || item["status"] != "skipped_manual_change" {
		t.Fatalf("manual conflict outcome=%v err=%v", brief(item, "status", "error_code"), err)
	}
}

func TestEditedApplicationStandardInvalidatesInFlightAnalysis(t *testing.T) {
	f := newPipelineFixture(t)
	entered, resume := make(chan struct{}), make(chan struct{})
	f.analyzeHook = func(ctx context.Context, _ Object) error {
		close(entered)
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	run := f.submit(t)
	done := make(chan struct{})
	go func() { defer close(done); f.executeJob(t, run, context.Background()) }()
	waitSignal(t, entered, "model did not start")
	editTestStandard(t, f)
	close(resume)
	waitSignal(t, done, "invalidated analysis did not finish")
	decision, err := one(context.Background(), f.a.Pool, "SELECT row_to_json(d) FROM core_agentdispatchdecision d WHERE processing_run_id=$1", run["id"])
	if err != nil || decision["error_code"] != "ai_reference_invalidated" {
		t.Fatalf("stale job result saved: %v %v", brief(decision, "error_code", "recommendation"), err)
	}
	var count int
	if err = f.a.Pool.QueryRow(context.Background(), "SELECT count(*) FROM core_assignmentattempt WHERE agent_decision_id=$1", decision["id"]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale job created an attempt: count=%d err=%v", count, err)
	}
}

func TestVerifiedAnalysisReuseInvalidatesChangedApplicationStandard(t *testing.T) {
	f := newPipelineFixture(t)
	f.resultHook = func(result Object) { obj(result["profile"])["tags"] = []any{} }
	ctx := context.Background()
	f.executeJob(t, f.submit(t), ctx)
	rerun := func() Object {
		reply := responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/run/", Object{"step": "step2", "scope": Object{"candidate_ids": []any{f.candidate["id"]}, "force_reprocess": true}}), 202)
		run := obj(list(reply["processing_runs"])[0])
		f.executeJob(t, run, ctx)
		return run
	}
	if _, err := f.a.save(ctx, f.a.Pool, "core_job", f.job["id"], Object{"headcount": 2}); err != nil {
		t.Fatal(err)
	}
	run := rerun()
	if f.analyses.Load() != 1 || f.extractions.Load() != 1 {
		t.Fatalf("unchanged content repeated work: model=%d extract=%d", f.analyses.Load(), f.extractions.Load())
	}
	decision, err := one(ctx, f.a.Pool, "SELECT row_to_json(d) FROM core_agentdispatchdecision d WHERE processing_run_id=$1", run["id"])
	if err != nil || str(obj(obj(decision["kernel_result"])["manifest"])["reused_from_task_id"]) == "" {
		t.Fatal("reuse lost provenance")
	}
	editTestStandard(t, f)
	rerun()
	if f.analyses.Load() != 2 {
		t.Fatal("changed job reused stale analysis")
	}
}

func TestConcurrentCandidatesRespectRunCapacity(t *testing.T) {
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	f := newPipelineFixture(t)
	ctx := context.Background()
	c := mustSave(t, f.a, "core_candidate", Object{"name": token(6), "phone": "13812345678", "identity_hash": token(32), "household_province": "上海", "highest_education": "bachelor"})
	mustSave(t, f.a, "core_resume", Object{"candidate_id": c["id"], "apply_id": token(6), "entity": "YLS", "position_name": f.job["public_name"], "resume_file": f.resume["resume_file"]})
	run := f.submit(t, f.candidate["id"], c["id"])
	f.executeJob(t, run, ctx)
	var reserved, attempts int
	if err := f.a.Pool.QueryRow(ctx, "SELECT used_count FROM core_processingrunjobcapacity WHERE run_id=$1 AND job_id=$2", run["id"], f.job["id"]).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_assignmentattempt a JOIN core_agentdispatchdecision d ON a.agent_decision_id=d.id WHERE d.processing_run_id=$1", run["id"]).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if reserved != 1 || attempts != 1 {
		t.Fatalf("capacity overbooking: reserved=%d attempts=%d", reserved, attempts)
	}
	current, err := f.a.get(ctx, f.a.Pool, "core_processingrun", run["id"])
	if err != nil || current["status"] != "success" {
		t.Fatalf("concurrent run failed: %v %v", brief(current, "status", "error"), err)
	}
}

func TestFeedbackRejectWaitsForScheduleThenPassCompletesWorkflow(t *testing.T) {
	f := newPipelineFixture(t)
	ctx := context.Background()
	second := mustSave(t, f.a, "core_resume", Object{"candidate_id": f.candidate["id"], "apply_id": token(6), "entity": "YLS", "position_name": f.job["public_name"], "resume_file": f.resume["resume_file"], "volunteer_rank": 2})
	if _, err := f.a.save(ctx, f.a.Pool, "core_resume", f.resume["id"], Object{"volunteer_rank": 1}); err != nil {
		t.Fatal(err)
	}
	at, err := f.a.manualAssign(ctx, f.resume["id"], f.job["department_id"], "首志愿人工分配", f.p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.mutateAttempt(ctx, at["id"], "dispatch_welink", Object{}, f.p, nil); err != nil {
		t.Fatal(err)
	}
	dep, err := f.a.get(ctx, f.a.Pool, "core_department", f.job["department_id"])
	if err != nil {
		t.Fatal(err)
	}
	p := &Principal{User: f.p.User, Contact: Object{"department_id": dep["id"], "is_active": true, "contact_level": "secondary"}, Department: dep, Permissions: map[string]bool{"attempt.view_department": true, "attempt.feedback": true}}
	field, _ := fieldFor(f.a, "core_assignmentattempt", "feedback_reason_code")
	reason := ""
	for _, v := range field.Choices {
		if str(v[0]) != "" && str(v[0]) != "other" {
			reason = str(v[0])
			break
		}
	}
	if _, err = f.a.mutateAttempt(ctx, at["id"], "feedback", Object{"result": "rejected", "reason_code": reason}, p, nil); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_processingrunscopeitem WHERE candidate_id=$1", f.candidate["id"]).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("department rejection must not queue work: %d %v", queued, err)
	}
	view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
	if err != nil || view["system_status"] != "raw" || view["workflow_status"] != "waiting_next" || num(obj(view["current_resume"])["id"]) != num(second["id"]) {
		t.Fatalf("not waiting for next application: %v %v", brief(view, "system_status", "workflow_status", "current_resume"), err)
	}
	assertApplicationState(t, f, f.resume, "department_rejected")
	schedule, due := scheduleFixture(t, f.a, f.p, "once", Object{"system_statuses": []any{"raw"}, "candidate_filters": Object{"name": f.candidate["name"]}})
	if claimed, triggerErr := f.a.triggerSchedule(ctx, due); triggerErr != nil || !claimed {
		t.Fatalf("schedule: %v %v", claimed, triggerErr)
	}
	schedule = storedSchedule(t, f.a, schedule["id"])
	run, err := f.a.get(ctx, f.a.Pool, "core_processingrun", schedule["last_run_id"])
	if err != nil {
		t.Fatal(err)
	}
	f.executeJob(t, run, ctx)
	next, err := one(ctx, f.a.Pool, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE resume_id=$1 ORDER BY id DESC LIMIT 1", second["id"])
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"dispatch_welink"} {
		if _, err = f.a.mutateAttempt(ctx, next["id"], action, Object{}, f.p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.a.mutateAttempt(ctx, next["id"], "feedback", Object{"result": "passed"}, p, nil); err != nil {
		t.Fatal(err)
	}
	workflow, err := one(ctx, f.a.Pool, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1", f.candidate["id"])
	if err != nil || workflow["status"] != "passed" || num(workflow["current_resume_id"]) != num(second["id"]) {
		t.Fatal("passing next volunteer did not finish workflow")
	}
	if _, err = f.a.mutateAttempt(ctx, next["id"], "feedback", Object{"result": "passed"}, p, nil); err == nil {
		t.Fatal("duplicate feedback was accepted")
	}
}
