package platform

import (
	"context"
	"testing"
)

func grantPrincipal(t *testing.T, a *App, employee string) *Principal {
	t.Helper()
	user, err := one(context.Background(), a.Pool, "SELECT row_to_json(u) FROM accounts_user u WHERE username=$1", employee)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.userPrincipal(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func createGrant(t *testing.T, a *App, admin *Principal, employee string, department Object, role string, delegate bool) Object {
	t.Helper()
	return responseObject(t, apiRequest(t, a, admin, "POST", "/api/contacts/", Object{"employee_no": employee, "name": "人员" + employee, "email": employee + "@example.test", "department": department["id"], "contact_level": role, "can_delegate": delegate, "is_active": true}), 201)
}

func inboxAttempt(t *testing.T, a *App, admin *Principal, department Object) Object {
	t.Helper()
	suffix := token(8)
	c := mustSave(t, a, "core_candidate", Object{"name": "候选人" + suffix, "phone": "13900000000", "identity_hash": identity(suffix, "13900000000")})
	r := mustSave(t, a, "core_resume", Object{"candidate_id": c["id"], "apply_id": suffix, "entity": "YLS", "position_name": "开发"})
	at, err := a.manualAssign(context.Background(), r["id"], department["id"], "部门收件箱测试", admin)
	if err != nil {
		t.Fatal(err)
	}
	responseObject(t, apiRequest(t, a, admin, "POST", "/api/workflow-attempts/"+str(at["id"])+"/dispatch/", Object{}), 200)
	at, err = a.get(context.Background(), a.Pool, "core_assignmentattempt", at["id"])
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestSameEmployeeHasIndependentDepartmentGrants(t *testing.T) {
	a := integrationApp(t)
	admin := adminPrincipal(t, a)
	ctx := context.Background()
	root := mustSave(t, a, "core_department", Object{"name": token(8), "level": 1})
	depA := mustSave(t, a, "core_department", Object{"name": token(8), "level": 2, "parent_id": root["id"]})
	depB := mustSave(t, a, "core_department", Object{"name": token(8), "level": 2, "parent_id": root["id"]})
	employee := token(8)
	grantA := createGrant(t, a, admin, employee, depA, "secondary", true)
	grantB := createGrant(t, a, admin, employee, depB, "tertiary", true)
	p := grantPrincipal(t, a, employee)
	if len(p.Grants) != 2 || len(p.Roles) != 2 {
		t.Fatal("one account did not retain both grants")
	}
	atA := inboxAttempt(t, a, admin, depA)
	atB := inboxAttempt(t, a, admin, depB)
	if !a.visibleAttempt(ctx, p, atA) || a.visibleAttempt(ctx, p, atB) {
		t.Fatal("unassigned screener can read department mailbox")
	}
	responseObject(t, apiRequest(t, a, admin, "POST", "/api/workflow-attempts/"+str(atB["id"])+"/transfer/", Object{"target_screener_id": grantB["id"]}), 200)
	view := responseObject(t, apiRequest(t, a, p, "GET", "/api/workflow-attempts/"+str(atB["id"])+"/", nil), 200)
	if truth(view["can_transfer"]) || !truth(view["can_feedback"]) || !truth(view["can_export"]) {
		t.Fatal("permissions leaked between department grants")
	}
	responseObject(t, apiRequest(t, a, p, "POST", "/api/workflow-attempts/"+str(atB["id"])+"/transfer/", Object{"target_department_id": depA["id"]}), 403)
	responseObject(t, apiRequest(t, a, admin, "PATCH", "/api/contacts/"+str(grantA["id"])+"/", Object{"is_active": false}), 200)
	responseObject(t, apiRequest(t, a, p, "GET", "/api/workflow-attempts/"+str(atA["id"])+"/", nil), 404)
	responseObject(t, apiRequest(t, a, p, "GET", "/api/workflow-attempts/"+str(atB["id"])+"/", nil), 200)
	deleted := apiRequest(t, a, admin, "DELETE", "/api/contacts/"+str(grantA["id"])+"/", nil)
	if deleted.Code != 204 {
		t.Fatalf("revoke grant: %d %s", deleted.Code, deleted.Body.String())
	}
	p = grantPrincipal(t, a, employee)
	if !truth(p.User["is_active"]) || len(p.Grants) != 1 || num(p.Grants[0].Contact["id"]) != num(grantB["id"]) {
		t.Fatal("revoking one grant damaged the account or another grant")
	}
	responseObject(t, apiRequest(t, a, p, "POST", "/api/workflow-attempts/"+str(atB["id"])+"/feedback/", Object{"result": "passed"}), 200)
}

func TestScreenerReassignmentKeepsDepartmentAndRevokesPreviousAccess(t *testing.T) {
	a := integrationApp(t)
	admin := adminPrincipal(t, a)
	dep := mustSave(t, a, "core_department", Object{"name": token(8), "level": 1})
	employee := token(8)
	createGrant(t, a, admin, employee, dep, "secondary", true)
	contact := grantPrincipal(t, a, employee)
	firstEmployee, secondEmployee := token(8), token(8)
	first := createGrant(t, a, admin, firstEmployee, dep, "tertiary", false)
	second := createGrant(t, a, admin, secondEmployee, dep, "tertiary", false)
	firstP, secondP := grantPrincipal(t, a, firstEmployee), grantPrincipal(t, a, secondEmployee)
	at := inboxAttempt(t, a, admin, dep)
	path := "/api/workflow-attempts/" + str(at["id"]) + "/"
	options := responseObject(t, apiRequest(t, a, contact, "GET", path+"transfer-options/", nil), 200)
	if len(list(options["screeners"])) != 2 {
		t.Fatal("department screener options missing")
	}
	responseObject(t, apiRequest(t, a, contact, "POST", path+"transfer/", Object{"target_screener_id": first["id"]}), 200)
	responseObject(t, apiRequest(t, a, contact, "POST", path+"feedback/", Object{"result": "passed"}), 403)
	responseObject(t, apiRequest(t, a, secondP, "GET", path, nil), 404)
	responseObject(t, apiRequest(t, a, firstP, "GET", path, nil), 200)
	view := responseObject(t, apiRequest(t, a, contact, "POST", path+"transfer/", Object{"target_screener_id": second["id"]}), 200)
	if num(view["current_department"]) != num(dep["id"]) || num(view["assigned_screener"]) != num(second["id"]) {
		t.Fatal("person delegation changed the receiving mailbox")
	}
	responseObject(t, apiRequest(t, a, firstP, "POST", path+"feedback/", Object{"result": "passed"}), 404)
	responseObject(t, apiRequest(t, a, secondP, "POST", path+"feedback/", Object{"result": "passed"}), 200)
}

func TestContactImportMergesEmployeeDepartmentAndRole(t *testing.T) {
	a := integrationApp(t)
	admin := adminPrincipal(t, a)
	employee := token(8)
	base := Object{"姓名": "兼任人员", "工号": employee, "邮箱": employee + "@example.test", "一层部门": token(8), "二层部门": "部门A", "角色": "接口人", "可转派": "是"}
	second := clone(base)
	second["二层部门"] = "部门B"
	second["角色"] = "简历筛选人"
	third := clone(base)
	third["角色"] = "简历筛选人"
	for i := 0; i < 2; i++ {
		responseObject(t, importTableFixture(t, a, admin, "contacts", "incremental", []Object{base, second, third}), 200)
	}
	p := grantPrincipal(t, a, employee)
	if len(p.Grants) != 3 {
		t.Fatal("reimport overwrote another department or duplicated a grant")
	}
	responseObject(t, importTableFixture(t, a, admin, "contacts", "incremental", []Object{base, base}), 400)
	conflict := clone(second)
	conflict["姓名"] = "不同人员"
	responseObject(t, importTableFixture(t, a, admin, "contacts", "incremental", []Object{base, conflict}), 400)
	if len(grantPrincipal(t, a, employee).Grants) != 3 {
		t.Fatal("invalid import partially committed")
	}
}

func TestPrimaryHRHasGlobalBusinessAccessAndSecondaryHRStaysScoped(t *testing.T) {
	a := isolatedInboxApp(t, false)
	admin := adminPrincipal(t, a)
	ctx := context.Background()
	group, err := one(ctx, a.Pool, "SELECT row_to_json(g) FROM auth_group g WHERE name='一级部门HR'")
	if err != nil {
		t.Fatal(err)
	}
	employee := token(8)
	u := responseObject(t, apiRequest(t, a, admin, "POST", "/api/users/", Object{"username": employee, "email": employee + "@example.test", "role": "primary_hr", "role_ids": []any{group["id"]}, "is_active": true}), 201)
	global := grantPrincipal(t, a, employee)
	for _, code := range []string{"resume.view", "resume.import", "pipeline.run", "attempt.view_all", "attempt.dispatch", "department.manage"} {
		if !global.has(code) {
			t.Fatalf("primary HR is missing business permission %s", code)
		}
	}
	for _, code := range []string{"settings.manage_config", "settings.manage_permissions", "settings.manage_ai_connection"} {
		if global.has(code) {
			t.Fatalf("nonadministrator has system permission %s", code)
		}
	}
	me := responseObject(t, apiRequest(t, a, global, "GET", "/api/me/", nil), 200)
	if obj(me["data_scope"])["type"] != "all" {
		t.Fatal("primary HR is not global")
	}
	responseObject(t, apiRequest(t, a, global, "GET", "/api/ai-connection/settings/", nil), 403)
	root := mustSave(t, a, "core_department", Object{"name": token(8), "level": 1})
	depA := mustSave(t, a, "core_department", Object{"name": token(8), "level": 2, "parent_id": root["id"]})
	depB := mustSave(t, a, "core_department", Object{"name": token(8), "level": 2, "parent_id": root["id"]})
	// A separately assigned global role survives changes to a department grant.
	g := createGrant(t, a, admin, employee, depA, "secondary", true)
	if num(grantPrincipal(t, a, employee).User["id"]) != num(u["id"]) || !grantPrincipal(t, a, employee).has("resume.import") {
		t.Fatal("department sync removed the global HR role")
	}
	if w := apiRequest(t, a, admin, "DELETE", "/api/contacts/"+str(g["id"])+"/", nil); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if !grantPrincipal(t, a, employee).has("resume.import") {
		t.Fatal("revoking a department grant revoked the independent HR role")
	}
	scopedEmployee := token(8)
	createGrant(t, a, admin, scopedEmployee, depA, "secondary_hr", true)
	scoped := grantPrincipal(t, a, scopedEmployee)
	atA, atB := inboxAttempt(t, a, admin, depA), inboxAttempt(t, a, admin, depB)
	if !a.visibleAttempt(ctx, scoped, atA) || a.visibleAttempt(ctx, scoped, atB) || scoped.has("resume.import") || scoped.has("pipeline.run") || scoped.has("attempt.view_all") {
		t.Fatal("secondary HR gained global business capabilities")
	}
	createGrant(t, a, admin, scopedEmployee, depB, "secondary", true)
	scoped = grantPrincipal(t, a, scopedEmployee)
	if !a.canManageAttempt(ctx, a.Pool, scoped, atA, "attempt.dispatch") || a.canManageAttempt(ctx, a.Pool, scoped, atB, "attempt.dispatch") {
		t.Fatal("HR permission leaked to another department's contact grant")
	}
	role, err := one(ctx, a.Pool, "SELECT row_to_json(g) FROM auth_group g WHERE name='二级部门HR'")
	if err != nil {
		t.Fatal(err)
	}
	responseObject(t, apiRequest(t, a, admin, "PATCH", "/api/roles/"+str(role["id"])+"/", Object{"permission_codes": []any{"resume.import", "attempt.view_all"}}), 400)
	responseObject(t, apiRequest(t, a, admin, "PATCH", "/api/roles/"+str(group["id"])+"/", Object{"permission_codes": []any{"settings.manage_config"}}), 400)
}

func TestSecondaryHRCanReviewEvidenceForOwnDepartment(t *testing.T) {
	f := newPipelineFixture(t)
	ctx := context.Background()
	dep, err := f.a.get(ctx, f.a.Pool, "core_department", f.job["department_id"])
	if err != nil {
		t.Fatal(err)
	}
	employee := token(8)
	createGrant(t, f.a, f.p, employee, dep, "secondary_hr", true)
	hr := grantPrincipal(t, f.a, employee)
	f.executeJob(t, f.submit(t), ctx)
	view := responseObject(t, apiRequest(t, f.a, hr, "GET", "/api/candidates/"+str(f.candidate["id"])+"/", nil), 200)
	at := obj(view["current_attempt"])
	if at["status"] != "pending_review" || !truth(at["can_dispatch"]) || obj(at["agent_decision_summary"])["id"] == nil {
		t.Fatal("department HR cannot inspect evidence before review")
	}
	responseObject(t, apiRequest(t, f.a, hr, "POST", "/api/workflow-attempts/"+str(at["id"])+"/confirm-review/", Object{}), 200)
	responseObject(t, apiRequest(t, f.a, hr, "POST", "/api/workflow-attempts/"+str(at["id"])+"/dispatch/", Object{}), 200)
}
