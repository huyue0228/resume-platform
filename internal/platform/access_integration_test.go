package platform

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBulkAndDrilldownPreserveSelectedScope(t *testing.T) {
	f := newPipelineFixture(t)
	ctx := context.Background()
	run := f.submit(t)
	f.executeJob(t, run, ctx)
	for _, filters := range []Object{{"unknown": "value"}, {"system_status": ""}, {"current_entity_in": []any{Object{"x": 1}}}} {
		responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/candidates/bulk-dispatch/", Object{"candidate_filters": filters}), 400)
	}
	values, _ := json.Marshal([]any{f.job["id"]})
	q := url.Values{"analytics_dimension": {"job"}, "analytics_values": {string(values)}}
	result := responseObject(t, apiRequest(t, f.a, f.p, "GET", "/api/candidates/?"+q.Encode(), nil), 200)
	if num(result["count"]) != 1 || num(obj(list(result["results"])[0])["id"]) != num(f.candidate["id"]) {
		t.Fatal("dashboard drilldown broadened selected candidates")
	}
	q.Set("analytics_values", "[999999999]")
	result = responseObject(t, apiRequest(t, f.a, f.p, "GET", "/api/candidates/?"+q.Encode(), nil), 200)
	if num(result["count"]) != 0 {
		t.Fatal("missing dashboard key matched candidates")
	}
	responseObject(t, apiRequest(t, f.a, f.p, "GET", "/api/candidates/?analytics_dimension=invalid", nil), 400)
	responseObject(t, apiRequest(t, f.a, f.p, "GET", "/api/candidates/?system_status=invalid", nil), 400)
	responseObject(t, apiRequest(t, f.a, f.p, "GET", "/api/candidates/?current_apply_date_from=2026-99-01", nil), 400)
}
func TestDepartmentAccessDoesNotLeakOtherCandidatesOrDecisions(t *testing.T) {
	f := newPipelineFixture(t)
	ctx := context.Background()
	f.executeJob(t, f.submit(t), ctx)
	at, err := one(ctx, f.a.Pool, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE resume_id=$1 ORDER BY id DESC LIMIT 1", f.resume["id"])
	if err != nil {
		t.Fatal(err)
	}
	department, _ := f.a.get(ctx, f.a.Pool, "core_department", f.job["department_id"])
	contact := mustSave(t, f.a, "core_contact", Object{"name": "接口人", "employee_no": token(8), "email": token(8) + "@example.test", "department_id": department["id"], "contact_level": "secondary", "can_delegate": true, "is_active": true})
	user := mustSave(t, f.a, "accounts_user", Object{"username": contact["employee_no"], "email": contact["email"], "contact_id": contact["id"], "password": "!unusable", "is_active": true, "date_joined": now()})
	principal := &Principal{User: user, Contact: contact, Department: department, Permissions: map[string]bool{"attempt.view_department": true, "attempt.feedback": true, "attempt.export": true}, Roles: []string{}}
	for code := range principal.Permissions {
		_, err = f.a.Pool.Exec(ctx, "INSERT INTO accounts_user_user_permissions(user_id,permission_id) SELECT $1,id FROM auth_permission WHERE codename=$2 ON CONFLICT DO NOTHING", user["id"], strings.ReplaceAll(code, ".", "__"))
		if err != nil {
			t.Fatal(err)
		}
	}
	responseObject(t, apiRequest(t, f.a, principal, "GET", "/api/candidates/"+str(f.candidate["id"])+"/", nil), 404)
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/workflow-attempts/"+str(at["id"])+"/dispatch/", Object{}), 200)
	result := responseObject(t, apiRequest(t, f.a, principal, "GET", "/api/candidates/"+str(f.candidate["id"])+"/", nil), 200)
	if result["phone"] != "" {
		t.Fatal("department candidate list revealed phone")
	}
	visible := obj(result["current_attempt"])
	if visible["agent_decision"] != nil || visible["agent_decision_summary"] != nil {
		t.Fatal("department response revealed private Agent decision")
	}
	responseObject(t, apiRequest(t, f.a, principal, "GET", "/api/candidates/?analytics_dimension=candidate", nil), 400)
	responseObject(t, apiRequest(t, f.a, principal, "GET", "/api/agent-decisions/", nil), 403)
	responseObject(t, apiRequest(t, f.a, principal, "GET", "/api/position-pools/members/", nil), 403)
	other := mustSave(t, f.a, "core_candidate", Object{"name": token(8), "phone": "13811111111", "identity_hash": token(32)})
	responseObject(t, apiRequest(t, f.a, principal, "GET", "/api/candidates/"+str(other["id"])+"/", nil), 404)
	// A contact of another department cannot submit feedback even with the same role.
	foreign := mustSave(t, f.a, "core_department", Object{"name": token(8), "level": 2, "parent_id": department["parent_id"]})
	if _, err = f.a.save(ctx, f.a.Pool, "core_contact", contact["id"], Object{"department_id": foreign["id"]}); err != nil {
		t.Fatal(err)
	}
	responseObject(t, apiRequest(t, f.a, principal, "POST", "/api/workflow-attempts/"+str(at["id"])+"/feedback/", Object{"result": "passed"}), 404)
}
func TestAllowedHostEnforced(t *testing.T) {
	a := unitApp(t)
	t.Setenv("DJANGO_ALLOWED_HOSTS", "resume.test,.example.test")
	// Reject before touching DB or authentication.
	r := httptest.NewRequest("GET", "http://untrusted.test/api/me/", nil)
	w := httptest.NewRecorder()
	a.Handler(nil).ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("untrusted host got %d", w.Code)
	}
}
