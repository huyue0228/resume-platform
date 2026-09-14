package platform

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Capture the real SQL arguments without starting another database for encoding tests.
type filenameDB struct {
	DB
	applyID, savedName string
	calls              int
}

func (db *filenameDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	db.calls++
	for _, arg := range args {
		if s, ok := arg.(string); ok && (!utf8.ValidString(s) || strings.ContainsRune(s, 0)) {
			return nil, errors.New("invalid byte sequence for PostgreSQL UTF8 text")
		}
	}
	if strings.HasPrefix(sql, "SELECT") {
		if len(args) != 1 || args[0] != db.applyID {
			return nil, fmt.Errorf("application ID was not decoded: %v", args)
		}
	} else if strings.HasPrefix(sql, "UPDATE") {
		match := regexp.MustCompile(`"resume_file"=\$(\d+)`).FindStringSubmatch(sql)
		if len(match) != 2 {
			return nil, fmt.Errorf("unexpected SQL: %s", sql)
		}
		index, _ := strconv.Atoi(match[1])
		db.savedName = args[index-1].(string)
	} else {
		return nil, fmt.Errorf("unexpected SQL: %s", sql)
	}
	raw, _ := json.Marshal(Object{"id": 1, "candidate_id": 2, "apply_id": db.applyID, "resume_file": db.savedName})
	return &filenameRows{raw: raw}, nil
}

type filenameRows struct {
	pgx.Rows
	raw  []byte
	done bool
}

func (r *filenameRows) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}
func (r *filenameRows) Scan(dest ...any) error { *dest[0].(*[]byte) = r.raw; return nil }
func (r *filenameRows) Err() error             { return nil }
func (r *filenameRows) Close()                 {}

func filenameArchive(t *testing.T, rawName string, legacy bool) *zip.Reader {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: rawName, NonUTF8: legacy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write([]byte("%PDF-1.7\nfixture content")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestResumePackageFilenameSQLAndDiskEncoding(t *testing.T) {
	for _, tc := range []struct {
		name, path, encoding, applyID, want string
	}{
		{"ASCII", "folder/Alex(A123).pdf", "utf8", "A123", "Alex(A123).pdf"},
		{"UTF8", "简历/张三（A123）.pdf", "utf8", "A123", "张三（A123）.pdf"},
		{"unflagged UTF8", "简历/张三（A123）.pdf", "unflagged", "A123", "张三（A123）.pdf"},
		{"GBK", "简历/张三(A123).pdf", "gbk", "A123", "张三(A123).pdf"},
		{"GBK fullwidth and ID", "简历\\张三（应聘123）.pdf", "gbk", "应聘123", "张三（应聘123）.pdf"},
		{"GBK backslash byte", "简历\\乗（A123）.pdf", "gbk", "A123", "乗（A123）.pdf"},
		{"GB18030 four bytes", "简历/𠮷（A123）.pdf", "gb18030", "A123", "𠮷（A123）.pdf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.path
			var err error
			switch tc.encoding {
			case "gbk":
				raw, err = simplifiedchinese.GBK.NewEncoder().String(raw)
			case "gb18030":
				raw, err = simplifiedchinese.GB18030.NewEncoder().String(raw)
			}
			if err != nil {
				t.Fatal(err)
			}
			a := unitApp(t)
			a.Config.MediaRoot = t.TempDir()
			db := &filenameDB{applyID: tc.applyID}
			affected := map[int64]bool{}
			files, err := a.stagePackage(context.Background(), db, filenameArchive(t, raw, tc.encoding != "utf8"), affected)
			if err != nil {
				t.Fatal(err)
			}
			defer files.close()
			if !strings.HasSuffix(db.savedName, "-"+tc.want) || len(db.savedName) != 33+len(tc.want) || !affected[2] || len(files.items) != 1 {
				t.Fatalf("filename/ID mismatch: saved=%q want=%q affected=%v", db.savedName, tc.want, affected)
			}
			if err = files.install(); err != nil {
				t.Fatal(err)
			}
			files.committed = true
			data, err := os.ReadFile(filepath.Join(a.Config.MediaRoot, "resumes", db.savedName))
			if err != nil || string(data) != "%PDF-1.7\nfixture content" {
				t.Fatalf("DB filename cannot find original PDF: %v", err)
			}
		})
	}
}

func TestResumePackageRejectsInvalidNamesBeforeSQL(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag uint16
	}{
		{"\xff(A123).pdf", 0},
		{"\x81(A123).pdf", 0},
		{"name\x00(A123).pdf", 0},
		{"\xd5\xc5\xc8\xfd(A123).pdf", 0x800}, // Invalid bytes despite declaring UTF-8.
	} {
		t.Run(fmt.Sprintf("%x-%x", tc.name, tc.flag), func(t *testing.T) {
			a := unitApp(t)
			a.Config.MediaRoot = t.TempDir()
			archive := filenameArchive(t, tc.name, true)
			archive.File[0].Flags |= tc.flag
			db := &filenameDB{applyID: "A123"}
			_, err := a.stagePackage(context.Background(), db, archive, map[int64]bool{})
			var api *apiError
			if !errors.As(err, &api) || api.Status != 400 || db.calls != 0 {
				t.Fatalf("invalid filename must fail before SQL: calls=%d err=%v", db.calls, err)
			}
			entries, err := os.ReadDir(a.Config.MediaRoot)
			if err != nil || len(entries) != 1 || entries[0].Name() != "resumes" {
				t.Fatalf("invalid filename left temporary files: %v", err)
			}
		})
	}
}
