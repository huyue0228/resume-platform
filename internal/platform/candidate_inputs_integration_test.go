package platform

import (
	"context"
	"testing"
)

func TestCandidateEditDuringAnalysisMustDiscardOldResult(t *testing.T) {
	f := newPipelineFixture(t)
	f.independentAllocation = true
	ctx := context.Background()
	f.resultHook = lowAssessment
	f.analyzeHook = func(_ context.Context, request Object) error {
		t.Logf("model input education=%v", obj(obj(request["scope"])["candidate"])["highest_education"])
		responseObject(t, apiRequest(t, f.a, f.p, "PATCH", "/api/candidates/"+str(f.candidate["id"])+"/", Object{"highest_education": "master"}), 200)
		return nil
	}
	run := f.submit(t)
	f.executeJob(t, run, ctx)
	candidate, err := f.a.get(ctx, f.a.Pool, "core_candidate", f.candidate["id"])
	if err != nil {
		t.Fatal(err)
	}
	application, err := one(ctx, f.a.Pool, "SELECT row_to_json(a) FROM platform_applications a WHERE resume_id=$1", f.resume["id"])
	if err != nil {
		t.Fatal(err)
	}
	var decisions int
	if err := f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_agentdispatchdecision WHERE processing_run_id=$1", run["id"]).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	t.Logf("current education=%v; application=%v; saved decisions=%d", candidate["highest_education"], application["status"], decisions)
	if decisions != 0 {
		t.Fatal("stale model result persisted after candidate API edit")
	}
	assertApplicationState(t, f, f.resume, "blocked")
	f.analyzeHook = nil
	f.resultHook = nil
	f.executeJob(t, f.submit(t), ctx)
	if poolMemberForTest(t, f)["status"] != "pending_allocation" {
		t.Fatal("edited candidate could not be reassessed")
	}
}

