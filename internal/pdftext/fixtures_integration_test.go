package extractor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealPopplerCorpus(t *testing.T) {
	root := os.Getenv("TEST_PDF_FIXTURE_DIR")
	if root == "" {
		t.Skip("TEST_PDF_FIXTURE_DIR not configured")
	}
	for _, tc := range []struct {
		name, status, code string
		pages              int
		contains           string
	}{
		{"english", "ready", "", 1, "Alex Example"}, {"chinese", "ready", "", 1, "张测试"}, {"columns", "ready", "", 1, "Column B: 6 complete evidence"}, {"table", "ready", "", 1, "Page and line references"}, {"blank-middle", "ready", "", 3, "张测试"}, {"scan", "needs_attention", "", 1, ""}, {"mixed", "needs_attention", "", 2, "Alex Example"}, {"blank-only", "needs_attention", "", 1, ""}, {"encrypted", "", "pdf_encrypted", 0, ""}, {"damaged", "", "pdf_damaged", 0, ""}, {"oversize", "", "pdf_too_large", 0, ""}, {"overtext", "", "resume_text_too_large", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name+".pdf")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result, err := Run(ctx, Options{Path: path, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(raw)), Pages: 100})
			if tc.code != "" {
				var f *Failure
				if !errors.As(err, &f) || f.Code != tc.code {
					t.Fatalf("expected %s got %v", tc.code, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			text := result.Text
			if text.Status != tc.status || len(text.Pages) != tc.pages || !strings.Contains(strings.Join(text.Pages, "\f"), tc.contains) {
				t.Fatalf("unexpected extraction: status=%s pages=%d required=%q warnings=%v", text.Status, len(text.Pages), tc.contains, text.Warnings)
			}
			if tc.name == "blank-middle" && text.Pages[1] != "" {
				t.Fatal("blank page position changed")
			}
			// 和旧路径相同的 pdftotext -layout 原始结果对照，全文不摘要、不截断。
			old, err := exec.CommandContext(ctx, "pdftotext", "-layout", "-enc", "UTF-8", path, "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			old = bytes.ReplaceAll(old, []byte("\r\n"), []byte("\n"))
			old = bytes.ReplaceAll(old, []byte("\r"), []byte("\n"))
			old = bytes.TrimSuffix(old, []byte("\f"))
			if string(old) != strings.Join(text.Pages, "\f") {
				t.Fatal("new extraction differs from full layout text")
			}
		})
	}
}
