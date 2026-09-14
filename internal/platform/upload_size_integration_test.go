package platform

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

type uploadZeros struct{}

func (uploadZeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestXLSXExpandedBeyondFormerSizeLimit(t *testing.T) {
	a := unitApp(t)
	book := excelize.NewFile()
	defer book.Close()
	if err := book.SetSheetRow("Sheet1", "A1", &[]string{"学校", "院校标签"}); err != nil {
		t.Fatal(err)
	}
	if err := book.SetSheetRow("Sheet1", "A2", &[]string{"大文件测试大学", "双一流"}); err != nil {
		t.Fatal(err)
	}
	raw, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	original, err := zip.NewReader(bytes.NewReader(raw.Bytes()), int64(raw.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var expanded bytes.Buffer
	writer := zip.NewWriter(&expanded)
	for _, entry := range original.File {
		if entry.Name != "xl/worksheets/sheet1.xml" {
			if err := writer.Copy(entry); err != nil {
				t.Fatal(err)
			}
			continue
		}
		source, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		dest, err := writer.Create(entry.Name)
		if err != nil {
			source.Close()
			t.Fatal(err)
		}
		_, err = io.Copy(dest, source)
		source.Close()
		if err != nil {
			t.Fatal(err)
		}
		// Valid trailing XML comments expand beyond 128 MiB without extra rows or cells.
		comment := []byte("<!--" + strings.Repeat(" ", 1017) + "-->")
		for n := 0; n < (129<<20)/len(comment); n++ {
			if _, err := dest.Write(comment); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	records, err := a.readTable(expanded.Bytes(), "schools.xlsx", "schools")
	if err != nil || len(records) != 1 || records[0]["学校"] != "大文件测试大学" {
		t.Fatalf("large XLSX was rejected or lost cell values: %v", err)
	}
}

func TestUploadBeyondFormerFileAndArchiveLimits(t *testing.T) {
	a := integrationApp(t)
	p := adminPrincipal(t, a)
	ctx := context.Background()
	suffix := token(8)
	candidate := mustSave(t, a, "core_candidate", Object{"name": suffix, "identity_hash": identity(suffix, "")})
	resume := mustSave(t, a, "core_resume", Object{"candidate_id": candidate["id"], "apply_id": suffix, "entity": "YLS"})
	filename := "large(" + suffix + ").pdf"
	const size = 513 << 20 // Exceeds the former 32, 128, 256 and 512 MiB limits.

	// Build the request on disk, using a fixed buffer instead of a huge byte slice.
	body, err := os.CreateTemp(t.TempDir(), "upload-*")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("resume_package", "large.zip")
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(part)
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: filename, Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	if _, err = io.CopyN(io.MultiWriter(entry, digest), uploadZeros{}, size); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	requestSize, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/import/", body)
	req.ContentLength = requestSize
	req.Header.Set("Content-Type", form.FormDataContentType())
	session, err := a.sessionToken(ctx, p.User["id"])
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Token "+session)
	w := httptest.NewRecorder()
	a.Handler(http.NotFoundHandler()).ServeHTTP(w, req)
	result := responseObject(t, w, http.StatusAccepted)
	if result["agent_processing"] != "submitted" || len(list(result["processing_runs"])) != 1 {
		t.Fatal("large PDF was not submitted for processing")
	}
	stored, err := a.get(ctx, a.Pool, "core_resume", resume["id"])
	if err != nil || !strings.HasSuffix(str(stored["resume_file"]), "-"+filename) {
		t.Fatalf("large upload was not committed: %v", err)
	}
	checksum, savedSize, err := fileDigest(ctx, filepath.Join(a.Config.MediaRoot, "resumes", str(stored["resume_file"])))
	if err != nil || savedSize != size || checksum != hex.EncodeToString(digest.Sum(nil)) {
		t.Fatalf("large PDF was rejected, truncated or changed: size=%d err=%v", savedSize, err)
	}
	for _, headers := range req.MultipartForm.File {
		for _, header := range headers {
			if f, err := header.Open(); err == nil {
				f.Close()
				t.Fatal("multipart temporary file leaked after import")
			}
		}
	}
	t.Logf("HTTP 202; uploaded %d bytes; stored and SHA-256 verified %d-byte PDF", requestSize, savedSize)
}

func TestCorruptPackagePreservesOriginalAndRollsBack(t *testing.T) {
	a := integrationApp(t)
	p := adminPrincipal(t, a)
	ctx := context.Background()
	suffix := token(8)
	filename := "original(" + suffix + ").pdf"
	candidate := mustSave(t, a, "core_candidate", Object{"name": suffix, "identity_hash": identity(suffix, "")})
	resume := mustSave(t, a, "core_resume", Object{"candidate_id": candidate["id"], "apply_id": suffix, "resume_file": filename})
	dest := filepath.Join(a.Config.MediaRoot, "resumes")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte("original PDF must survive")
	if err := os.WriteFile(filepath.Join(dest, filename), old, 0600); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	payload := []byte("replacement data with a deliberately wrong checksum")
	for _, name := range []string{filename, "corrupt(" + suffix + ").pdf"} {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := raw.Bytes()
	index := bytes.LastIndex(data, payload)
	if index < 0 {
		t.Fatal("cannot locate fixture payload")
	}
	data[index] ^= 1
	responseObject(t, importFileFixture(t, a, p, "resume_package", "incremental", "corrupt.zip", data), http.StatusBadRequest)
	stored, err := a.get(ctx, a.Pool, "core_resume", resume["id"])
	if err != nil || stored["resume_file"] != filename {
		t.Fatalf("failed import changed database: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dest, filename))
	if err != nil || !bytes.Equal(contents, old) {
		t.Fatalf("failed import damaged original file: %v", err)
	}
	entries, err := os.ReadDir(a.Config.MediaRoot)
	if err != nil || len(entries) != 1 || entries[0].Name() != "resumes" {
		t.Fatalf("failed import leaked staging directory: %v", err)
	}
}
