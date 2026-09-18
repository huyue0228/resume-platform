package platform

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCandidateSummaryFiltersMatchFullProjection(t *testing.T) {
	f := newPipelineFixture(t)
	a, p, ctx := f.a, f.p, context.Background()
	f.resultHook = func(result Object) { obj(result["profile"])["tags"] = []any{} }
	f.executeJob(t, f.submit(t), ctx)
	primary := mustSave(t, a, "core_department", Object{"name": "筛选一级", "level": 1})
	dep := mustSave(t, a, "core_department", Object{"name": "筛选二级", "level": 2, "parent_id": primary["id"]})
	job := mustSave(t, a, "core_job", Object{"public_name": "机械研发", "department_id": dep["id"]})
	tag := mustSave(t, a, "core_schooltag", Object{"code": "filter_tag", "name": "筛选院校"})
	for i, status := range []string{"pending", "waiting_next", "talent_pool", "passed", "archived", "in_progress"} {
		c := mustSave(t, a, "core_candidate", Object{"name": "张三" + str(i), "identity_hash": token(32), "highest_major": "机械工程", "first_degree_tag_id": tag["id"]})
		r := mustSave(t, a, "core_resume", Object{"candidate_id": c["id"], "apply_id": "first-" + str(i), "position_name": "机械研发", "job_id": job["id"], "entity": "YLS", "job_category": "技术", "volunteer_rank": 1, "apply_date": "2026-09-01"})
		mustSave(t, a, "core_resume", Object{"candidate_id": c["id"], "apply_id": "second-" + str(i), "position_name": "软件研发", "entity": "GW", "volunteer_rank": 2, "apply_date": "2026-09-02"})
		wf := mustSave(t, a, "core_candidateworkflow", Object{"candidate_id": c["id"], "current_resume_id": r["id"], "status": status, "started_at": now()})
		if status == "waiting_next" {
			if _, err := a.Pool.Exec(ctx, `INSERT INTO platform_applications(resume_id,candidate_id,status,reason) VALUES($1,$2,'department_rejected','test')`, r["id"], c["id"]); err != nil {
				t.Fatal(err)
			}
		}
		if i > 1 {
			mustSave(t, a, "core_assignmentattempt", Object{"workflow_id": wf["id"], "resume_id": r["id"], "attempt_no": 1, "status": []string{"passed", "rejected", "dispatched", "pending_dispatch"}[i-2], "current_department_id": dep["id"], "initial_department_id": dep["id"], "source": "manual", "feedback_reason_code": "other"})
		}
	}
	queries := []string{"system_status=pending_allocation", "name=no-match", "pool_status=admitted", "pool_status=pending_allocation", "pool_status=allocated", "pool_status=needs_reanalysis", "pool_status=none"}
	run, err := one(ctx, a.Pool, `SELECT row_to_json(r) FROM core_processingrun r ORDER BY id DESC LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{"completed", "success", "dispatch", "review", "archive", "failed", "skipped"} {
		queries = append(queries, "processing_run_id="+str(run["id"])+"&processing_result="+result)
	}
	for _, query := range []string{
		"name=zhang", "name=zs", "search=ruanjian", "search=second-1", "highest_major_in=机械工程", "current_entity_in=GW", "current_position_name_in=机械研发", "current_rank_in=2", "job_department_name_in=筛选二级", "school_tag_in=筛选院校", "school_tag=filter_tag", "current_job_category_in=技术", "current_apply_date_from=2026-09-02", "current_apply_date_to=2026-09-01", "allocation_source=manual", "reason_type=waiting_next", "feedback_reason_code=other", "system_status=raw", "system_status=talent_pool", "system_status=pending_dispatch", "system_status=screening_passed", "system_status=screening_rejected", "system_status=pending_screening", "workflow_status=waiting_next", "attempt_status=rejected", "current_department_id=" + str(dep["id"]), "current_primary_department_id=" + str(primary["id"]),
	} {
		queries = append(queries, query)
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/candidates/?"+query+"&page_size=2", nil)
			full, err := a.filtered(ctx, "candidates", req, p)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			if err := a.readResource(response, req, "candidates", nil, p); err != nil {
				t.Fatal(err)
			}
			page := responseObject(t, response, 200)
			if num(page["count"]) != int64(len(full)) {
				t.Fatalf("count differs: got %v want %d", page["count"], len(full))
			}
			for i, v := range list(page["results"]) {
				for _, key := range []string{"id", "current_resume", "system_status", "workflow_status", "school_tags", "job_department_name", "current_department_id", "current_primary_department_id", "reason_type", "reason_code", "allocation_source"} {
					if !reflect.DeepEqual(obj(v)[key], full[i][key]) {
						t.Fatalf("%s differs: got %v want %v", key, obj(v)[key], full[i][key])
					}
				}
			}
		})
	}
}

func TestCandidateDepartmentSummaryCannotMatchHiddenApplications(t *testing.T) {
	a, ctx := isolatedInboxApp(t, false), context.Background()
	admin := adminPrincipal(t, a)
	dep := mustSave(t, a, "core_department", Object{"name": "可见部门", "level": 1})
	other := mustSave(t, a, "core_department", Object{"name": "隐藏部门", "level": 1})
	employee := token(8)
	grant := createGrant(t, a, admin, employee, dep, "tertiary", false)
	p := grantPrincipal(t, a, employee)
	at := inboxAttempt(t, a, admin, dep)
	responseObject(t, apiRequest(t, a, admin, "POST", "/api/workflow-attempts/"+str(at["id"])+"/transfer/", Object{"target_screener_id": grant["id"]}), 200)
	if _, err := a.save(ctx, a.Pool, "core_assignmentattempt", at["id"], Object{"status": "rejected"}); err != nil {
		t.Fatal(err)
	}
	wf, _ := a.get(ctx, a.Pool, "core_candidateworkflow", at["workflow_id"])
	hidden := mustSave(t, a, "core_resume", Object{"candidate_id": wf["candidate_id"], "apply_id": "HIDDEN-APPLICATION", "position_name": "隐藏投递岗位"})
	mustSave(t, a, "core_assignmentattempt", Object{"workflow_id": wf["id"], "resume_id": hidden["id"], "attempt_no": 2, "status": "dispatched", "current_department_id": other["id"], "initial_department_id": other["id"]})
	if _, err := a.save(ctx, a.Pool, "core_candidateworkflow", wf["id"], Object{"current_resume_id": hidden["id"]}); err != nil {
		t.Fatal(err)
	}
	for query, count := range map[string]int{"": 1, "search=HIDDEN-APPLICATION": 0, "current_position_name_in=隐藏投递岗位": 0, "current_department_id=" + str(other["id"]): 0, "current_department_id=" + str(dep["id"]): 1} {
		req := httptest.NewRequest("GET", "/api/candidates/?"+query, nil)
		response := httptest.NewRecorder()
		if err := a.readResource(response, req, "candidates", nil, p); err != nil {
			t.Fatal(err)
		}
		page := responseObject(t, response, 200)
		if num(page["count"]) != int64(count) || strings.Contains(response.Body.String(), "HIDDEN-APPLICATION") {
			t.Fatalf("department filter leaked hidden application: %s", query)
		}
		for _, v := range list(page["results"]) {
			if obj(v)["phone"] != "" || num(obj(obj(v)["current_resume"])["id"]) != num(at["resume_id"]) {
				t.Fatal("department projection changed")
			}
		}
	}
	response := httptest.NewRecorder()
	if err := a.filterOptions(response, httptest.NewRequest("GET", "/api/candidates/filter-options/", nil), "candidates", p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Body.String(), "隐藏") {
		t.Fatal("filter options exposed hidden department or application")
	}
}

func TestCandidateFilteringQueriesDoNotGrowPerCandidate(t *testing.T) {
	a := isolatedInboxApp(t, false)
	p, ctx := adminPrincipal(t, a), context.Background()
	for i := 0; i < 1000; i++ {
		c := mustSave(t, a, "core_candidate", Object{"name": fmt.Sprintf("性能候选人%04d", i), "identity_hash": token(32)})
		mustSave(t, a, "core_resume", Object{"candidate_id": c["id"], "apply_id": fmt.Sprintf("PERF%04d", i), "position_name": "测试研发", "entity": "YLS"})
	}
	req := httptest.NewRequest("GET", "/api/candidates/?system_status=raw&page_size=10", nil)
	before, start := a.Pool.Stat().AcquireCount(), time.Now()
	legacy, err := a.filtered(ctx, "candidates", req, p)
	if err != nil {
		t.Fatal(err)
	}
	legacyTime, legacyQueries := time.Since(start), a.Pool.Stat().AcquireCount()-before
	before, start = a.Pool.Stat().AcquireCount(), time.Now()
	response := httptest.NewRecorder()
	if err := a.readResource(response, req, "candidates", nil, p); err != nil {
		t.Fatal(err)
	}
	fastTime, fastQueries := time.Since(start), a.Pool.Stat().AcquireCount()-before
	page := responseObject(t, response, 200)
	if num(page["count"]) != int64(len(legacy)) || len(list(page["results"])) != 10 {
		t.Fatal("pagination changed filtered scope")
	}
	if fastQueries > 200 || fastQueries*10 >= legacyQueries {
		t.Fatalf("per-candidate queries remain: before=%d after=%d", legacyQueries, fastQueries)
	}
	t.Logf("1000 candidates, 10/page: full filtering %s / %d queries; page filtering %s / %d queries", legacyTime, legacyQueries, fastTime, fastQueries)
	before, start = a.Pool.Stat().AcquireCount(), time.Now()
	response = httptest.NewRecorder()
	if err := a.filterOptions(response, req, "candidates", p); err != nil {
		t.Fatal(err)
	}
	options := responseObject(t, response, 200)
	queries := a.Pool.Stat().AcquireCount() - before
	if queries > 15 || len(list(options["current_position_name"])) != 1 {
		t.Fatalf("options expanded full records: %d queries", queries)
	}
	t.Logf("filter options: %s / %d queries", time.Since(start), queries)
	// The final page uses the same ordering and total as exports/bulk scopes.
	u, _ := url.Parse(req.URL.String())
	q := u.Query()
	q.Set("page", "last")
	u.RawQuery = q.Encode()
	req.URL = u
	response = httptest.NewRecorder()
	if err := a.readResource(response, req, "candidates", nil, p); err != nil {
		t.Fatal(err)
	}
	last := responseObject(t, response, 200)
	if last["next"] != nil || num(obj(list(last["results"])[9])["id"]) != num(legacy[len(legacy)-1]["id"]) {
		t.Fatal("last page differs")
	}
}
