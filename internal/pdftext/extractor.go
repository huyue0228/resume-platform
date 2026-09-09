// Package extractor owns bounded, lossless PDF text extraction. It never uses OCR.
package extractor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxFileBytes = 32 << 20
const MaxTextBytes = 1 << 20
const MaxPages = 100

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Failure) Error() string      { return e.Message }
func fail(code, message string) error { return &Failure{code, message} }
func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail("text_extraction_timeout", "PDF 文字提取超时，已停止提取，可单份重试")
	}
	if ctx.Err() != nil {
		return fail("agent_cancelled", "任务已取消")
	}
	return nil
}

type Options struct {
	Path, SHA256 string
	Size         int64
	Pages        int
	Inspect      bool
}
type Text struct {
	FileSHA256       string   `json:"file_sha256"`
	TextSHA256       string   `json:"text_sha256"`
	ExtractorVersion string   `json:"extractor_version"`
	Pages            []string `json:"pages"`
	Status           string   `json:"status"`
	Warnings         []string `json:"warnings"`
}
type Result struct {
	FileSHA256       string `json:"file_sha256"`
	ExtractorVersion string `json:"extractor_version"`
	Text             *Text  `json:"text,omitempty"`
}
type commandFunc func(context.Context, string, ...string) ([]byte, []byte, error)

type boundedWriter struct {
	bytes.Buffer
	max      int
	exceeded *atomic.Bool
	cancel   context.CancelFunc
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	if w.Len()+len(data) > w.max {
		w.exceeded.Store(true)
		w.cancel()
		return 0, errors.New("output limit")
	}
	return w.Buffer.Write(data)
}
func command(parent context.Context, name string, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	exceeded := &atomic.Bool{}
	out := &boundedWriter{max: MaxTextBytes + 1, exceeded: exceeded, cancel: cancel}
	stderr := &boundedWriter{max: 65536, exceeded: exceeded, cancel: cancel}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if parent.Err() != nil {
		return nil, nil, contextError(parent)
	}
	if exceeded.Load() {
		return nil, nil, fail("resume_text_too_large", "提取文本或工具输出超限，未截断全文")
	}
	if err != nil {
		var missing *exec.Error
		if errors.As(err, &missing) {
			return nil, nil, fail("text_extractor_unavailable", "平台 Poppler 工具不可用")
		}
		private := strings.ToLower(stderr.String())
		if strings.Contains(private, "password") || strings.Contains(private, "encrypted") {
			return nil, nil, fail("pdf_encrypted", "PDF 已加密，请提供未加密文件")
		}
		return nil, nil, fail("pdf_damaged", "PDF 文件损坏或无法解析，请重新提供文件")
	}
	return out.Bytes(), stderr.Bytes(), nil
}

