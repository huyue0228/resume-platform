package extractor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}
func TestCanonicalPageBoundaries(t *testing.T) {
	raw := strings.Repeat("中文 English 双栏表格文字\r\n", 8) + "\f\f末页\r保留尾部换行\r\n\f"
	text, err := canonical([]byte(raw), nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Pages) != 3 || text.Pages[1] != "" || text.Pages[2] != "末页\n保留尾部换行\n" {
		t.Fatalf("page boundaries changed: %#v", text.Pages)
	}
	joined := strings.Join(text.Pages, "\f")
	digest := sha256.Sum256([]byte(joined))
	if text.TextSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("checksum differs from full canonical text")
	}
	if text.Status != "ready" {
		t.Fatalf("ordinary text classified as incomplete: %v", text.Warnings)
	}
}
func TestCanonicalScanAndMixedPages(t *testing.T) {
	for _, tc := range []struct {
		name, body, images string
		pages              int
		status             string
	}{{"scan", "\f", "1 0 image 1024 2048 rgb", 1, "needs_attention"}, {"mixed", strings.Repeat("完整文本", 40) + "\f扫描页标题\f", "2 0 image 1024 2048 rgb", 2, "needs_attention"}, {"blank", strings.Repeat("完整文本", 40) + "\f\f", "", 2, "ready"}, {"logo", strings.Repeat("完整文本", 40) + "\f", "1 0 image 100 100 rgb", 1, "ready"}} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := canonical([]byte(tc.body), []byte(tc.images), tc.pages)
			if err != nil {
				t.Fatal(err)
			}
			if text.Status != tc.status {
				t.Fatalf("wanted %s, got %s", tc.status, text.Status)
			}
		})
	}
}
func TestCanonicalRejectsLossyInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   []byte
		pages int
		code  string
	}{{"oversize", []byte(strings.Repeat("a", MaxTextBytes+1)), 1, "resume_text_too_large"}, {"page mismatch", []byte("page\f"), 2, "pdf_damaged"}, {"nul", []byte("text\x00"), 1, "pdf_damaged"}, {"invalid UTF8", []byte{0xff}, 1, "pdf_damaged"}} {
		t.Run(tc.name, func(t *testing.T) { _, err := canonical(tc.raw, nil, tc.pages); requireCode(t, err, tc.code) })
	}
}
func TestSnapshotHashAndReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.pdf")
	raw := []byte("%PDF test input")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	options := Options{Path: path, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(raw)), Pages: 100}
	calls := []string{}
	fake := func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "pdfinfo":
			return []byte("Pages: 1\nEncrypted: no\n"), nil, nil
		case "pdftotext":
			if args[0] == "-v" {
				return nil, []byte("pdftotext version 24.12.0"), nil
			}
			snapshot, err := os.ReadFile(args[len(args)-2])
			if err != nil || string(snapshot) != string(raw) {
				t.Fatal("extractor did not read the checked snapshot")
			}
			return []byte(strings.Repeat("read all text and preserve spacing  ", 8) + "\f"), nil, nil
		case "pdfimages":
			return nil, nil, nil
		}
		t.Fatal("unexpected external tool")
		return nil, nil, nil
	}
	result, err := run(context.Background(), options, fake)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExtractorVersion != "poppler-layout-go/v2.1:24.12.0" || result.Text.FileSHA256 != options.SHA256 {
		t.Fatal("provenance missing")
	}
	if len(calls) != 4 {
		t.Fatalf("unexpected tools: %v", calls)
	}
	if err = os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = run(context.Background(), options, fake)
	requireCode(t, err, "pdf_changed")
}
func TestCancelledExtractionNeverStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := run(ctx, Options{Pages: 100}, func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("cancelled extraction started a process")
		return nil, nil, nil
	})
	requireCode(t, err, "agent_cancelled")
}
func TestCommandTimeoutStopsProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := command(ctx, "sleep", "10")
	requireCode(t, err, "text_extraction_timeout")
	if time.Since(start) > 2*time.Second {
		t.Fatal("timed out process was not stopped")
	}
}
func TestEncryptedPDFRejected(t *testing.T) {
	raw := []byte("test encrypted fixture")
	hash := sha256.Sum256(raw)
	path := filepath.Join(t.TempDir(), "encrypted.pdf")
	os.WriteFile(path, raw, 0600)
	_, err := run(context.Background(), Options{Path: path, Size: int64(len(raw)), SHA256: hex.EncodeToString(hash[:]), Pages: 100}, func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "pdftotext" && args[0] == "-v" {
			return nil, []byte("pdftotext version 24.12.0"), nil
		}
		if name == "pdfinfo" {
			return []byte("Pages: 1\nEncrypted: yes\n"), nil, nil
		}
		t.Fatal("encrypted PDF reached text extraction")
		return nil, nil, nil
	})
	requireCode(t, err, "pdf_encrypted")
}

func TestPopplerDiagnosticCannotSilentlyLosePartOfText(t *testing.T) {
	raw := []byte("fixture PDF")
	digest := sha256.Sum256(raw)
	path := filepath.Join(t.TempDir(), "mixed-cmap.pdf")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := run(context.Background(), Options{Path: path, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(raw)), Pages: 100}, func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		switch name {
		case "pdftotext":
			if args[0] == "-v" {
				return nil, []byte("pdftotext version 24.12.0"), nil
			}
			return []byte(strings.Repeat("Readable English text but Chinese text is missing. ", 4) + "\f"), []byte("Syntax Error: Missing language pack for 'Adobe-GB1' mapping"), nil
		case "pdfinfo":
			return []byte("Pages: 1\nEncrypted: no\n"), nil, nil
		case "pdfimages":
			return nil, nil, nil
		}
		t.Fatal("unexpected tool")
		return nil, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text.Status != "needs_attention" || len(result.Text.Warnings) == 0 {
		t.Fatal("partial text was treated as complete despite Poppler errors")
	}
	if !strings.Contains(result.Text.Pages[0], "Readable English") {
		t.Fatal("diagnostic handling discarded the available full text")
	}
}
