package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"resume-platform/internal/contract"
	pdftext "resume-platform/internal/pdftext"
)

type taskFailure struct {
	Code, Message   string
	Manifest, Trace Object
}

func (e *taskFailure) Error() string       { return e.Message }
func taskError(code, message string) error { return &taskFailure{Code: code, Message: message} }
func (a *App) kernelCapabilities(ctx context.Context) (Object, error) {
	if a.Config.KernelToken == "" {
		return nil, taskError("agent_kernel_unavailable", "Kernel 服务令牌尚未配置")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(a.Config.KernelURL, "/")+"/v2/capabilities", nil)
	req.Header.Set("X-Agent-Kernel-Token", a.Config.KernelToken)
	res, err := a.HTTP.Do(req)
	if err != nil {
		return nil, taskError("agent_kernel_unavailable", "Kernel 能力发现失败")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65536))
	if err != nil || res.StatusCode != 200 {
		return nil, taskError("agent_kernel_unavailable", "Kernel 能力发现失败")
	}
	if contract.Validate("capabilities", raw) != nil {
		return nil, taskError("agent_protocol_incompatible", "平台与 Kernel 协议不兼容，请配套升级")
	}
	var caps Object
	json.Unmarshal(raw, &caps)
	if truth(caps["mock"]) != (a.Config.Debug && boolEnv("AGENT_KERNEL_ALLOW_MOCK")) {
		return nil, taskError("agent_protocol_incompatible", "模拟与真实 Kernel 环境不匹配")
	}
	if a.Config.KernelBuild != "" && caps["kernel_build"] != a.Config.KernelBuild {
		return nil, taskError("kernel_version_unavailable", "Kernel 与部署锁定版本不一致")
	}
	return caps, nil
}
func (a *App) runtimePin(ctx context.Context) (Object, Object, error) {
	c, key, err := a.connection(ctx)
	if err != nil {
		return nil, nil, err
	}
	if str(c["model_name"]) == "" || str(c["base_url"]) == "" || str(a.configValue(ctx, "ai_connection_test_fingerprint", "")) != connectionFingerprint(c, key) {
		return nil, nil, taskError("ai_not_configured", "当前模型连接尚未测试通过，请先在系统设置中验证")
	}
	caps, err := a.kernelCapabilities(ctx)
	if err != nil {
		return nil, nil, err
	}
	pin := Object{"kernel_build": caps["kernel_build"], "protocol_version": protocolVersion, "toolset_version": caps["toolset_version"], "result_schema_version": resultVersion, "policy_version": policyVersion, "instruction_version": caps["instruction_version"], "model_config_revision": connectionFingerprint(c, key)}
	pin["pin_id"] = fingerprint(pin)
	return pin, c, nil
}
func (a *App) resumeFile(filename string) (string, error) {
	if strings.TrimSpace(filename) == "" {
		return "", taskError("pdf_missing", "PDF 简历文件缺失，请补齐文件")
	}
	root, err := filepath.Abs(a.Config.MediaRoot)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", taskError("pdf_missing", "材料目录不存在或不可读")
	}
	path := filepath.Join(root, "resumes", filepath.Base(filename))
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", taskError("pdf_missing", "PDF 简历文件缺失或不可读")
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", taskError("pdf_missing", "PDF 材料路径无效")
	}
	return resolved, nil
}
func fileDigest(ctx context.Context, path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, taskError("pdf_missing", "PDF 简历文件缺失或不可读")
	}
	defer f.Close()
	hash := sha256.New()
	size := int64(0)
	buffer := make([]byte, 65536)
	for {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		n, err := f.Read(buffer)
		size += int64(n)
		hash.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", size, taskError("pdf_missing", "PDF 文件读取失败")
		}
	}
	if size == 0 {
		return "", 0, taskError("pdf_damaged", "PDF 文件为空")
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
func (a *App) extractText(ctx context.Context, item Object, volunteer Object) (pdftext.Text, Object, error) {
	var text pdftext.Text
	path, err := a.resumeFile(str(obj(volunteer["artifact"])["path"]))
	if err != nil {
		return text, nil, err
	}
	checksum, size, err := fileDigest(ctx, path)
	if err != nil {
		return text, nil, err
	}
	artifact := obj(volunteer["artifact"])
	if prior := str(artifact["checksum"]); prior != "" && prior != checksum {
		return text, nil, taskError("pdf_changed", "PDF 已更换，请重新提交任务")
	}
	artifact["checksum"] = checksum
	artifact["size_bytes"] = size
	deadline := time.Duration(num(env("RESUME_TEXT_TIMEOUT_SECONDS", "60"))) * time.Second
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	options := pdftext.Options{Path: path, SHA256: checksum, Size: size, Pages: int(num(env("RESUME_TEXT_MAX_PAGES", "100"))), Inspect: true}
	extract := a.Extract
	if extract == nil {
		extract = pdftext.Run
	}
	info, err := extract(ctx, options)
	if err != nil {
		return text, nil, err
	}
	cached, cacheErr := one(ctx, a.Pool, "SELECT row_to_json(t) FROM core_resumetextextraction t WHERE file_sha256=$1 AND extractor_version=$2", checksum, info.ExtractorVersion)
	if cacheErr != nil {
		var missing *apiError
		if !errors.As(cacheErr, &missing) || missing.Status != 404 {
			return text, nil, cacheErr
		}
	}
	if cached != nil {
		raw := canonicalJSON(cached["payload"], false)
		if json.Unmarshal(raw, &text) != nil || text.FileSHA256 != checksum || text.ExtractorVersion != info.ExtractorVersion || !validText(text) {
			cached = nil
		}
	}
	if cached == nil {
		options.Inspect = false
		extracted, err := extract(ctx, options)
		if err != nil {
			return text, nil, err
		}
		if extracted.Text == nil || !validText(*extracted.Text) || extracted.Text.FileSHA256 != checksum || extracted.Text.ExtractorVersion != info.ExtractorVersion {
			return text, nil, taskError("resume_text_invalid", "提取文本校验失败，请重试材料")
		}
		text = *extracted.Text
		if ctx.Err() != nil {
			return text, nil, ctx.Err()
		}
		raw := canonicalJSON(text, false)
		cached, err = one(ctx, a.Pool, "INSERT INTO core_resumetextextraction AS t (file_sha256,extractor_version,payload,created_at) VALUES($1,$2,$3::jsonb,now()) ON CONFLICT(file_sha256,extractor_version) DO UPDATE SET payload=EXCLUDED.payload RETURNING row_to_json(t)", checksum, text.ExtractorVersion, string(raw))
		if err != nil {
			return text, nil, err
		}
	}
	if ctx.Err() != nil {
		return text, nil, ctx.Err()
	}
	_, err = a.Pool.Exec(ctx, "UPDATE core_processingrunscopeitem SET text_extraction_id=$1,processing_node='validating_text' WHERE id=$2", cached["id"], item["id"])
	return text, artifact, err
}
func validText(t pdftext.Text) bool {
	if len(t.Pages) < 1 || len(t.Pages) > 100 || len(t.ExtractorVersion) < 1 || len(t.ExtractorVersion) > 128 || !utf8.ValidString(t.ExtractorVersion) || (t.Status != "ready" && t.Status != "needs_attention") || len(t.Warnings) > 200 {
		return false
	}
	if decoded, err := hex.DecodeString(t.FileSHA256); err != nil || len(decoded) != 32 || strings.ToLower(t.FileSHA256) != t.FileSHA256 {
		return false
	}
	for _, warning := range t.Warnings {
		if !utf8.ValidString(warning) {
			return false
		}
	}
	if t.Status == "needs_attention" && len(t.Warnings) == 0 {
		return false
	}
	for _, p := range t.Pages {
		if !utf8.ValidString(p) || strings.ContainsAny(p, "\r\f\x00") {
			return false
		}
	}
	if t.Status == "ready" && strings.TrimSpace(strings.Join(t.Pages, "")) == "" {
		return false
	}
	raw := []byte(strings.Join(t.Pages, "\f"))
	hash := sha256.Sum256(raw)
	return len(raw) <= 1<<20 && hex.EncodeToString(hash[:]) == t.TextSHA256
}
func (a *App) analyze(ctx context.Context, item, frozen Object, text pdftext.Text) (Object, error) {
	c, key, err := a.connection(ctx)
	if err != nil {
		return nil, err
	}
	if str(a.configValue(ctx, "ai_connection_test_fingerprint", "")) != connectionFingerprint(c, key) {
		return nil, taskError("ai_not_configured", "当前模型连接尚未测试通过")
	}
	pin := clone(obj(frozen["pin"]))
	delete(pin, "pin_id")
	pin["model_config_revision"] = connectionFingerprint(c, key)
	pin["pin_id"] = fingerprint(pin)
	snapshot := obj(frozen["snapshot"])
	d := obj(frozen["preflight"])
	refs := stringValues(d["job_refs"])
	resolved := prepareSnapshot(snapshot)
	if resolved["status"] != "ready" || resolved["standard_code"] != d["standard_code"] || resolved["pool_code"] != d["pool_code"] {
		return nil, taskError("agent_snapshot_unavailable", "冻结投递与评估标准不一致，请重新提交")
	}
	if len(refs) != 1 || d["standard_code"] != refs[0] || obj(frozen["pin"])["policy_version"] != policyVersion {
		return nil, taskError("agent_snapshot_unavailable", "任务未固定当前投递的评估标准，请重新提交")
	}
	jobs := []any{}
	for _, v := range list(snapshot["jobs"]) {
		job := obj(v)
		if contains(refs, str(job["ref"])) {
			jobs = append(jobs, brief(job, "ref", "content_hash", "entity", "public_name", "position_name", "category", "job_family", "location", "education", "required_majors", "responsibilities", "department_ref", "department_name"))
		}
	}
	taskID := str(frozen["task_id"]) + "-" + str(item["attempt_count"])
	request := Object{"protocol_version": protocolVersion, "task_kind": "candidate.application_assessment", "task_id": taskID, "trigger": "processing_run", "workflow_revision": obj(snapshot["workflow"])["revision"], "pin": pin, "scope": Object{"candidate": brief(obj(snapshot["candidate"]), "ref", "highest_major", "highest_education"), "volunteer_ref": d["current_volunteer_ref"], "resume_text": text, "jobs": jobs, "tag_catalog": snapshot["tag_catalog"]}, "model": Object{"api_style": c["api_style"], "base_url": c["base_url"], "model_name": c["model_name"], "structured_output_mode": a.configValue(ctx, "ai_connection_structured_output_mode", "json_compat"), "timeout_seconds": a.configValue(ctx, "ai_timeout_seconds", 60), "retry_count": a.configValue(ctx, "ai_retry_count", 1), "insecure_skip_verify": boolEnv("AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY")}, "budget": Object{"max_turns": 32, "max_tool_calls": 256, "max_duration_seconds": 600, "max_tokens": 120000, "max_context_tokens": a.configValue(ctx, "ai_context_tokens", 32768)}}
	request["idempotency_key"] = fingerprint(Object{"task_id": taskID, "pin": pin, "scope": request["scope"], "workflow_revision": request["workflow_revision"], "budget": request["budget"]})
	raw := canonicalJSON(request, false)
	if len(raw) > 2<<20 {
		return nil, taskError("request_too_large", "全文与岗位数据编码后超过 2 MiB 请求上限，未截断全文")
	}
	if err = contract.Validate("request", raw); err != nil {
		return nil, taskError("agent_invalid_input", "候选人输入不符合文本协议")
	}
	if reused, err := a.reusableAnalysis(ctx, item, frozen, request, text, refs); err != nil || reused != nil {
		return reused, err
	}
	ctx, cancel := context.WithTimeout(ctx, 615*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.Config.KernelURL, "/")+"/v2/tasks/execute", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Kernel-Token", a.Config.KernelToken)
	if key != "" {
		req.Header.Set("X-Model-API-Key", key)
	}
	client := *a.HTTP
	client.Timeout = 615 * time.Second
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, taskError("agent_kernel_unavailable", "Kernel 连接失败")
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return nil, taskError("agent_invalid_output", "Kernel 响应无效或超限")
	}
	var result Object
	if json.Unmarshal(raw, &result) != nil {
		return nil, taskError("agent_invalid_output", "Kernel 响应不是合法 JSON")
	}
	if res.StatusCode != 200 {
		code := str(result["code"])
		if contains([]string{"agent_protocol_incompatible", "kernel_version_unavailable", "idempotency_conflict", "request_too_large"}, code) {
			return nil, taskError(code, "Kernel 拒绝请求，请检查配套版本或重新提交任务")
		}
		return nil, taskError("agent_kernel_unavailable", "Kernel 未完成分析")
	}
	if contract.Validate("response", raw) != nil {
		return nil, taskError("agent_invalid_output", "Kernel 返回内容不符合协议")
	}
	if err = validateAnalysis(request, result, text, refs); err != nil {
		return nil, err
	}
	result["deterministic"] = d
	return result, nil
}
func normQuote(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
func validateAnalysis(request, result Object, text pdftext.Text, refs []string) error {
	invalid := taskError("agent_invalid_output", "分析结果的任务、版本、全文、证据或岗位引用不一致")
	for _, key := range []string{"protocol_version", "task_id", "idempotency_key", "workflow_revision", "pin"} {
		if !bytes.Equal(canonicalJSON(request[key], false), canonicalJSON(result[key], false)) {
			return invalid
		}
	}
	manifest := obj(result["manifest"])
	if manifest["terminal_state"] != "DONE" {
		code := str(manifest["failure_code"])
		mapping := map[string]string{"task_timeout": "llm_timeout", "task_cancelled": "agent_cancelled", "budget_exhausted": "agent_budget_exhausted", "text_invalid": "resume_text_unavailable", "model_connection_error": "ai_connection_error", "model_rate_limited": "ai_rate_limited", "model_output_invalid": "agent_invalid_output", "materials_incomplete": "agent_incomplete"}
		for _, reason := range []string{"token_budget_exhausted", "next_request_budget_insufficient", "context_limit_exceeded", "turn_budget_exhausted", "tool_budget_exhausted"} {
			mapping[reason] = "agent_budget_exhausted"
		}
		mapping["analysis_stalled"] = "agent_stalled"
		mapped := mapping[code]
		if mapped == "" {
			mapped = "agent_incomplete"
		}
		return &taskFailure{Code: mapped, Message: "候选人分析未完成，请重试或人工处理", Manifest: manifest, Trace: obj(result["safe_trace"])}
	}
	if str(manifest["resume_checksum"]) != text.FileSHA256 || str(obj(result["profile"])["source_text"]) != strings.Join(text.Pages, "\f") {
		return invalid
	}
	type sourceLine struct {
		page int
		text string
	}
	lines := []sourceLine{}
	for page, body := range text.Pages {
		for _, line := range strings.Split(body, "\n") {
			lines = append(lines, sourceLine{page + 1, line})
		}
	}
	verify := func(e Object) bool {
		start, end, page := int(num(e["start_line"])), int(num(e["end_line"])), int(num(e["page"]))
		q := normQuote(str(e["quote"]))
		if len([]rune(q)) < 8 || start < 1 || end < start || end > len(lines) || end-start > 100 {
			return false
		}
		var source strings.Builder
		for _, line := range lines[start-1 : end] {
			if line.page != page {
				return false
			}
			source.WriteString(line.text)
		}
		return strings.Contains(normQuote(source.String()), q)
	}
	for _, claim := range list(obj(result["profile"])["claims"]) {
		for _, e := range list(obj(claim)["evidence"]) {
			if !verify(obj(e)) {
				return invalid
			}
		}
	}
	knownTags := map[string]bool{}
	for _, v := range list(obj(request["scope"])["tag_catalog"]) {
		knownTags[str(obj(v)["code"])] = true
	}
	seenTags := map[string]bool{}
	for _, v := range list(obj(result["profile"])["tags"]) {
		tag := obj(v)
		code := str(tag["code"])
		if !knownTags[code] || seenTags[code] {
			return invalid
		}
		seenTags[code] = true
		for _, evidence := range list(tag["evidence"]) {
			if !verify(obj(evidence)) {
				return invalid
			}
		}
		if floatValue(tag["confidence"]) < .8 {
			tag["status"] = "needs_verification"
		}
	}
	seen := map[string]bool{}
	matches := list(result["matches"])
	lastScore := math.Inf(1)
	lastRef := ""
	for i, value := range matches {
		match := obj(value)
		ref := str(match["job_ref"])
		if seen[ref] || !contains(refs, ref) {
			return invalid
		}
		seen[ref] = true
		score := 0.0
		weights := map[string]float64{"major_match": .3, "skills_match": .2, "experience_evidence": .25, "job_requirement": .15, "resume_quality": .1}
		for key, weight := range weights {
			v, _ := obj(match["dimensions"])[key].(float64)
			score += v * weight
		}
		score = math.Round(score*10000) / 10000
		actual, _ := match["score"].(float64)
		if math.Abs(score-actual) > .00011 || num(match["rank"]) != int64(i+1) || score > lastScore || (score == lastScore && lastRef > ref) {
			return invalid
		}
		lastScore, lastRef = score, ref
		for _, e := range list(match["evidence"]) {
			if !verify(obj(e)) {
				return invalid
			}
		}
	}
	covered := stringValues(manifest["covered_jobs"])
	sort.Strings(covered)
	expected := append([]string{}, refs...)
	sort.Strings(expected)
	if len(seen) != len(refs) || fmt.Sprint(covered) != fmt.Sprint(expected) {
		return invalid
	}
	if bytes.Contains(canonicalJSON(result, false), []byte(`\u0000`)) {
		return invalid
	}
	return nil
}
func (a *App) analysisPresentation(ctx context.Context, decision Object, detail bool) Object {
	stored := obj(decision["kernel_result"])
	if len(stored) == 0 {
		return Object{}
	}
	if !detail {
		return Object{"match_count": len(list(stored["matches"])), "terminal_state": obj(stored["manifest"])["terminal_state"], "reused": str(obj(stored["manifest"])["reused_from_task_id"]) != ""}
	}
	value := clone(stored)
	delete(obj(value["profile"]), "source_text")
	item, _ := one(ctx, a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i JOIN core_candidateworkflow w ON w.candidate_id=i.candidate_id WHERE w.id=$1 AND i.run_id=$2 LIMIT 1", decision["workflow_id"], decision["processing_run_id"])
	frozen := obj(item["kernel_snapshot"])
	jobs := map[string]Object{}
	for _, v := range list(obj(frozen["snapshot"])["jobs"]) {
		j := obj(v)
		jobs[str(j["ref"])] = j
	}
	for _, v := range list(value["matches"]) {
		m := obj(v)
		j := jobs[str(m["job_ref"])]
		title := str(j["position_name"])
		if title == "" {
			title = str(j["public_name"])
		}
		if title == "" {
			title = "岗位名称不可用"
		}
		m["job_title"] = title
		m["department_name"] = str(j["department_name"])
		m["is_selected"] = num(obj(frozen["job_ids"])[str(m["job_ref"])]) == num(decision["recommended_job_id"]) && decision["recommended_job_id"] != nil
	}
	return value
}