// Run snapshots and hashes the current PDF before cache probing or extraction.
func Run(ctx context.Context, options Options) (Result, error) { return run(ctx, options, command) }
func run(ctx context.Context, o Options, cmd commandFunc) (Result, error) {
	var result Result
	if err := contextError(ctx); err != nil {
		return result, err
	}
	if o.Pages < 1 || o.Pages > MaxPages {
		return result, fail("pdf_page_limit", "PDF 页数上限必须在 1 到 100 之间")
	}
	dir, err := os.MkdirTemp("", "resume-text-")
	if err != nil {
		return result, fail("text_extractor_unavailable", "无法创建提取临时目录")
	}
	defer os.RemoveAll(dir)
	pdf := filepath.Join(dir, "input.pdf")
	checksum, err := snapshot(ctx, o, pdf)
	if err != nil {
		return result, err
	}
	out, stderr, err := cmd(ctx, "pdftotext", "-v")
	if err != nil {
		return result, err
	}
	version := regexp.MustCompile(`pdftotext version ([^\s]+)`).FindSubmatch(append(out, stderr...))
	if len(version) != 2 || len(version[1]) > 80 {
		return result, fail("text_extractor_unavailable", "无法确认 PDF 提取器版本")
	}
	result = Result{FileSHA256: checksum, ExtractorVersion: "poppler-layout-go/v2.1:" + string(version[1])}
	if o.Inspect {
		return result, nil
	}
	info, infoDiagnostics, err := cmd(ctx, "pdfinfo", pdf)
	if err != nil {
		return result, err
	}
	if regexp.MustCompile(`(?m)^Encrypted:\s+yes`).Match(info) {
		return result, fail("pdf_encrypted", "PDF 已加密，请提供未加密文件")
	}
	match := regexp.MustCompile(`(?m)^Pages:\s+(\d+)`).FindSubmatch(info)
	pages := 0
	if len(match) == 2 {
		pages, _ = strconv.Atoi(string(match[1]))
	}
	if pages < 1 || pages > o.Pages {
		return result, fail("pdf_page_limit", "PDF 页数无效或超过材料页数上限")
	}
	raw, textDiagnostics, err := cmd(ctx, "pdftotext", "-layout", "-enc", "UTF-8", pdf, "-")
	if err != nil {
		return result, err
	}
	images, imageDiagnostics, err := cmd(ctx, "pdfimages", "-list", pdf)
	if err != nil {
		return result, err
	}
	text, err := canonical(raw, images, pages)
	if err != nil {
		return result, err
	}
	if err := contextError(ctx); err != nil {
		return result, err
	}
	// Poppler 可能在缺少字符映射或遇到语法错误时返回 0；不能把诊断信息静默丢弃。
	if len(bytes.TrimSpace(infoDiagnostics)) > 0 || len(bytes.TrimSpace(textDiagnostics)) > 0 || len(bytes.TrimSpace(imageDiagnostics)) > 0 {
		text.Status = "needs_attention"
		text.Warnings = append(text.Warnings, "PDF 提取工具报告字体、字符映射或文档结构异常，全文可能不完整，请核对或重新导出可复制文字的 PDF")
	}
	text.FileSHA256 = checksum
	text.ExtractorVersion = result.ExtractorVersion
	result.Text = &text
	return result, nil
}
func snapshot(ctx context.Context, o Options, target string) (string, error) {
	source, err := os.Open(o.Path)
	if err != nil {
		return "", fail("pdf_missing", "PDF 简历文件缺失或不可读，请重新提供文件")
	}
	defer source.Close()
	stat, err := source.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return "", fail("pdf_missing", "PDF 简历文件不可读")
	}
	if stat.Size() > MaxFileBytes {
		return "", fail("pdf_too_large", "PDF 超过 32 MiB 材料上限")
	}
	dest, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fail("text_extractor_unavailable", "无法创建提取临时文件")
	}
	defer dest.Close()
	hash := sha256.New()
	buffer := make([]byte, 65536)
	size := int64(0)
	for {
		if err := contextError(ctx); err != nil {
			return "", err
		}
		n, readErr := source.Read(buffer)
		size += int64(n)
		if size > MaxFileBytes {
			return "", fail("pdf_too_large", "PDF 超过 32 MiB 材料上限")
		}
		if n > 0 {
			hash.Write(buffer[:n])
			if _, err := dest.Write(buffer[:n]); err != nil {
				return "", fail("text_extractor_unavailable", "提取临时存储不可用")
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fail("pdf_missing", "PDF 读取失败")
		}
	}
	if size == 0 {
		return "", fail("pdf_damaged", "PDF 文件为空，请重新提供文件")
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if checksum != o.SHA256 || size != o.Size {
		return "", fail("pdf_changed", "PDF 已更换，请重新提交任务以使用新材料")
	}
	return checksum, nil
}
func visible(text string) int {
	n := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}
func canonical(raw, images []byte, pageCount int) (Text, error) {
	var result Text
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return result, fail("pdf_damaged", "提取文字编码无效，无法保证完整文本")
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")
	// Remove only Poppler's terminal FF; retain every blank page and trailing LF.
	pages := strings.Split(strings.TrimSuffix(text, "\f"), "\f")
	if len(pages) != pageCount {
		return result, fail("pdf_damaged", "提取分页无效，无法保证完整文本")
	}
	canonical := strings.Join(pages, "\f")
	if len(canonical) > MaxTextBytes {
		return result, fail("resume_text_too_large", "提取文本超过 1 MiB 上限，未截断全文")
	}
	warnings := []string{}
	counts := make([]int, len(pages))
	total := 0
	for i, page := range pages {
		counts[i] = visible(page)
		total += counts[i]
	}
	if total < 50 {
		warnings = append(warnings, "无足够有效文本，请提供可复制文字的 PDF")
	}
	imagePages := map[int]bool{}
	for _, line := range strings.Split(string(images), "\n") {
		p := strings.Fields(line)
		if len(p) < 5 {
			continue
		}
		page, _ := strconv.Atoi(p[0])
		width, _ := strconv.Atoi(p[3])
		height, _ := strconv.Atoi(p[4])
		if page >= 1 && page <= len(pages) && width >= 256 && height >= 256 {
			imagePages[page] = true
		}
	}
	for i, n := range counts {
		if n < 50 && imagePages[i+1] {
			warnings = append(warnings, "第 "+strconv.Itoa(i+1)+" 页疑似扫描页，内容可能不完整，请提供可复制文字的 PDF")
		}
	}
	hash := sha256.Sum256([]byte(canonical))
	status := "ready"
	if len(warnings) > 0 {
		status = "needs_attention"
	}
	return Text{Pages: pages, TextSHA256: hex.EncodeToString(hash[:]), Status: status, Warnings: warnings}, nil
}
