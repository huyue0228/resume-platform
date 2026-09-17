package platform

import (
	"context"
	"strings"
	"testing"
)

func TestDecisionWorkflowFilterUsesExactRelationID(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p := adminPrincipal(t, a)
	ctx := context.Background()
	var expected any
	for _, id := range []int{12, 112, 120} {
		c := mustSave(t, a, "core_candidate", Object{"name": str(id), "identity_hash": token(32)})
		if _, err := a.Pool.Exec(ctx, "SELECT setval(pg_get_serial_sequence('core_candidateworkflow','id'),$1,false)", id); err != nil {
			t.Fatal(err)
		}
		wf := mustSave(t, a, "core_candidateworkflow", Object{"candidate_id": c["id"]})
		r := mustSave(t, a, "core_resume", Object{"candidate_id": c["id"], "apply_id": token(6)})
		d := mustSave(t, a, "core_agentdispatchdecision", Object{"workflow_id": wf["id"], "resume_id": r["id"], "summary": "candidate " + str(id)})
		if id == 12 {
			expected = d["id"]
		}
	}
	response := responseObject(t, apiRequest(t, a, p, "GET", "/api/agent-decisions/?workflow=12&page_size=100", nil), 200)
	if num(response["count"]) != 1 || num(obj(list(response["results"])[0])["id"]) != num(expected) {
		t.Fatalf("workflow 12 included 112/120: %v", response)
	}
}

func TestMechanicalAndSoftwareAssessmentsStayIsolatedInOneRun(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	ctx := context.Background()
	mechanical := mustSave(t, f.a, "core_job", Object{"public_name": "机械校招", "position_name": "机构研发", "entity": "YLS", "responsibilities": "机械设计要求", "headcount": 1, "department_id": f.job["department_id"]})
	installTestPoolPolicy(t, f.a, mechanical)
	c := mustSave(t, f.a, "core_candidate", Object{"name": "机械候选人", "identity_hash": token(32), "household_province": "上海", "highest_education": "bachelor"})
	r := mustSave(t, f.a, "core_resume", Object{"candidate_id": c["id"], "apply_id": token(6), "entity": "YLS", "position_name": mechanical["public_name"], "resume_file": f.resume["resume_file"]})
	f.analyzeHook = func(_ context.Context, request Object) error {
		scope := obj(request["scope"])
		jobs := list(scope["jobs"])
		if len(jobs) != 1 {
			t.Errorf("request includes %d standards", len(jobs))
			return nil
		}
		suffix := str(f.job["id"])
		if scope["volunteer_ref"] == f.a.ref("volunteer", r["id"]) {
			suffix = str(mechanical["id"])
		}
		if obj(jobs[0])["ref"] != "standard_"+suffix || obj(list(scope["tag_catalog"])[0])["code"] != "tag_"+suffix {
			t.Errorf("application or tags crossed candidates: %v", scope)
		}
		if str(obj(jobs[0])["department_ref"]) != "" {
			t.Error("department demand leaked into assessment")
		}
		return nil
	}
	run := f.submit(t, f.candidate["id"], c["id"])
	f.executeJob(t, run, ctx)
	decisions, err := rows(ctx, f.a.Pool, "SELECT row_to_json(d) FROM core_agentdispatchdecision d WHERE processing_run_id=$1", run["id"])
	if err != nil || len(decisions) != 2 {
		t.Fatalf("missing isolated decisions: %v %v", decisions, err)
	}
	for _, d := range decisions {
		if len(list(obj(d["kernel_result"])["matches"])) != 1 {
			t.Fatal("saved mixed analysis")
		}
	}
}

func TestPoolTagsAreAuditedAndStandardChangesRequireReassessment(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	ctx := context.Background()
	f.resultHook = func(result Object) { obj(result["profile"])["tags"] = []any{} }
	f.executeJob(t, f.submit(t), ctx)
	m := poolMemberForTest(t, f)
	if m["status"] != "pending_allocation" {
		t.Fatal("missing tags consumed assignment")
	}
	path := "/api/position-pools/members/" + str(m["id"]) + "/"
	responseObject(t, apiRequest(t, f.a, f.p, "POST", path+"tags/", Object{"revision": m["revision"], "note": "确认设计经历", "tags": []any{Object{"code": "unknown", "status": "supported"}}}), 400)
	responseObject(t, apiRequest(t, f.a, f.p, "POST", path+"tags/", Object{"revision": m["revision"], "note": "确认服务开发项目原文", "tags": []any{Object{"code": "tag_" + str(f.job["id"]), "status": "supported"}}}), 200)
	responseObject(t, apiRequest(t, f.a, f.p, "POST", path+"allocate/", Object{"revision": m["revision"]}), 409)
	detail := responseObject(t, apiRequest(t, f.a, f.p, "GET", path, nil), 200)
	found := false
	for _, v := range list(detail["events"]) {
		e := obj(v)
		if e["kind"] == "tags_revised" && e["actor_id"] != nil && len(list(obj(e["payload"])["previous"])) == 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("manual tag audit lost previous values or actor")
	}
	editTestStandard(t, f)
	detail = responseObject(t, apiRequest(t, f.a, f.p, "GET", path, nil), 200)
	allocationLocalKernel(t, f)
	response := responseObject(t, apiRequest(t, f.a, f.p, "POST", path+"allocate/", Object{"revision": detail["revision"]}), 202)
	if _, _, err := runAllocation(t, f); err != nil {
		t.Fatal(err)
	}
	if response["code"] != "allocation_queued" || poolMemberForTest(t, f)["status"] != "needs_reanalysis" || f.analyses.Load() != 1 {
		t.Fatal("changed standard silently reused admission")
	}
	m = poolMemberForTest(t, f)
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/agent-decisions/"+str(m["decision_id"])+"/retry/", Object{}), 202)
}

