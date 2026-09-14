package platform

import (
	"context"
	"errors"
	"testing"
)

func nextApplication(t *testing.T, f *pipelineFixture) Object {
	t.Helper()
	return mustSave(t, f.a, "core_resume", Object{"candidate_id": f.candidate["id"], "apply_id": token(10), "entity": f.resume["entity"], "position_name": f.resume["position_name"], "resume_file": f.resume["resume_file"]})
}

func assertApplicationState(t *testing.T, f *pipelineFixture, resume Object, expected string) {
	t.Helper()
	state, err := one(context.Background(), f.a.Pool, "SELECT row_to_json(a) FROM platform_applications a WHERE resume_id=$1", resume["id"])
	if err != nil || state["status"] != expected {
		t.Fatalf("application %v: state=%v want=%s err=%v", resume["id"], state, expected, err)
	}
	if closedApplication(expected) && state["completed_at"] == nil {
		t.Fatal("closed application has no completion time")
	}
}

func lowAssessment(result Object) {
	for _, v := range list(result["matches"]) {
		match := obj(v)
		match["score"] = .1
		for k := range obj(match["dimensions"]) {
			obj(match["dimensions"])[k] = .1
		}
	}
}

func TestAIRejectionContinuesNextApplicationInSameRun(t *testing.T) {
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	second := nextApplication(t, f)
	// Different applications use different standards and tag catalogs.
	config, err := f.a.poolPolicy(context.Background(), f.a.Pool)
	if err != nil {
		t.Fatal(err)
	}
	policy := obj(config["policy"])
	standard := clone(obj(list(policy["standards"])[0]))
	standard["code"], standard["name"], standard["application_names"] = "second_standard", "第二志愿标准", []any{"第二投递"}
	policy["standards"] = append(list(policy["standards"]), standard)
	if _, err = f.a.Pool.Exec(context.Background(), "UPDATE platform_pool_policy SET policy=$1::jsonb", string(canonicalJSON(policy, false))); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.save(context.Background(), f.a.Pool, "core_resume", second["id"], Object{"position_name": "第二投递"}); err != nil {
		t.Fatal(err)
	}
	seen := []string{}
	f.analyzeHook = func(_ context.Context, request Object) error {
		jobs := list(obj(request["scope"])["jobs"])
		if len(jobs) != 1 {
			t.Errorf("expected exactly one current standard: %v", jobs)
		}
		seen = append(seen, str(obj(jobs[0])["ref"]))
		return nil
	}
	f.resultHook = func(result Object) {
		if f.analyses.Load() == 1 {
			lowAssessment(result)
		}
	}
	run := f.submit(t)
	f.executeJob(t, run, context.Background())
	assertApplicationState(t, f, f.resume, "ai_rejected")
	assertApplicationState(t, f, second, "pending_dispatch")
	if f.analyses.Load() != 2 || len(seen) != 2 || seen[0] == seen[1] || seen[1] != "second_standard" {
		t.Fatalf("wrong continuation: calls=%d standards=%v", f.analyses.Load(), seen)
	}
	var decisions, runs, finished int
	ctx := context.Background()
	if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_agentdispatchdecision WHERE processing_run_id=$1", run["id"]).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err = f.a.Pool.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE status='success') FROM core_processingrunscopeitem WHERE candidate_id=$1", f.candidate["id"]).Scan(&runs, &finished); err != nil {
		t.Fatal(err)
	}
	item, err := one(ctx, f.a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1", run["id"])
	if err != nil || item["workflow_revision_at_submit"] != nil || num(obj(item["kernel_snapshot"])["continuation_revision"]) != 1 {
		t.Fatalf("continuation changed original submission revision: %v %v", brief(item, "workflow_revision_at_submit", "workflow_revision_at_prepare"), err)
	}
	if decisions != 2 || runs != 1 || finished != 1 {
		t.Fatalf("lost per-application history or spawned extra run: %d %d %d", decisions, runs, finished)
	}
	view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
	if err != nil || len(view["application_history"].([]Object)) < 4 {
		t.Fatalf("history missing: %v %v", view["application_history"], err)
	}
	// A repeated scheduled or explicitly forced batch cannot disturb department work.
	replay := responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/run/", Object{"step": "step2", "scope": Object{"candidate_ids": []any{f.candidate["id"]}, "force_reprocess": true}}), 202)
	f.executeJob(t, obj(list(replay["processing_runs"])[0]), ctx)
	if f.analyses.Load() != 2 {
		t.Fatal("batch re-entered completed AI stage")
	}
	assertApplicationState(t, f, f.resume, "ai_rejected")
	assertApplicationState(t, f, second, "pending_dispatch")
}

