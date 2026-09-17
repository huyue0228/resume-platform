package platform

import (
	"context"
	"testing"
)

func TestJobConfigurationInitializesChecksAndRepairsFullPipeline(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	f.independentAllocation = true
	ctx := context.Background()
	if _, err := f.a.Pool.Exec(ctx, "UPDATE platform_pool_policy SET policy=$1::jsonb,version=version+1 WHERE singleton", string(canonicalJSON(emptyPoolPolicy(), false))); err != nil {
		t.Fatal(err)
	}
	path := "/api/position-pools/config/"
	config := responseObject(t, apiRequest(t, f.a, f.p, "POST", path, Object{}), 200)
	policy := obj(config["policy"])
	s := obj(list(policy["standards"])[0])
	if s["responsibilities"] != f.job["responsibilities"] || len(list(policy["tags"])) != 0 {
		t.Fatal("source requirements lost or tags invented", s)
	}
	second := responseObject(t, apiRequest(t, f.a, f.p, "POST", path, Object{}), 200)
	if second["version"] != config["version"] {
		t.Fatal("initialization changed identical config")
	}
	check := func() Object {
		return responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/config-check/", Object{"scope": Object{"candidate_ids": []any{f.candidate["id"]}}}), 200)
	}
	if c := check(); num(c["ready_count"]) != 1 || num(c["allocation_wait_count"]) != 1 {
		t.Fatal("screening and allocation readiness conflated", c)
	}
	policy["tags"] = []any{Object{"code": "service", "name": "服务开发", "category": "skill", "description": "原文包含开发服务的项目证据", "active": true}}
	s["tag_codes"] = []any{"service"}
	rule := obj(list(policy["rules"])[0])
	rule["required_tags"], rule["active"] = []any{"service"}, true
	preview := responseObject(t, apiRequest(t, f.a, f.p, "PUT", path, Object{"version": config["version"], "policy": policy, "preview": true}), 200)
	if num(preview["reassessment_count"]) != 0 {
		t.Fatal(preview)
	}
	current, err := f.a.poolPolicy(ctx, f.a.Pool)
	if err != nil || current["version"] != config["version"] {
		t.Fatal("preview wrote configuration", err)
	}
	responseObject(t, apiRequest(t, f.a, f.p, "PUT", path, Object{"version": config["version"], "policy": policy}), 200)
	if c := check(); len(list(c["issues"])) != 0 {
		t.Fatal(c)
	}
	f.analyzeHook = func(_ context.Context, request Object) error {
		if obj(request["scope"])["taxonomy"] != nil {
			t.Error("retired dictionary transmitted")
		}
		return nil
	}
	f.executeJob(t, f.submit(t), ctx)
	if poolMemberForTest(t, f)["status"] != "pending_allocation" {
		t.Fatal("screening did not persist qualification before allocation")
	}
	responseObject(t, apiRequest(t, f.a, f.p, "PATCH", "/api/jobs/"+str(f.job["id"])+"/", Object{"responsibilities": "更新后的服务开发与交付要求", "headcount": 0}), 200)
	if poolMemberForTest(t, f)["status"] != "needs_reanalysis" {
		t.Fatal("source job change left stale qualification allocatable")
	}
	repair := responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/position-pools/config/reprocess/", Object{}), 202)
	run, err := f.a.get(ctx, f.a.Pool, "core_processingrun", repair["run_id"])
	if err != nil {
		t.Fatal(err)
	}
	f.executeJob(t, run, ctx)
	f.finishAllocations(t)
	if poolMemberForTest(t, f)["status"] != "allocated" || f.analyses.Load() != 2 || f.extractions.Load() != 1 {
		t.Fatalf("repair did not complete: state=%v model=%d extraction=%d", poolMemberForTest(t, f)["status"], f.analyses.Load(), f.extractions.Load())
	}
	var audit int
	if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM platform_pool_config_events WHERE actor_id=$1", f.p.User["id"]).Scan(&audit); err != nil || audit != 3 {
		t.Fatal("configuration audit missing", audit, err)
	}
}

func TestJobConfigurationConflictAndRetiredEndpoints(t *testing.T) {
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	ctx := context.Background()
	if _, err := f.a.Pool.Exec(ctx, "UPDATE platform_pool_policy SET policy=$1::jsonb WHERE singleton", string(canonicalJSON(emptyPoolPolicy(), false))); err != nil {
		t.Fatal(err)
	}
	job := Object{"department": f.job["department_id"], "entity": f.job["entity"], "position_name": f.job["position_name"], "public_name": f.job["public_name"], "responsibilities": "不同的岗位要求"}
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/jobs/", job), 201)
	c := responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/config-check/", Object{"scope": Object{"candidate_ids": []any{f.candidate["id"]}}}), 200)
	if num(c["blocked_count"]) != 1 || obj(list(c["issues"])[0])["code"] != "source_job_conflict" {
		t.Fatal(c)
	}
	for _, path := range []string{"/api/major-categories/", "/api/major-aliases/", "/api/configs/job_hc_coefficient/"} {
		responseObject(t, apiRequest(t, f.a, f.p, "GET", path, nil), 404)
	}
	config := responseObject(t, apiRequest(t, f.a, f.p, "GET", "/api/position-pools/config/", nil), 200)
	// One unresolved posting must not prevent saving progress on the rest of the configuration.
	config = responseObject(t, apiRequest(t, f.a, f.p, "PUT", "/api/position-pools/config/", Object{"version": config["version"], "policy": config["policy"]}), 200)
	policy := obj(config["policy"])
	obj(list(policy["standards"])[0])["source_job_id"] = f.job["id"]
	responseObject(t, apiRequest(t, f.a, f.p, "PUT", "/api/position-pools/config/", Object{"version": config["version"], "policy": policy}), 200)
	c = responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/config-check/", Object{"scope": Object{"candidate_ids": []any{f.candidate["id"]}}}), 200)
	if num(c["blocked_count"]) != 0 || num(c["ready_count"]) != 1 {
		t.Fatal("explicit source did not resolve conflict", c)
	}
}