func TestPoolMembershipClosesWhenVolunteerChanges(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	ctx := context.Background()
	f.executeJob(t, f.submit(t), ctx)
	m := poolMemberForTest(t, f)
	next := mustSave(t, f.a, "core_resume", Object{"candidate_id": f.candidate["id"], "apply_id": token(6), "entity": "YLS", "position_name": f.job["public_name"], "resume_file": f.resume["resume_file"]})
	tx, err := f.a.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	wf, err := f.a.lockWorkflow(ctx, tx, f.candidate["id"])
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.touchWorkflow(ctx, tx, wf, next); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	m = poolMemberForTest(t, f)
	if m["status"] != "closed" {
		t.Fatal("old volunteer retained active qualification")
	}
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/position-pools/members/"+str(m["id"])+"/review/", Object{"revision": m["revision"], "decision": "approve", "note": "历史记录"}), 410)
}

func TestPoolConfigurationRejectsAmbiguousMappingAndConflictingVersions(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	ctx := context.Background()
	cfg, err := f.a.poolPolicy(ctx, f.a.Pool)
	if err != nil {
		t.Fatal(err)
	}
	policy := obj(cfg["policy"])
	copy := clone(obj(list(policy["standards"])[0]))
	copy["code"] = "duplicate_standard"
	policy["standards"] = append(list(policy["standards"]), copy)
	response := apiRequest(t, f.a, f.p, "PUT", "/api/position-pools/config/", Object{"version": cfg["version"], "policy": policy})
	if response.Code != 400 || !strings.Contains(response.Body.String(), "多个评估标准") {
		t.Fatalf("ambiguous mapping accepted: %s", response.Body.String())
	}
	responseObject(t, apiRequest(t, f.a, f.p, "PUT", "/api/position-pools/config/", Object{"version": 0, "policy": policy}), 409)
}

func installTestPoolPolicy(t *testing.T, a *App, job Object) {
	t.Helper()
	policy, err := a.poolPolicy(context.Background(), a.Pool)
	if err != nil {
		t.Fatal(err)
	}
	value := obj(policy["policy"])
	suffix := str(job["id"])
	tag := "tag_" + suffix
	value["tags"] = append(list(value["tags"]), Object{"code": tag, "name": "服务开发", "category": "skill", "description": "原文明确描述开发服务"})
	value["pools"] = append(list(value["pools"]), Object{"code": "pool_" + suffix, "name": "软件工程师职位池", "entity": job["entity"]})
	value["standards"] = append(list(value["standards"]), Object{"code": "standard_" + suffix, "name": "统一投递标准", "entity": job["entity"], "pool_code": "pool_" + suffix, "application_names": []any{job["public_name"]}, "responsibilities": "当前投递要求具备服务开发能力", "required_majors": []any{}, "tag_codes": []any{tag}})
	value["rules"] = append(list(value["rules"]), Object{"job_id": job["id"], "pool_code": "pool_" + suffix, "required_tags": []any{tag}, "preferred_tags": []any{}, "priority": 0})
	if err = a.validatePoolPolicy(context.Background(), a.Pool, value); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Pool.Exec(context.Background(), "UPDATE platform_pool_policy SET policy=$1::jsonb,version=version+1 WHERE singleton", string(canonicalJSON(value, false))); err != nil {
		t.Fatal(err)
	}
	if err = a.syncAllocationConfig(context.Background(), a.Pool); err != nil {
		t.Fatal(err)
	}
}

func poolMemberForTest(t *testing.T, f *pipelineFixture) Object {
	t.Helper()
	m, err := one(context.Background(), f.a.Pool, "SELECT row_to_json(m) FROM platform_pool_memberships m WHERE candidate_id=$1 ORDER BY id DESC LIMIT 1", f.candidate["id"])
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func editTestStandard(t *testing.T, f *pipelineFixture) {
	t.Helper()
	ctx := context.Background()
	cfg, err := f.a.poolPolicy(ctx, f.a.Pool)
	if err != nil {
		t.Fatal(err)
	}
	policy := obj(cfg["policy"])
	policyItem(policy, "standards", "standard_"+str(f.job["id"]))["responsibilities"] = "新的投递评估标准，要求独立完成开发和架构设计"
	responseObject(t, apiRequest(t, f.a, f.p, "PUT", "/api/position-pools/config/", Object{"version": cfg["version"], "policy": policy}), 200)
}

func TestHCZeroAllocatesWithoutRepeatingAnalysis(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	ctx := context.Background()
	if _, err := f.a.save(ctx, f.a.Pool, "core_job", f.job["id"], Object{"headcount": 0}); err != nil {
		t.Fatal(err)
	}
	f.executeJob(t, f.submit(t), ctx)
	if poolMemberForTest(t, f)["status"] != "allocated" || f.analyses.Load() != 1 || f.extractions.Load() != 1 {
		t.Fatal("HC prevented allocation or repeated screening")
	}
}