func TestAllAIRejectionsEnterTalentPoolAndCannotReopen(t *testing.T) {
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	second := nextApplication(t, f)
	f.resultHook = lowAssessment
	run := f.submit(t)
	ctx := context.Background()
	f.executeJob(t, run, ctx)
	assertApplicationState(t, f, f.resume, "ai_rejected")
	assertApplicationState(t, f, second, "ai_rejected")
	view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
	if err != nil || view["system_status"] != "talent_pool" || view["workflow_status"] != "talent_pool" {
		t.Fatalf("not in talent pool: %v %v", brief(view, "system_status", "workflow_status"), err)
	}
	responseObject(t, apiRequest(t, f.a, f.p, "GET", "/api/candidates/?system_status=talent_pool", nil), 200)
	schedule, due := scheduleFixture(t, f.a, f.p, "once", Object{"system_statuses": []any{"raw"}})
	if _, err = f.a.triggerSchedule(ctx, due); err != nil {
		t.Fatal(err)
	}
	if storedSchedule(t, f.a, schedule["id"])["last_run_id"] != nil {
		t.Fatal("talent pool leaked into pending batch")
	}
	replay := responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/run/", Object{"step": "step2", "scope": Object{"candidate_ids": []any{f.candidate["id"]}, "force_reprocess": true}}), 202)
	f.executeJob(t, obj(list(replay["processing_runs"])[0]), ctx)
	if f.analyses.Load() != 2 {
		t.Fatal("reprocessed exhausted applications")
	}
	decision, err := one(ctx, f.a.Pool, "SELECT row_to_json(d) FROM core_agentdispatchdecision d WHERE resume_id=$1", second["id"])
	if err != nil {
		t.Fatal(err)
	}
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/agent-decisions/"+str(decision["id"])+"/retry/", Object{}), 409)
	if _, err = f.a.manualAssign(ctx, second["id"], f.job["department_id"], "attempt to overwrite", f.p); err == nil {
		t.Fatal("manual reassignment overwrote terminal application")
	}
}

func TestTechnicalFailureDoesNotConsumeApplication(t *testing.T) {
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	second := nextApplication(t, f)
	f.analyzeHook = func(context.Context, Object) error { return errors.New("fixture connection failure") }
	ctx := context.Background()
	f.executeJob(t, f.submit(t), ctx)
	assertApplicationState(t, f, f.resume, "blocked")
	view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
	if err != nil || view["system_status"] == "talent_pool" {
		t.Fatalf("technical error became rejection: %v %v", view["system_status"], err)
	}
	for _, v := range list(view["resumes"]) {
		if num(obj(v)["id"]) == num(second["id"]) && obj(v)["lifecycle_status"] != "pending" {
			t.Fatal("technical failure consumed next application")
		}
	}
	f.analyzeHook = nil
	f.executeJob(t, f.submit(t), ctx)
	assertApplicationState(t, f, f.resume, "pending_dispatch")
	if f.analyses.Load() != 2 {
		t.Fatalf("did not retry same application: %d", f.analyses.Load())
	}
}

