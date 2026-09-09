package platform

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDockerTextPipelineWithTLS(t *testing.T) {
	base, dbURL := os.Getenv("TEST_DOCKER_PLATFORM_URL"), os.Getenv("TEST_DOCKER_DATABASE_URL")
	if base == "" || dbURL == "" {
		t.Skip("isolated Docker endpoints not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	a, err := New(ctx, Config{DatabaseURL: dbURL, RedisURL: "redis://127.0.0.1:56379/2", Secret: "verification-only-platform-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	p := adminPrincipal(t, a)
	session, err := a.sessionToken(ctx, p.User["id"])
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, status int) Object {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(canonicalJSON(body, false))
		}
		req, _ := http.NewRequestWithContext(ctx, method, base+path, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Token "+session)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		var value Object
		if json.Unmarshal(raw, &value) != nil || response.StatusCode != status {
			t.Fatalf("%s %s: status=%d body=%s", method, path, response.StatusCode, string(raw))
		}
		return value
	}
	call("GET", "/api/me/", nil, 200)
	// 保存及 TEST 都经过容器 API；TLS 验证必须由平台与 Kernel 各自完成。
	call("PATCH", "/api/ai-connection/", Object{"api_style": "chat_json", "base_url": "https://host.docker.internal:56443/v1", "model_name": "tls-fixture", "api_key": "verification-only-model-key"}, 200)
	tested := call("POST", "/api/ai-connection/test/", Object{}, 200)
	if !truth(tested["ok"]) {
		t.Fatalf("container model TLS probe failed: %v", tested)
	}
	suffix := token(6)
	primary := call("POST", "/api/departments/", Object{"name": "联调一级" + suffix, "level": 1, "parent": nil}, 201)
	department := call("POST", "/api/departments/", Object{"name": "联调二级" + suffix, "level": 2, "parent": primary["id"]}, 201)
	job := call("POST", "/api/jobs/", Object{"department": department["id"], "entity": "YLS", "public_name": "岗位" + suffix, "position_name": "后端开发" + suffix, "responsibilities": "开发 Go 服务并完成测试", "headcount": 1}, 201)
	var csvBody, zipBody, body bytes.Buffer
	headers := a.Spec.ImportSchemas["resume_list"].Headers
	record := Object{"招聘主体": "YLS", "姓名": "Docker联调" + suffix, "应聘ID": suffix, "手机号": "13812340000", "对外职位名称": job["public_name"], "学历": "本科", "户口所在地": "上海"}
	writer := csv.NewWriter(&csvBody)
	writer.Write(headers)
	values := []string{}
	for _, h := range headers {
		values = append(values, str(record[h]))
	}
	writer.Write(values)
	writer.Flush()
	pdf, err := os.ReadFile("/tmp/resume-pdf-fixtures/english.pdf")
	if err != nil {
		t.Fatal(err)
	}
	zipper := zip.NewWriter(&zipBody)
	file, err := zipper.Create("Docker(" + suffix + ").pdf")
	if err != nil {
		t.Fatal(err)
	}
	file.Write(pdf)
	zipper.Close()
	form := multipart.NewWriter(&body)
	form.WriteField("mode", "incremental")
	file, _ = form.CreateFormFile("resume_list", "fixture.csv")
	file.Write(csvBody.Bytes())
	file, _ = form.CreateFormFile("resume_package", "fixture.zip")
	file.Write(zipBody.Bytes())
	form.Close()
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/import/", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Token "+session)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var submitted Object
	if json.Unmarshal(raw, &submitted) != nil || response.StatusCode != 202 {
		t.Fatalf("resume library import status=%d body=%s", response.StatusCode, raw)
	}
	resume, err := one(ctx, a.Pool, "SELECT row_to_json(r) FROM core_resume r WHERE apply_id=$1", suffix)
	if err != nil {
		t.Fatal(err)
	}
	candidate := call("GET", "/api/candidates/"+str(resume["candidate_id"])+"/", nil, 200)
	run := obj(list(submitted["processing_runs"])[0])
	if len(list(run["stages"])) != 8 {
		t.Fatal("incomplete submitted node plan")
	}
	for activeRun(str(run["status"])) && ctx.Err() == nil {
		time.Sleep(100 * time.Millisecond)
		run = call("GET", "/api/pipeline/runs/"+str(run["id"])+"/", nil, 200)
	}
	if run["status"] != "success" {
		t.Fatalf("container pipeline failed: %v", brief(run, "id", "status", "error", "message"))
	}
	detail := call("GET", "/api/candidates/"+str(candidate["id"])+"/", nil, 200)
	attempt := obj(detail["current_attempt"])
	if attempt["status"] != "pending_review" {
		t.Fatalf("unexpected business result: %v", attempt["status"])
	}
	decision := call("GET", "/api/agent-decisions/"+str(attempt["agent_decision"])+"/", nil, 200)
	manifest := obj(obj(decision["kernel_result"])["manifest"])
	if manifest["terminal_state"] != "DONE" || num(manifest["ocr_pages"]) != 0 {
		t.Fatal("Kernel did not complete text-only analysis")
	}
	matches := list(obj(decision["kernel_result"])["matches"])
	if len(matches) != 1 || str(obj(matches[0])["job_title"]) != str(job["position_name"]) {
		t.Fatal("frozen job presentation changed")
	}
	profile, err := one(ctx, a.Pool, "SELECT row_to_json(p) FROM core_resumeprofile p WHERE resume_id=$1", resume["id"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(str(profile["raw_text"]), "Designed reliable background processing services.") {
		t.Fatal("full PDF text did not reach saved profile")
	}
	materials := call("GET", "/api/pipeline/runs/"+str(run["id"])+"/materials/", nil, 200)
	item := obj(list(materials["results"])[0])
	text := call("GET", "/api/pipeline/runs/"+str(run["id"])+"/material-text/?item_id="+str(item["id"]), nil, 200)
	if strings.Join(stringValues(text["pages"]), "\f") != profile["raw_text"] {
		t.Fatal("preview text differs from saved analysis text")
	}
	t.Logf("Docker pipeline run=%v: full text, verified evidence, saved result, platform and Kernel TLS succeeded", run["id"])
}
