package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"resume-platform/internal/contract"
	pdftext "resume-platform/internal/pdftext"
)

func fixtureObject(t *testing.T, name string) Object {
	t.Helper()
	raw, err := contract.Bundle.ReadFile("bundle/" + name + ".example.json")
	if err != nil {
		t.Fatal(err)
	}
	var value Object
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func mustSave(t *testing.T, a *App, table string, values Object) Object {
	t.Helper()
	v, err := a.save(context.Background(), a.Pool, table, nil, values)
	if err != nil {
		t.Fatalf("save %s: %v", table, err)
	}
	return v
}
func adminPrincipal(t *testing.T, a *App) *Principal {
	t.Helper()
	if err := a.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	user, err := one(context.Background(), a.Pool, "SELECT row_to_json(u) FROM accounts_user u WHERE username='012358'")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.userPrincipal(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if !p.has("pipeline.run") {
		t.Fatal("administrator has no pipeline permission")
	}
	return p
}
func apiRequest(t *testing.T, a *App, p *Principal, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(canonicalJSON(body, false))
	}
	request := httptest.NewRequest(method, path, reader)
	if p != nil {
		session, err := a.sessionToken(context.Background(), p.User["id"])
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Token "+session)
	}
	request.Host = "testserver"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.Handler(http.NotFoundHandler()).ServeHTTP(response, request)
	return response
}
func responseObject(t *testing.T, response *httptest.ResponseRecorder, status int) Object {
	t.Helper()
	if response.Code != status {
		t.Fatalf("HTTP %d, wanted %d: %s", response.Code, status, response.Body.String())
	}
	var body Object
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

type pipelineFixture struct {
	a                      *App
	p                      *Principal
	candidate, resume, job Object
	pages                  []string
	extractions, analyses  *atomic.Int64
	extractHook            func(context.Context, pdftext.Options) error
	analyzeHook            func(context.Context, Object) error
	resultHook             func(Object)
}

func newPipelineFixture(t *testing.T) *pipelineFixture {
	return newPipelineFixtureWithApp(t, integrationApp(t))
}

func newPipelineFixtureWithApp(t *testing.T, a *App) *pipelineFixture {
	t.Helper()
	ctx := context.Background()
	p := adminPrincipal(t, a)
	f := &pipelineFixture{a: a, p: p}
	requestFixture := fixtureObject(t, "request")
	textBody := obj(obj(requestFixture["scope"])["resume_text"])
	pages := stringValues(textBody["pages"])
	var extractions, analyses atomic.Int64
	a.Extract = func(ctx context.Context, o pdftext.Options) (pdftext.Result, error) {
		result := pdftext.Result{FileSHA256: o.SHA256, ExtractorVersion: "fixture-poppler/v2"}
		if !o.Inspect {
			extractions.Add(1)
			if f.extractHook != nil {
				if err := f.extractHook(ctx, o); err != nil {
					return result, err
				}
			}
			digest := sha256.Sum256([]byte(strings.Join(pages, "\f")))
			result.Text = &pdftext.Text{FileSHA256: o.SHA256, TextSHA256: hex.EncodeToString(digest[:]), ExtractorVersion: result.ExtractorVersion, Pages: pages, Status: "ready", Warnings: []string{}}
		}
		return result, nil
	}
	caps := fixtureObject(t, "capabilities")
	responseFixture := fixtureObject(t, "response")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agent-Kernel-Token") != "test-kernel-token" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/v2/capabilities" {
			write(w, 200, caps)
			return
		}
		analyses.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if err = contract.Validate("request", raw); err != nil {
			t.Errorf("wire request failed schema: %v", err)
			w.WriteHeader(400)
			return
		}
		var request Object
		json.Unmarshal(raw, &request)
		if f.analyzeHook != nil {
			if err := f.analyzeHook(r.Context(), request); err != nil {
				write(w, 500, Object{"detail": "fixture failure"})
				return
			}
		}
		if obj(request["scope"])["artifact"] != nil {
			t.Error("PDF artifact crossed the Kernel boundary")
		}
		result := clone(responseFixture)
		for _, key := range []string{"task_id", "idempotency_key", "workflow_revision", "pin", "protocol_version"} {
			result[key] = request[key]
		}
		resumeText := obj(obj(request["scope"])["resume_text"])
		obj(result["profile"])["source_text"] = strings.Join(stringValues(resumeText["pages"]), "\f")
		obj(result["manifest"])["resume_checksum"] = resumeText["file_sha256"]
		refs := []string{}
		for _, v := range list(obj(request["scope"])["jobs"]) {
			refs = append(refs, str(obj(v)["ref"]))
		}
		sort.Strings(refs)
		matches := []any{}
		for i, ref := range refs {
			match := clone(obj(list(responseFixture["matches"])[0]))
			match["job_ref"] = ref
			match["rank"] = i + 1
			matches = append(matches, match)
		}
		result["matches"] = matches
		tags := []any{}
		for _, v := range list(obj(request["scope"])["tag_catalog"]) {
			tags = append(tags, Object{"code": obj(v)["code"], "status": "supported", "confidence": .95, "evidence": obj(matches[0])["evidence"]})
		}
		obj(result["profile"])["tags"] = tags
		obj(result["manifest"])["covered_jobs"] = refs
		if f.resultHook != nil {
			f.resultHook(result)
		}
		write(w, 200, result)
	}))
	t.Cleanup(server.Close)
	a.Config.KernelURL = server.URL
	a.Config.KernelToken = "test-kernel-token"
	c := Object{"base_url": "http://model.test/v1", "api_style": "chat_json", "model_name": "test-model"}
	for k, v := range c {
		if err := a.setConfig(ctx, a.Pool, "ai_connection_"+k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.setConfig(ctx, a.Pool, "ai_connection_test_fingerprint", connectionFingerprint(c, "")); err != nil {
		t.Fatal(err)
	}
	suffix := token(5)
	primary := mustSave(t, a, "core_department", Object{"name": "测试一级" + suffix, "level": 1})
	department := mustSave(t, a, "core_department", Object{"name": "测试二级" + suffix, "level": 2, "parent_id": primary["id"]})
	job := mustSave(t, a, "core_job", Object{"public_name": "测试岗位" + suffix, "position_name": "开发" + suffix, "entity": "YLS", "responsibilities": "负责后端服务开发、测试与交付", "headcount": 1, "department_id": department["id"]})
	installTestPoolPolicy(t, a, job)
	candidate := mustSave(t, a, "core_candidate", Object{"name": "候选人" + suffix, "phone": "13800138000", "identity_hash": identity(suffix, "13800138000"), "household_province": "上海", "highest_education": "bachelor"})
	filename := "test-" + suffix + ".pdf"
	if err := os.MkdirAll(filepath.Join(a.Config.MediaRoot, "resumes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Config.MediaRoot, "resumes", filename), []byte("%PDF-"+suffix), 0600); err != nil {
		t.Fatal(err)
	}
	resume := mustSave(t, a, "core_resume", Object{"candidate_id": candidate["id"], "apply_id": suffix, "resume_file": filename, "entity": "YLS", "position_name": job["public_name"]})
	f.candidate, f.resume, f.job = candidate, resume, job
	f.pages, f.extractions, f.analyses = pages, &extractions, &analyses
	return f
}
func TestGoPipelineTextToSavedDecision(t *testing.T) {
	f := newPipelineFixture(t)
	a, p, candidate, resume, job := f.a, f.p, f.candidate, f.resume, f.job
	ctx := context.Background()
	pages, extractions, analyses := f.pages, f.extractions, f.analyses
	reply := apiRequest(t, a, p, "POST", "/api/pipeline/run/", Object{"step": "all", "scope": Object{"candidate_ids": []any{candidate["id"]}}})
	submitted := responseObject(t, reply, 202)
	run := obj(list(submitted["processing_runs"])[0])
	if extractions.Load() != 0 || analyses.Load() != 0 {
		t.Fatal("submission performed extraction or analysis")
	}
	if len(list(run["stages"])) != 8 {
		t.Fatalf("incomplete node plan: %v", run["stages"])
	}
	if err := a.executeRun(ctx, run["id"]); err != nil {
		t.Fatalf("worker: %v", err)
	}
	finished, err := a.get(ctx, a.Pool, "core_processingrun", run["id"])
	if err != nil {
		t.Fatal(err)
	}
	if finished["status"] != "success" || num(finished["completed_count"]) != 1 {
		items, _ := rows(ctx, a.Pool, "SELECT row_to_json(i) FROM (SELECT status,error_code,error_message FROM core_processingrunscopeitem WHERE run_id=$1) i", run["id"])
		t.Fatalf("run did not finish: %v items=%v", brief(finished, "status", "completed_count", "error"), items)
	}
	decision, err := one(ctx, a.Pool, "SELECT row_to_json(d) FROM core_agentdispatchdecision d WHERE processing_run_id=$1", run["id"])
	if err != nil {
		t.Fatal(err)
	}
	if decision["recommendation"] != "dispatch" || num(decision["recommended_job_id"]) != num(job["id"]) {
		t.Fatal("direct admission targeted wrong job")
	}
	profile, err := one(ctx, a.Pool, "SELECT row_to_json(p) FROM core_resumeprofile p WHERE resume_id=$1", resume["id"])
	if err != nil {
		t.Fatal(err)
	}
	if profile["raw_text"] != strings.Join(pages, "\f") {
		t.Fatal("saved profile lost full text")
	}
	member := poolMemberForTest(t, f)
	var attempts, reservations int
	a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_assignmentattempt WHERE workflow_id=$1", member["workflow_id"]).Scan(&attempts)
	a.Pool.QueryRow(ctx, "SELECT COALESCE(sum(used_count),0) FROM core_processingrunjobcapacity WHERE run_id=$1", run["id"]).Scan(&reservations)
	if member["status"] != "allocated" || attempts != 1 || reservations != 1 {
		t.Fatalf("direct admission did not allocate exactly once: member=%v attempts=%d HC=%d", member["status"], attempts, reservations)
	}
	at, err := one(ctx, a.Pool, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE agent_decision_id=$1", decision["id"])
	if err != nil || at["status"] != "pending_dispatch" || at["capacity_reservation_id"] == nil {
		t.Fatalf("missing allocation: %v %v", at, err)
	}
	decision, _ = a.get(ctx, a.Pool, "core_agentdispatchdecision", decision["id"])
	if num(decision["recommended_job_id"]) != num(job["id"]) {
		t.Fatal("allocation targeted wrong demand")
	}

	responseObject(t, apiRequest(t, a, p, "GET", "/api/pipeline/runs/"+str(run["id"])+"/materials/", nil), 200)
	if analyses.Load() != 1 || extractions.Load() != 1 {
		t.Fatalf("unexpected work counts: analyze=%d extract=%d", analyses.Load(), extractions.Load())
	}
}

func (f *pipelineFixture) submit(t *testing.T, ids ...any) Object {
	t.Helper()
	if len(ids) == 0 {
		ids = []any{f.candidate["id"]}
	}
	reply := responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/run/", Object{"step": "all", "scope": Object{"candidate_ids": ids}}), 202)
	return obj(list(reply["processing_runs"])[0])
}
func (f *pipelineFixture) executeJob(t *testing.T, run Object, ctx context.Context) {
	t.Helper()
	owner := token(16)
	job, err := one(ctx, f.a.Pool, "UPDATE platform_go_jobs j SET worker_token=$2,status='running',lease_until=now()+interval '20 seconds' WHERE run_id=$1 RETURNING row_to_json(j)", run["id"], owner)
	if err != nil {
		t.Fatal(err)
	}
	f.a.executeJob(ctx, job, owner)
}
func assertNoDecision(t *testing.T, a *App, run Object) {
	t.Helper()
	var count int
	if err := a.Pool.QueryRow(context.Background(), "SELECT count(*) FROM core_agentdispatchdecision WHERE processing_run_id=$1", run["id"]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("cancelled run persisted a decision")
	}
}
func TestCancelExtractionStopsWorkAndWrites(t *testing.T) {
	f := newPipelineFixture(t)
	entered, stopped := make(chan struct{}), make(chan struct{})
	f.extractHook = func(ctx context.Context, _ pdftext.Options) error {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	run := f.submit(t)
	done := make(chan struct{})
	go func() { defer close(done); f.executeJob(t, run, context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("extraction did not start")
	}
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/runs/"+str(run["id"])+"/cancel/", Object{}), 200)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("extraction ignored cancellation")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled task did not settle")
	}
	current, err := f.a.get(context.Background(), f.a.Pool, "core_processingrun", run["id"])
	if err != nil {
		t.Fatal(err)
	}
	if current["status"] != "cancelled" || num(current["cancelled_count"]) != 1 {
		t.Fatalf("bad cancellation outcome: %v persistence_error=%v", brief(current, "status", "cancelled_count"), f.a.stopRun(context.Background(), current, taskError("agent_cancelled", "cancelled fixture")))
	}
	if f.analyses.Load() != 0 {
		t.Fatal("cancelled extraction started model analysis")
	}
	assertNoDecision(t, f.a, run)
	if err = f.a.summarizeRun(context.Background(), run["id"], true); err == nil {
		t.Fatal("finalization overwrote accepted cancellation")
	}
}
func TestExtractionFailureIsolatedAndRetryReusesText(t *testing.T) {
	f := newPipelineFixture(t)
	// Keep this cache test before department assignment; assigned work must not be rerun.
	f.resultHook = func(result Object) { obj(result["profile"])["tags"] = []any{} }
	ctx := context.Background()
	f.analyzeHook = func(context.Context, Object) error { return errors.New("fixture model unavailable") }
	run := f.submit(t)
	f.executeJob(t, run, ctx)
	current, _ := f.a.get(ctx, f.a.Pool, "core_processingrun", run["id"])
	if current["status"] != "needs_attention" && current["status"] != "partial_failed" {
		t.Fatalf("unexpected failure outcome: %v", brief(current, "status", "error"))
	}
	f.analyzeHook = nil
	decision, err := one(ctx, f.a.Pool, "SELECT row_to_json(d) FROM core_agentdispatchdecision d WHERE processing_run_id=$1", run["id"])
	if err != nil {
		t.Fatal(err)
	}
	retry := responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/agent-decisions/"+str(decision["id"])+"/retry/", Object{}), 202)
	f.executeJob(t, obj(retry["run"]), ctx)
	current, _ = f.a.get(ctx, f.a.Pool, "core_processingrun", obj(retry["run"])["id"])
	if current["status"] != "success" {
		t.Fatalf("retry failed: %v", brief(current, "status", "error"))
	}
	if f.extractions.Load() != 1 || f.analyses.Load() != 2 {
		t.Fatalf("retry failed cache behavior: extract=%d analyze=%d", f.extractions.Load(), f.analyses.Load())
	}
	// New bytes under the same path invalidate the cached extraction.
	if err = os.WriteFile(filepath.Join(f.a.Config.MediaRoot, "resumes", str(f.resume["resume_file"])), []byte("%PDF-replacement"+token(8)), 0600); err != nil {
		t.Fatal(err)
	}
	response := responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/pipeline/run/", Object{"step": "step2", "scope": Object{"candidate_ids": []any{f.candidate["id"]}, "force_reprocess": true}}), 202)
	f.executeJob(t, obj(list(response["processing_runs"])[0]), ctx)
	if f.extractions.Load() != 2 {
		t.Fatal("replaced PDF reused previous extraction")
	}
}
func TestMissingPDFFailureDoesNotBlockAnotherCandidate(t *testing.T) {
	f := newPipelineFixture(t)
	suffix := token(6)
	c := mustSave(t, f.a, "core_candidate", Object{"name": suffix, "phone": "13800000000", "identity_hash": identity(suffix, "13800000000"), "household_province": "上海", "highest_education": "bachelor"})
	mustSave(t, f.a, "core_resume", Object{"candidate_id": c["id"], "apply_id": suffix, "resume_file": "missing-" + suffix + ".pdf", "entity": "YLS", "position_name": f.job["public_name"]})
	run := f.submit(t, f.candidate["id"], c["id"])
	f.executeJob(t, run, context.Background())
	result, err := f.a.get(context.Background(), f.a.Pool, "core_processingrun", run["id"])
	if err != nil {
		t.Fatal(err)
	}
	if num(result["completed_count"]) != 1 || num(result["needs_attention_count"])+num(result["failed_count"]) != 1 {
		t.Fatalf("per-resume failure was not isolated: %v", brief(result, "status", "completed_count", "failed_count", "needs_attention_count"))
	}
	if f.analyses.Load() != 1 {
		t.Fatal("missing PDF reached analysis")
	}
}