func TestMixedAIAndDepartmentRejectionsExhaustApplications(t *testing.T) {
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	second := nextApplication(t, f)
	f.resultHook = func(result Object) {
		if f.analyses.Load() == 1 {
			lowAssessment(result)
		}
	}
	ctx := context.Background()
	f.executeJob(t, f.submit(t), ctx)
	at, err := one(ctx, f.a.Pool, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE resume_id=$1", second["id"])
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
	if _, err = f.a.mutateAttempt(ctx, at["id"], "feedback", Object{"result": "rejected", "reason_code": "other", "note": "不适合当前部门"}, p, nil); err != nil {
		t.Fatal(err)
	}
	assertApplicationState(t, f, f.resume, "ai_rejected")
	assertApplicationState(t, f, second, "department_rejected")
	view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
	if err != nil || view["system_status"] != "talent_pool" {
		t.Fatalf("mixed failures not exhausted: %v %v", view["system_status"], err)
	}
	departmental, err := f.a.serialize(ctx, "candidates", f.candidate, p, true)
	if err != nil || departmental["system_status"] != "screening_rejected" || departmental["application_history"] != nil {
		t.Fatalf("department scope leaked candidate state/history: %v %v", departmental["system_status"], err)
	}
	var active, used, runs int
	if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM platform_pool_memberships WHERE candidate_id=$1 AND status IN ('pending_review','pending_allocation','allocated','needs_reanalysis')", f.candidate["id"]).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err = f.a.Pool.QueryRow(ctx, "SELECT used_count FROM core_processingrunjobcapacity WHERE id=$1", at["capacity_reservation_id"]).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_processingrunscopeitem WHERE candidate_id=$1", f.candidate["id"]).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if active != 0 || used != 0 || runs != 1 {
		t.Fatalf("rejection leaked pool/capacity or queued a run: %d %d %d", active, used, runs)
	}
	if _, err = f.a.mutateAttempt(ctx, at["id"], "feedback", Object{"result": "rejected", "reason_code": "other", "note": "duplicate"}, p, nil); err == nil {
		t.Fatal("duplicate rejection accepted")
	}
}

func TestApplicationMigrationPreservesRejectedHistory(t *testing.T) {
	a := isolatedInboxApp(t, false)
	ctx := context.Background()
	c := mustSave(t, a, "core_candidate", Object{"name": "历史候选人", "identity_hash": token(32)})
	first := mustSave(t, a, "core_resume", Object{"candidate_id": c["id"], "apply_id": token(10)})
	second := mustSave(t, a, "core_resume", Object{"candidate_id": c["id"], "apply_id": token(10)})
	w := mustSave(t, a, "core_candidateworkflow", Object{"candidate_id": c["id"], "status": "archived", "archive_reason": "all_rejected", "current_resume_id": second["id"]})
	decision := mustSave(t, a, "core_agentdispatchdecision", Object{"workflow_id": w["id"], "resume_id": first["id"], "recommendation": "archive", "reason": "原始评估原因"})
	department := mustSave(t, a, "core_department", Object{"name": "历史接收部门", "level": 1})
	at := mustSave(t, a, "core_assignmentattempt", Object{"current_department_id": department["id"], "initial_department_id": department["id"], "workflow_id": w["id"], "resume_id": second["id"], "attempt_no": 1, "status": "rejected", "feedback_result": "rejected", "feedback_note": "原始部门原因"})
	if err := a.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := &pipelineFixture{a: a, candidate: c}
	assertApplicationState(t, f, first, "ai_rejected")
	assertApplicationState(t, f, second, "department_rejected")
	stored, err := a.get(ctx, a.Pool, "core_candidateworkflow", w["id"])
	if err != nil || stored["status"] != "talent_pool" {
		t.Fatalf("legacy exhaustion not migrated: %v %v", stored, err)
	}
	d, err := a.get(ctx, a.Pool, "core_agentdispatchdecision", decision["id"])
	if err != nil || d["reason"] != "原始评估原因" {
		t.Fatal("historical AI decision was changed")
	}
	feedback, err := a.get(ctx, a.Pool, "core_assignmentattempt", at["id"])
	if err != nil || feedback["feedback_note"] != "原始部门原因" {
		t.Fatal("historical feedback was changed")
	}
	var count int
	if err = a.Pool.QueryRow(ctx, "SELECT count(*) FROM platform_application_events").Scan(&count); err != nil || count != 2 {
		t.Fatalf("migration not idempotent: %d %v", count, err)
	}
}

