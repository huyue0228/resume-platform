package platform

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestImportJobsWithoutSecondaryDepartment(t *testing.T) {
	a := integrationApp(t)
	p := adminPrincipal(t, a)
	ctx := context.Background()
	suffix := token(6)
	base := Object{"招聘主体": "YLS", "一层部门": "直属中心" + suffix, "二层部门": "", "对外发布名称": "直属岗位" + suffix, "职位名称": "开发" + suffix, "岗位类别": "研发", "工作职责": "开发服务", "需求专业": "计算机、软件工程", "HC": "2"}
	other := clone(base)
	other["一层部门"] = "另一中心" + suffix
	secondary := clone(base)
	secondary["二层部门"] = "研发部" + suffix
	records := []Object{base, other, secondary}
	result := responseObject(t, importTableFixture(t, a, p, "jobs", "incremental", records), 200)
	if num(obj(result["counts"])["jobs"]) != 3 {
		t.Fatal("primary-only and secondary jobs were not all imported")
	}
	jobs, err := rows(ctx, a.Pool, "SELECT row_to_json(j) FROM core_job j WHERE public_name=$1 ORDER BY id", base["对外发布名称"])
	if err != nil || len(jobs) != 3 {
		t.Fatalf("imported jobs: count=%d error=%v", len(jobs), err)
	}
	ids := []string{}
	for i, job := range jobs {
		ids = append(ids, str(job["id"]))
		view := responseObject(t, apiRequest(t, a, p, "GET", "/api/jobs/"+str(job["id"])+"/", nil), 200)
		if view["primary_department_name"] != records[i]["一层部门"] || view["secondary_department_name"] != records[i]["二层部门"] || len(list(view["majors"])) != 2 {
			t.Fatalf("department names or majors changed: %v", view)
		}
		dep, err := a.get(ctx, a.Pool, "core_department", job["department_id"])
		if err != nil || (i < 2 && (num(dep["level"]) != 1 || dep["parent_id"] != nil)) {
			t.Fatalf("primary-only job got a synthetic secondary department: %v, %v", dep, err)
		}
		responseObject(t, apiRequest(t, a, p, "PATCH", "/api/jobs/"+str(job["id"])+"/", Object{"headcount": 3}), 200)
		records[i]["HC"] = "4"
	}
	responseObject(t, importTableFixture(t, a, p, "jobs", "incremental", records), 200)
	updated, err := rows(ctx, a.Pool, "SELECT row_to_json(j) FROM core_job j WHERE public_name=$1 ORDER BY id", base["对外发布名称"])
	if err != nil || len(updated) != 3 {
		t.Fatalf("reimport duplicated jobs: %v", err)
	}
	for i, job := range updated {
		if num(job["id"]) != num(jobs[i]["id"]) || num(job["headcount"]) != 4 {
			t.Fatal("reimport failed to update the existing business key")
		}
	}
	export := apiRequest(t, a, p, "GET", "/api/jobs/export/?ids="+strings.Join(ids, ","), nil)
	if export.Code != 200 {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	exported, err := a.readTable(export.Body.Bytes(), "jobs.xlsx", "jobs")
	if err != nil || len(exported) != 3 {
		t.Fatalf("export cannot be reimported: %v", err)
	}
	blankSecondary := 0
	for _, row := range exported {
		if row["二层部门"] == "" {
			blankSecondary++
			if row["一层部门"] == "" {
				t.Fatal("export lost primary ownership")
			}
		}
	}
	if blankSecondary != 2 {
		t.Fatal("export fabricated a secondary department")
	}
	responseObject(t, importTableFixture(t, a, p, "jobs", "incremental", exported), 200)
	responseObject(t, importTableFixture(t, a, p, "jobs", "incremental", []Object{base, base}), 400)
	missing := clone(base)
	missing["一层部门"] = ""
	responseObject(t, importTableFixture(t, a, p, "jobs", "incremental", []Object{missing}), 400)
}

func TestJobPoolAcceptsPrimaryAndSecondaryDepartments(t *testing.T) {
	for _, level := range []int{0, 1, 2, 3} {
		s := Object{"volunteers": []any{Object{"ref": "volunteer", "entity": "YLS", "position_name": "岗位"}}, "jobs": []any{Object{"ref": "job", "entity": "YLS", "public_name": "岗位", "position_name": "开发", "department_ref": "department", "department_level": level, "responsibilities": "开发服务"}}}
		want := "job_pool_empty"
		if level == 1 || level == 2 {
			want = "ready"
		}
		if got := prepareSnapshot(s)["status"]; got != want {
			t.Fatalf("department level=%d: got=%v want=%s", level, got, want)
		}
	}
}

func TestImportTemplatesExplainOptionalDepartmentLevels(t *testing.T) {
	a := unitApp(t)
	for _, key := range []string{"jobs", "contacts"} {
		w := httptest.NewRecorder()
		if err := a.importTemplate(w, httptest.NewRequest("GET", "/api/import/templates/"+key+"/", nil), key); err != nil {
			t.Fatal(err)
		}
		book, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		instructions, err := book.GetRows("填写说明")
		book.Close()
		if err != nil || !strings.Contains(str(instructions), "没有二层部门") || (key == "contacts" && !strings.Contains(str(instructions), "只有被指定的筛选人")) {
			t.Fatalf("template %s does not explain the new rules: %v", key, err)
		}
		if key == "contacts" && (!strings.Contains(str(instructions), "自动合并") || !strings.Contains(str(instructions), "冲突行号")) {
			t.Fatal("contact template does not explain duplicate-grant merging and conflicts")
		}
	}
}
