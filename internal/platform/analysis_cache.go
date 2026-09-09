package platform

import (
	"context"
	"sort"
	"strings"

	"resume-platform/internal/contract"
	pdftext "resume-platform/internal/pdftext"
)

func analysisContentKey(frozen, pin Object, text pdftext.Text) string {
	snapshot := obj(frozen["snapshot"])
	refs := stringValues(obj(frozen["preflight"])["job_refs"])
	jobs := []string{}
	for _, value := range list(snapshot["jobs"]) {
		job := obj(value)
		if contains(refs, str(job["ref"])) {
			jobs = append(jobs, str(job["ref"])+":"+str(job["content_hash"]))
		}
	}
	sort.Strings(jobs)
	return fingerprint(Object{"pin": pin, "candidate": brief(obj(snapshot["candidate"]), "highest_major", "highest_education"), "volunteer": obj(frozen["preflight"])["current_volunteer_ref"], "file_sha256": text.FileSHA256, "text_sha256": text.TextSHA256, "extractor_version": text.ExtractorVersion, "jobs": jobs, "taxonomy": snapshot["taxonomy"]})
}

// 容量和流程修订变化可复用已验证分析；正文、岗位要求、模型或未冻结的外部知识变化必须重新分析。
func (a *App) reusableAnalysis(ctx context.Context, item, frozen, request Object, text pdftext.Text, refs []string) (Object, error) {
	prior, err := rows(ctx, a.Pool, `SELECT json_build_object('kernel_snapshot',i.kernel_snapshot,'kernel_result',i.kernel_result,'text',t.payload) FROM core_processingrunscopeitem i JOIN core_resumetextextraction t ON i.text_extraction_id=t.id WHERE i.candidate_id=$1 AND i.id<>$2 AND i.kernel_result->'manifest'->>'terminal_state'='DONE' ORDER BY i.id DESC LIMIT 20`, item["candidate_id"], item["id"])
	if err != nil {
		return nil, err
	}
	key := analysisContentKey(frozen, obj(request["pin"]), text)
	for _, previous := range prior {
		source := obj(previous["text"])
		priorText := pdftext.Text{FileSHA256: str(source["file_sha256"]), TextSHA256: str(source["text_sha256"]), ExtractorVersion: str(source["extractor_version"])}
		result := clone(obj(previous["kernel_result"]))
		if analysisContentKey(obj(previous["kernel_snapshot"]), obj(result["pin"]), priorText) != key {
			continue
		}
		external := false
		for name := range obj(obj(result["manifest"])["tool_versions"]) {
			if strings.HasPrefix(name, "mcp.") {
				external = true
			}
		}
		if external {
			continue
		}
		delete(result, "deterministic")
		obj(result["manifest"])["reused_from_task_id"] = result["task_id"]
		for _, name := range []string{"task_id", "idempotency_key", "workflow_revision", "pin"} {
			result[name] = request[name]
		}
		trace := obj(result["safe_trace"])
		for _, name := range []string{"turns", "tool_call_count", "input_tokens", "output_tokens"} {
			trace[name] = 0
		}
		trace["tool_calls"] = []any{}
		if contract.Validate("response", canonicalJSON(result, false)) != nil || validateAnalysis(request, result, text, refs) != nil {
			continue
		}
		result["deterministic"] = obj(frozen["preflight"])
		return result, nil
	}
	return nil, nil
}
