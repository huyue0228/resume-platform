package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pdftext "resume-platform/internal/pdftext"
)

func TestRequestLimitIncludesJSONEscaping(t *testing.T) {
	f := newPipelineFixture(t)
	extract := f.a.Extract
	page := strings.Repeat("\"", 1<<20)
	digest := sha256.Sum256([]byte(page))
	f.a.Extract = func(ctx context.Context, o pdftext.Options) (pdftext.Result, error) {
		result, err := extract(ctx, o)
		if result.Text != nil {
			result.Text.Pages = []string{page}
			result.Text.TextSHA256 = hex.EncodeToString(digest[:])
		}
		return result, err
	}
	run := f.submit(t)
	f.executeJob(t, run, context.Background())
	item, err := one(context.Background(), f.a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1", run["id"])
	if err != nil || item["error_code"] != "request_too_large" {
		t.Fatalf("encoded request limit not enforced: %v %v", brief(item, "status", "error_code"), err)
	}
	if f.analyses.Load() != 0 {
		t.Fatal("oversized request reached Kernel")
	}
	stored, err := f.a.get(context.Background(), f.a.Pool, "core_resumetextextraction", item["text_extraction_id"])
	if err != nil || str(list(obj(stored["payload"])["pages"])[0]) != page {
		t.Fatal("oversized request silently truncated extracted text")
	}
}

func TestModelTLSRejectsUntrustedCertificate(t *testing.T) {
	a := integrationApp(t)
	t.Setenv("AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY", "False")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { write(w, 200, Object{"ok": true}) }))
	defer server.Close()
	if _, _, err := a.modelHTTP(context.Background(), "GET", server.URL, "", nil); err == nil {
		t.Fatal("untrusted model TLS certificate accepted")
	}
	a.HTTP = server.Client()
	if _, status, err := a.modelHTTP(context.Background(), "GET", server.URL, "", nil); err != nil || status != 200 {
		t.Fatalf("trusted model TLS failed: status=%d err=%v", status, err)
	}
}

func TestContainerHealthUsesLoopbackWithProductionHosts(t *testing.T) {
	a := integrationApp(t)
	t.Setenv("DJANGO_ALLOWED_HOSTS", "resume.example.test")
	r := httptest.NewRequest("GET", "http://127.0.0.1/healthz", nil)
	w := httptest.NewRecorder()
	a.Handler(http.NotFoundHandler()).ServeHTTP(w, r)
	responseObject(t, w, 200)
}
