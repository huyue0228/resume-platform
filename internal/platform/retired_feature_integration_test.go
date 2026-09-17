package platform

import (
	"context"
	"strings"
	"testing"
)

func TestRetiredSpecialRoutingCannotOverridePoolAllocation(t *testing.T) {
	f := newPipelineFixture(t)
	a, ctx := f.a, context.Background()
	t.Setenv("AGENT_KERNEL_ROLLOUT", "enforced")
	parent := mustSave(t, a, "core_department", Object{"name": "historical-parent-" + token(5), "level": 2})
	target := mustSave(t, a, "core_department", Object{"name": "historical-target-" + token(5), "level": 3, "parent_id": parent["id"]})
	legacy := Object{"ai_special_route_enabled": true, "ai_special_route_threshold": .9,
		"ai_special_route_secondary_department_id": parent["id"], "ai_special_route_tertiary_department_id": target["id"]}
	for key, value := range legacy {
		if err := a.setConfig(ctx, a.Pool, key, value); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for key := range legacy {
			if _, err := a.Pool.Exec(ctx, "DELETE FROM core_config WHERE key=$1", key); err != nil {
				t.Error(err)
			}
		}
	})
	f.resultHook = func(result Object) {
		profile := obj(result["profile"])
		claim := clone(obj(list(profile["claims"])[0]))
		claim["kind"], claim["confidence"] = "agent_experience", .99
		profile["claims"] = append(list(profile["claims"]), claim)
		// 风险继续保留，旧专项配置不能改变按标签和名额分配。
		profile["risks"] = []any{"profile_incomplete"}
	}
	listing := apiRequest(t, a, f.p, "GET", "/api/ai-connection/settings/", nil)
	if listing.Code != 200 || strings.Contains(listing.Body.String(), "ai_special_route_") {
		t.Fatalf("retired settings are exposed: status=%d", listing.Code)
	}
	for key := range legacy {
		response := apiRequest(t, a, f.p, "PATCH", "/api/ai-connection/settings/"+key+"/", Object{"value": legacy[key]})
		if response.Code != 404 {
			t.Fatalf("retired setting remains writable: %s status=%d", key, response.Code)
		}
	}
	run := f.submit(t)
	if err := a.executeRun(ctx, run["id"]); err != nil {
		t.Fatal(err)
	}
	f.finishAllocations(t)
	decision, err := one(ctx, a.Pool, "SELECT row_to_json(d) FROM core_agentdispatchdecision d WHERE processing_run_id=$1", run["id"])
	if err != nil {
		t.Fatal(err)
	}
	m := poolMemberForTest(t, f)
	if decision["recommendation"] != "dispatch" || m["status"] != "allocated" {
		t.Fatal("old routing changed pool admission")
	}
	attempt, err := one(ctx, a.Pool, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE agent_decision_id=$1", decision["id"])
	if err != nil {
		t.Fatal(err)
	}
	if attempt["status"] != "pending_dispatch" {
		t.Fatal("old routing bypassed department dispatch")
	}
	if num(attempt["current_department_id"]) != num(f.job["department_id"]) || truth(decision["special_route_applied"]) || str(attempt["route_code"]) != "" {
		t.Fatal("old routing changed the matched job's department")
	}
	if truth(decision["ai_specialist_match"]) || len(list(decision["ai_specialist_evidence"])) != 0 {
		t.Fatal("platform still derives a special decision from Kernel claims")
	}
}
