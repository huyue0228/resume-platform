package platform

import (
	"bytes"
	"context"
	"encoding/csv"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func importTableFixture(t *testing.T, a *App, p *Principal, key, mode string, records []Object) *httptest.ResponseRecorder {
	t.Helper()
	var data bytes.Buffer
	writer := csv.NewWriter(&data)
	headers := a.Spec.ImportSchemas[key].Headers
	writer.Write(headers)
	for _, r := range records {
		values := []string{}
		for _, h := range headers {
			values = append(values, str(r[h]))
		}
		writer.Write(values)
	}
	writer.Flush()
	return importFileFixture(t, a, p, key, mode, "fixture.csv", data.Bytes())
}

func importFileFixture(t *testing.T, a *App, p *Principal, key, mode, filename string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("mode", mode)
	file, err := form.CreateFormFile(key, filename)
	if err != nil {
		t.Fatal(err)
	}
	file.Write(raw)
	form.Close()
	req := httptest.NewRequest("POST", "/api/import/", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	session, err := a.sessionToken(context.Background(), p.User["id"])
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Token "+session)
	w := httptest.NewRecorder()
	a.Handler(http.NotFoundHandler()).ServeHTTP(w, req)
	return w
}
func TestGoImportExportJobsAndContacts(t *testing.T) {
	a := integrationApp(t)
	p := adminPrincipal(t, a)
	ctx := context.Background()
	suffix := token(6)
	record := Object{"招聘主体": "YLS", "一层部门": "一级" + suffix, "二层部门": "二级" + suffix, "对外发布名称": "外部" + suffix, "职位名称": "开发" + suffix, "岗位类别": "研发", "工作职责": "设计 Go 后台服务", "需求专业": "计算机、软件工程", "HC": "2.0", "是否对外发布": "是"}
	result := responseObject(t, importTableFixture(t, a, p, "jobs", "incremental", []Object{record}), 200)
	if num(obj(result["counts"])["jobs"]) != 1 {
		t.Fatal("job import count")
	}
	job, err := one(ctx, a.Pool, "SELECT row_to_json(j) FROM core_job j WHERE public_name=$1", record["对外发布名称"])
	if err != nil {
		t.Fatal(err)
	}
	if num(job["headcount"]) != 2 {
		t.Fatal("Excel numeric HC changed")
	}
	record["HC"] = "3"
	responseObject(t, importTableFixture(t, a, p, "jobs", "incremental", []Object{record}), 200)
	var count int
	a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_job WHERE public_name=$1", record["对外发布名称"]).Scan(&count)
	if count != 1 {
		t.Fatal("incremental job import duplicated business key")
	}
	export := apiRequest(t, a, p, "GET", "/api/jobs/export/?ids="+str(job["id"]), nil)
	if export.Code != 200 {
		t.Fatalf("export status=%d %s", export.Code, export.Body.String())
	}
	book, err := excelize.OpenReader(bytes.NewReader(export.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	rows, err := book.GetRows(book.GetSheetName(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || strings.Join(rows[0], "|") != strings.Join(a.Spec.ImportSchemas["jobs"].Headers, "|") {
		t.Fatal("export does not match existing job template")
	}
	contact := Object{"姓名": "接口人" + suffix, "工号": "00" + suffix, "邮箱": suffix + "@example.test", "一层部门": record["一层部门"], "二层部门": record["二层部门"], "可转派": "是"}
	schema := a.Spec.ImportSchemas["contacts"]
	t.Logf("contact template has %d fields", len(schema.Headers))
	responseObject(t, importTableFixture(t, a, p, "contacts", "incremental", []Object{contact}), 200)
	user, err := one(ctx, a.Pool, "SELECT row_to_json(u) FROM accounts_user u WHERE username=$1", contact["工号"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(str(user["password"]), "!") || user["contact_id"] == nil {
		t.Fatal("contact account was not bound with unusable password")
	}
	principal, err := a.userPrincipal(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.has("attempt.view_department") || principal.has("resume.view") {
		t.Fatal("contact role scope changed")
	}
	responseObject(t, apiRequest(t, a, p, "PATCH", "/api/users/"+str(user["id"])+"/", Object{"password": "must-reject"}), 400)
	responseObject(t, apiRequest(t, a, p, "PATCH", "/api/jobs/"+str(job["id"])+"/", Object{"department": float64(num(job["department_id"])) + .5}), 400)
	responseObject(t, apiRequest(t, a, p, "PATCH", "/api/jobs/"+str(job["id"])+"/", Object{"headcount": 1.5}), 400)
}
func TestCSVWindowsEncodingAndStandardHeaders(t *testing.T) {
	a := unitApp(t)
	data := "学校;院校标签\n测试大学;双一流\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	records, err := a.readTable(encoded, "schools.csv", "schools")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0]["学校"] != "测试大学" {
		t.Fatal("Windows CSV lost Chinese text")
	}
	if _, err = a.readTable([]byte("学校,院校标签,未知\n测试,一本,x\n"), "schools.csv", "schools"); err == nil {
		t.Fatal("unknown headers silently accepted")
	}
}