func TestNextStandardUnavailableDoesNotSkipApplication(t *testing.T) {
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	second := nextApplication(t, f)
	third := nextApplication(t, f)
	ctx := context.Background()
	if _, err := f.a.save(ctx, f.a.Pool, "core_resume", second["id"], Object{"position_name": "未配置投递标准"}); err != nil {
		t.Fatal(err)
	}
	f.resultHook = lowAssessment
	f.executeJob(t, f.submit(t), ctx)
	assertApplicationState(t, f, f.resume, "ai_rejected")
	assertApplicationState(t, f, second, "blocked")
	if f.analyses.Load() != 1 {
		t.Fatal("evaluated unavailable standard or skipped to third application")
	}
	view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
	if err != nil || view["workflow_status"] == "talent_pool" || num(obj(view["current_resume"])["id"]) != num(second["id"]) {
		t.Fatalf("configuration failure crossed application boundary: %v %v", view["workflow_status"], err)
	}
	for _, v := range list(view["resumes"]) {
		if num(obj(v)["id"]) == num(third["id"]) && obj(v)["lifecycle_status"] != "pending" {
			t.Fatal("third application changed")
		}
	}
}

func TestContinuationRecoveryDoesNotReevaluateRejectedApplication(t *testing.T) {
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	second := nextApplication(t, f)
	f.resultHook = func(result Object) {
		if f.analyses.Load() == 1 {
			lowAssessment(result)
		}
	}
	entered := make(chan struct{})
	f.analyzeHook = func(ctx context.Context, _ Object) error {
		if f.analyses.Load() == 2 {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	run := f.submit(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.executeJob(t, run, ctx) }()
	waitSignal(t, entered, "second application did not start")
	cancel()
	waitSignal(t, done, "shutdown did not finish")
	assertApplicationState(t, f, f.resume, "ai_rejected")
	if _, err := f.a.Pool.Exec(context.Background(), "UPDATE core_candidateworkflow SET active_processing_expires_at=now()-interval '1 second' WHERE candidate_id=$1", f.candidate["id"]); err != nil {
		t.Fatal(err)
	}
	f.analyzeHook = nil
	f.executeJob(t, run, context.Background())
	assertApplicationState(t, f, second, "pending_dispatch")
	var firstDecisions int
	if err := f.a.Pool.QueryRow(context.Background(), "SELECT count(*) FROM core_agentdispatchdecision WHERE resume_id=$1", f.resume["id"]).Scan(&firstDecisions); err != nil || firstDecisions != 1 {
		t.Fatalf("recovery rewrote rejected application: %d %v", firstDecisions, err)
	}
	if f.analyses.Load() != 3 {
		t.Fatalf("wrong recovery calls: %d", f.analyses.Load())
	}
}

func TestScheduledFixedScopeDoesNotRepeatAllocation(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	ctx := context.Background()
	f.executeJob(t, f.submit(t), ctx)
	assertApplicationState(t, f, f.resume, "pending_dispatch")
	// Plans created by earlier versions may still contain the force flag.
	schedule, due := scheduleFixture(t, f.a, f.p, "once", Object{"candidate_ids": []any{f.candidate["id"]}, "force_reprocess": true})
	if _, err := f.a.triggerSchedule(ctx, due); err != nil {
		t.Fatal(err)
	}
	run, err := f.a.get(ctx, f.a.Pool, "core_processingrun", storedSchedule(t, f.a, schedule["id"])["last_run_id"])
	if err != nil {
		t.Fatal(err)
	}
	f.executeJob(t, run, ctx)
	assertApplicationState(t, f, f.resume, "pending_dispatch")
	if f.analyses.Load() != 1 {
		t.Fatal("old fixed-id schedule repeated allocation")
	}
}

func TestApplicationAddedDuringAssessmentWaitsForNextBatch(t *testing.T) {
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	var second Object
	f.resultHook = func(result Object) {
		if f.analyses.Load() == 1 {
			lowAssessment(result)
			second = nextApplication(t, f)
		}
	}
	ctx := context.Background()
	run := f.submit(t)
	f.executeJob(t, run, ctx)
	assertApplicationState(t, f, f.resume, "ai_rejected")
	view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
	if err != nil || view["system_status"] != "raw" || view["workflow_status"] != "waiting_next" {
		t.Fatalf("unseen application incorrectly exhausted: %v %v", brief(view, "system_status", "workflow_status"), err)
	}
	item, err := one(ctx, f.a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1", run["id"])
	if err != nil || item["reason_code"] != "waiting_next" {
		t.Fatalf("task reported false exhaustion: %v %v", item["reason_code"], err)
	}
	f.executeJob(t, f.submit(t), ctx)
	assertApplicationState(t, f, second, "pending_dispatch")
	if f.analyses.Load() != 2 {
		t.Fatal("new batch repeated rejected application")
	}
}