func TestSchoolCorrectionMustInvalidateAdmission(t *testing.T) {
	f := newPipelineFixture(t)
	f.independentAllocation = true
	ctx := context.Background()
	tag := mustSave(t, f.a, "core_schooltag", Object{"code": "AUDIT_TARGET", "name": "Audit Target", "is_active": true})
	school := mustSave(t, f.a, "core_school", Object{"name": "Audit Eligible University", "province": "上海", "school_tag_id": tag["id"]})
	rule := mustSave(t, f.a, "core_schooltagrule", Object{"name": "Audit Admission", "priority": -1, "is_active": true})
	for _, degree := range []string{"first", "highest"} {
		mustSave(t, f.a, "core_schooltagruletag", Object{"rule_id": rule["id"], "school_tag_id": tag["id"], "degree_type": degree})
	}
	responseObject(t, apiRequest(t, f.a, f.p, "PATCH", "/api/candidates/"+str(f.candidate["id"])+"/", Object{"first_degree_school": school["name"], "highest_degree_school": school["name"]}), 200)
	f.executeJob(t, f.submit(t), ctx)
	member := poolMemberForTest(t, f)
	if member["status"] != "pending_allocation" {
		t.Fatalf("setup: %v", member["status"])
	}
	responseObject(t, apiRequest(t, f.a, f.p, "PATCH", "/api/candidates/"+str(f.candidate["id"])+"/", Object{"highest_degree_school": "Audit Noneligible University"}), 200)
	candidate, err := f.a.get(ctx, f.a.Pool, "core_candidate", f.candidate["id"])
	if err != nil {
		t.Fatal(err)
	}
	data, err := f.a.loadPreparationData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := f.a.freezeCase(ctx, Object{}, candidate, Object{}, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fresh preflight=%v", obj(frozen["preflight"])["status"])
	if truth(obj(frozen["preflight"])["admission_passed"]) {
		t.Fatal("setup: changed school unexpectedly admitted")
	}
	member = poolMemberForTest(t, f)
	if member["status"] != "needs_reanalysis" {
		t.Fatalf("school correction retained eligibility: %v", member["status"])
	}
	if _, _, err := f.a.claimAllocation(ctx); !noPoolRecord(err) {
		t.Fatalf("stale qualification was claimed: %v", err)
	}
	assertApplicationState(t, f, f.resume, "needs_reanalysis")
	f.executeJob(t, f.submit(t), ctx)
	if f.analyses.Load() != 1 {
		t.Fatal("ineligible school reached the model")
	}
	workflow, err := one(ctx, f.a.Pool, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1", f.candidate["id"])
	if err != nil || workflow["archive_reason"] != "school_rule_not_matched" {
		t.Fatalf("admission was not rechecked: %v %v", workflow, err)
	}
}

func TestCancelSimulationMustPreserveAutomaticAllocation(t *testing.T) {
	f := newAllocationFixture(t)
	ctx := context.Background()
	member := poolMemberForTest(t, f)
	scope, err := f.a.allocationScope(ctx, f.a.Pool, member, false)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/position-pools/allocation-tasks/"
	task := responseObject(t, apiRequest(t, f.a, f.p, "POST", path, Object{"scope_id": scope["id"], "mode": "simulate", "member_ids": []any{member["id"]}, "idempotency_key": "audit-cancel-simulation"}), 202)
	responseObject(t, apiRequest(t, f.a, f.p, "POST", path+str(task["task_id"])+"/cancel/", Object{}), 202)
	items, err := rows(ctx, f.a.Pool, "SELECT row_to_json(i) FROM platform_allocation_work_items i WHERE member_id=$1", member["id"])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("work after simulation cancellation=%v", items)
	_, task, err = runAllocation(t, f)
	if err != nil || task["mode"] != "execute" || poolMemberForTest(t, f)["status"] != "allocated" {
		t.Fatalf("automatic allocation failed after cancelling simulation: %v", err)
	}
}

func TestFailedSimulationMustPreserveAutomaticAllocation(t *testing.T) {
	f := newAllocationFixture(t)
	ctx := context.Background()
	member := poolMemberForTest(t, f)
	scope, err := f.a.allocationScope(ctx, f.a.Pool, member, false)
	if err != nil {
		t.Fatal(err)
	}
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/position-pools/allocation-tasks/", Object{"scope_id": scope["id"], "mode": "simulate", "member_ids": []any{member["id"]}, "idempotency_key": "audit-fail-simulation"}), 202)
	oldToken := f.a.Config.KernelToken
	f.a.Config.KernelToken = ""
	if _, _, err := runAllocation(t, f); err == nil {
		t.Fatal("setup: expected unavailable Kernel")
	}
	f.a.Config.KernelToken = oldToken
	items, err := rows(ctx, f.a.Pool, "SELECT row_to_json(i) FROM platform_allocation_work_items i WHERE member_id=$1", member["id"])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("work after simulation failure=%v", items)
	_, task, err := runAllocation(t, f)
	if err != nil || task["mode"] != "execute" || poolMemberForTest(t, f)["status"] != "allocated" {
		t.Fatalf("automatic allocation failed after failed simulation: %v", err)
	}
}

func TestCandidateCosmeticEditAndAssignedHistory(t *testing.T) {
	f := newAllocationFixture(t)
	ctx := context.Background()
	member := poolMemberForTest(t, f)
	responseObject(t, apiRequest(t, f.a, f.p, "PATCH", "/api/candidates/"+str(f.candidate["id"])+"/", Object{"gender": "F", "highest_education": "bachelor"}), 200)
	after := poolMemberForTest(t, f)
	if after["status"] != "pending_allocation" || after["revision"] != member["revision"] {
		t.Fatal("cosmetic or unchanged inputs invalidated qualification")
	}
	if _, _, err := runAllocation(t, f); err != nil {
		t.Fatal(err)
	}
	responseObject(t, apiRequest(t, f.a, f.p, "PATCH", "/api/candidates/"+str(f.candidate["id"])+"/", Object{"highest_education": "master", "highest_degree_school": "Corrected School"}), 200)
	if poolMemberForTest(t, f)["status"] != "allocated" {
		t.Fatal("candidate correction changed existing department assignment")
	}
	assertApplicationState(t, f, f.resume, "pending_dispatch")
	var assignments int
	if err := f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_assignmentattempt").Scan(&assignments); err != nil || assignments != 1 {
		t.Fatalf("assignment history changed: count=%d error=%v", assignments, err)
	}
}

func TestImportedCandidateCorrectionInvalidatesQualification(t *testing.T) {
	f := newAllocationFixture(t)
	ctx := context.Background()
	// The pipeline fixture uses a synthetic identity; imports merge by name/phone.
	if _, err := f.a.save(ctx, f.a.Pool, "core_candidate", f.candidate["id"], Object{"identity_hash": identity(str(f.candidate["name"]), str(f.candidate["phone"]))}); err != nil {
		t.Fatal(err)
	}
	responseObject(t, importTableFixture(t, f.a, f.p, "resume_list", "incremental", []Object{{
		"姓名": f.candidate["name"], "手机号": f.candidate["phone"], "应聘ID": f.resume["apply_id"],
		"户口所在地": f.candidate["household_province"], "学历": "硕士", "招聘主体": "YLS", "对外职位名称": f.job["public_name"],
	}}), 202)
	if poolMemberForTest(t, f)["status"] != "needs_reanalysis" {
		t.Fatal("import retained qualification from old candidate inputs")
	}
	assertApplicationState(t, f, f.resume, "needs_reanalysis")
	if _, _, err := f.a.claimAllocation(ctx); !noPoolRecord(err) {
		t.Fatalf("import left stale qualification runnable: %v", err)
	}
}

func TestAllocationRechecksAdmissionInputsAndLegacyQualifications(t *testing.T) {
	for _, field := range []string{"first_degree_school", "highest_degree_school", "household_province", "legacy_snapshot"} {
		t.Run(field, func(t *testing.T) {
			f := newAllocationFixture(t)
			ctx := context.Background()
			member := poolMemberForTest(t, f)
			if field == "legacy_snapshot" {
				_, err := f.a.Pool.Exec(ctx, `UPDATE platform_pool_memberships SET assessment=jsonb_set(assessment,'{candidate}',$2::jsonb) WHERE id=$1`, member["id"], string(canonicalJSON(brief(f.candidate, "highest_major", "highest_education"), false)))
				if err != nil {
					t.Fatal(err)
				}
			} else {
				// Bypass edit invalidation to exercise allocation's independent fence.
				if _, err := f.a.save(ctx, f.a.Pool, "core_candidate", f.candidate["id"], Object{field: "Changed"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := runAllocation(t, f); err != nil {
				t.Fatal(err)
			}
			if poolMemberForTest(t, f)["status"] != "needs_reanalysis" {
				t.Fatal("stale admission inputs allocated")
			}
			assertApplicationState(t, f, f.resume, "needs_reanalysis")
			f.executeJob(t, f.submit(t), ctx)
			if poolMemberForTest(t, f)["status"] != "pending_allocation" {
				t.Fatal("stale qualification could not be reassessed")
			}
		})
	}
}

func TestSuccessfulSimulationDoesNotMutateQualificationOrQueue(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(str(stale), func(t *testing.T) {
			f := newAllocationFixture(t)
			ctx := context.Background()
			member := poolMemberForTest(t, f)
			scope, err := f.a.allocationScope(ctx, f.a.Pool, member, false)
			if err != nil {
				t.Fatal(err)
			}
			if stale {
				if _, err := f.a.save(ctx, f.a.Pool, "core_candidate", f.candidate["id"], Object{"highest_degree_school": "Changed"}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := rows(ctx, f.a.Pool, "SELECT row_to_json(i) FROM platform_allocation_work_items i ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/position-pools/allocation-tasks/", Object{"scope_id": scope["id"], "mode": "simulate", "member_ids": []any{member["id"]}}), 202)
			if _, task, err := runAllocation(t, f); err != nil || task["mode"] != "simulate" {
				t.Fatalf("simulation: %v", err)
			}
			after, err := rows(ctx, f.a.Pool, "SELECT row_to_json(i) FROM platform_allocation_work_items i ORDER BY id")
			if err != nil || fingerprint(before) != fingerprint(after) {
				t.Fatalf("simulation changed execution queue: %v", err)
			}
			if poolMemberForTest(t, f)["status"] != "pending_allocation" {
				t.Fatal("simulation changed qualification")
			}
			if _, _, err := runAllocation(t, f); err != nil {
				t.Fatal(err)
			}
			expected := "allocated"
			if stale {
				expected = "needs_reanalysis"
			}
			if poolMemberForTest(t, f)["status"] != expected {
				t.Fatal("automatic allocation did not handle qualification")
			}
		})
	}
}
