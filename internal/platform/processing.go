package platform

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	pdftext "resume-platform/internal/pdftext"
)

func failureInfo(err error) (string, string) {
	var t *taskFailure
	if errors.As(err, &t) {
		return t.Code, t.Message
	}
	var p *pdftext.Failure
	if errors.As(err, &p) {
		return p.Code, p.Message
	}
	if errors.Is(err, context.Canceled) {
		return "agent_cancelled", "任务已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "llm_timeout", "处理超时，可重试此份简历"
	}
	return "processing_error", "后台处理失败，请重试或联系管理员"
}
func (a *App) lockWorkflow(ctx context.Context, db DB, candidateID any) (Object, error) {
	if _, err := one(ctx, db, "SELECT row_to_json(c) FROM core_candidate c WHERE id=$1 FOR UPDATE", candidateID); err != nil {
		return nil, err
	}
	w, err := one(ctx, db, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1 FOR UPDATE", candidateID)
	var e *apiError
	if errors.As(err, &e) && e.Status == 404 {
		return a.save(ctx, db, "core_candidateworkflow", nil, Object{"candidate_id": candidateID})
	}
	return w, err
}
func (a *App) itemOutcome(ctx context.Context, db DB, item Object, status, code, message string) error {
	resultType := status
	node := status
	switch status {
	case "success":
		resultType = "completed"
		node = "done"
	case "skipped_manual_change":
		resultType = ""
		node = "done"
	}
	values := Object{"status": status, "processing_node": node, "result_type": resultType, "reason_code": code, "result_message": message, "finished_at": time.Now().UTC()}
	if status == "failed" || status == "needs_attention" {
		values["error_code"] = code
		values["error_message"] = message
	}
	_, err := a.save(ctx, db, "core_processingrunscopeitem", item["id"], values)
	return err
}
func revisionChanged(item, w Object) bool {
	expected := item["workflow_revision_at_submit"]
	if expected == nil {
		return num(w["revision"]) != 0
	}
	return num(expected) != num(w["revision"])
}
func (a *App) prepareItems(ctx context.Context, run Object, step string) error {
	items, err := rows(ctx, a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1 ORDER BY candidate_id", run["id"])
	if err != nil {
		return err
	}
	for _, item := range items {
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			conflict, err := a.prepareItem(ctx, run, item["id"], step)
			if err != nil {
				return err
			}
			if !conflict {
				break
			}
			if !pause(ctx, time.Second) {
				return ctx.Err()
			}
		}
	}
	return a.summarizeRun(ctx, run["id"], false)
}
func (a *App) prepareItem(ctx context.Context, run Object, id any, step string) (bool, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	live, err := one(ctx, tx, "SELECT row_to_json(r) FROM core_processingrun r WHERE id=$1 FOR UPDATE", run["id"])
	if err != nil {
		return false, err
	}
	if live["cancel_requested_at"] != nil {
		return false, taskError("agent_cancelled", "任务已取消")
	}
	item, err := one(ctx, tx, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return false, err
	}
	if terminalItem(str(item["status"])) {
		return false, nil
	}
	w, err := a.lockWorkflow(ctx, tx, item["candidate_id"])
	if err != nil {
		return false, err
	}
	outcome := func(status, code, message string) (bool, error) {
		if err := a.itemOutcome(ctx, tx, item, status, code, message); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	if revisionChanged(item, w) {
		return outcome("skipped_manual_change", "workflow_changed_after_submit", "流程已被人工操作或其他任务修改，已跳过")
	}
	if expiry, err := time.Parse(time.RFC3339Nano, str(w["active_processing_expires_at"])); err == nil && expiry.After(time.Now()) && num(w["active_processing_scope_item_id"]) != num(id) {
		return true, nil
	}
	force := forceReprocess(run)
	if !force && contains([]string{"passed", "archived"}, str(w["status"])) {
		return outcome("success", "terminal_workflow", "流程已结束，已保留现有结果")
	}
	frozen := obj(item["kernel_snapshot"])
	d := obj(frozen["preflight"])
	resumeID := obj(frozen["volunteer_ids"])[str(d["current_volunteer_ref"])]
	resume, _ := a.get(ctx, tx, "core_resume", resumeID)
	if step == "step1" {
		for i, ref := range list(d["volunteer_order"]) {
			rid := obj(frozen["volunteer_ids"])[str(ref)]
			ranked, readErr := a.get(ctx, tx, "core_resume", rid)
			if readErr != nil {
				return false, readErr
			}
			if _, err = a.save(ctx, tx, "core_resume", rid, Object{"volunteer_rank": i + 1, "assigned_entity": ranked["entity"]}); err != nil {
				return false, err
			}
		}
		candidate, _ := a.get(ctx, tx, "core_candidate", item["candidate_id"])
		if _, err = a.save(ctx, tx, "core_candidate", candidate["id"], Object{"preferred_entity": preferredEntity(obj(obj(frozen["snapshot"])["candidate"]))}); err != nil {
			return false, err
		}
		if str(run["step"]) == "step1" {
			return outcome("success", "dedup_completed", "简历与志愿整理完成")
		}
		return false, tx.Commit(ctx)
	}
	if resume == nil {
		if err = a.archiveWorkflow(ctx, tx, w, "all_rejected", "全部可尝试志愿均已反馈未通过"); err != nil {
			return false, err
		}
		return outcome("success", "no_effective_volunteer", "没有可继续处理的有效志愿")
	}
	if step == "step2" {
		if force {
			if err = a.cancelOpenAttempts(ctx, tx, w, "rerun", []string{"ai", "rule"}); err != nil {
				return false, err
			}
			if _, err = a.save(ctx, tx, "core_candidateworkflow", w["id"], Object{"status": "in_progress", "passed_attempt_id": nil, "archive_reason": "", "archive_detail": "", "block_reason": "", "block_detail": "", "completed_at": nil}); err != nil {
				return false, err
			}
		}
		values := Object{}
		for _, degree := range []string{"first", "highest"} {
			values[degree+"_degree_tag_id"] = obj(frozen["tag_ids"])[str(d[degree+"_degree_tag_ref"])]
			tag, readErr := a.get(ctx, tx, "core_schooltag", values[degree+"_degree_tag_id"])
			if readErr != nil {
				return false, readErr
			}
			values[degree+"_degree_platform"] = tag["name"]
		}
		if _, err = a.save(ctx, tx, "core_candidate", item["candidate_id"], values); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM core_candidate_school_tags WHERE candidate_id=$1", item["candidate_id"]); err != nil {
			return false, err
		}
		for key, v := range values {
			if v != nil && strings.HasSuffix(key, "_tag_id") {
				if _, err = tx.Exec(ctx, "INSERT INTO core_candidate_school_tags(candidate_id,schooltag_id) VALUES($1,$2) ON CONFLICT DO NOTHING", item["candidate_id"], v); err != nil {
					return false, err
				}
			}
		}
		if !truth(d["admission_passed"]) {
			if err = a.cancelOpenAttempts(ctx, tx, w, "rerun", nil); err != nil {
				return false, err
			}
			if err = a.touchWorkflow(ctx, tx, w, resume); err != nil {
				return false, err
			}
			if err = a.archiveWorkflow(ctx, tx, w, "school_rule_not_matched", "未通过院校或学历准入检查"); err != nil {
				return false, err
			}
			return outcome("success", str(d["status"]), "未通过院校或学历准入检查")
		}
	}
	if step == "step3" && str(d["status"]) != "ready" {
		reason := map[string]string{"job_not_found": "job_not_matched", "job_pool_empty": "department_not_found", "job_mapping_ambiguous": "job_mapping_ambiguous", "internal_position_name_missing": "internal_position_name_missing"}[str(d["status"])]
		if reason == "" {
			reason = "agent_no_recommendation"
		}
		message := map[string]string{"job_not_found": "当前志愿未找到对应岗位", "job_pool_empty": "岗位缺少有效一级或二级部门", "job_mapping_ambiguous": "外部岗位对应多个内部职位，请修正岗位配置", "internal_position_name_missing": "岗位缺少内部职位名称", "job_responsibility_missing": "岗位职责未填写，请补齐后重试"}[str(d["status"])]
		if message == "" {
			message = "当前志愿未通过准入与岗位检查"
		}
		if err = a.cancelOpenAttempts(ctx, tx, w, "rerun", nil); err != nil {
			return false, err
		}
		if err = a.touchWorkflow(ctx, tx, w, resume); err != nil {
			return false, err
		}
		if err = a.archiveWorkflow(ctx, tx, w, reason, message); err != nil {
			return false, err
		}
		return outcome("needs_attention", str(d["status"]), message)
	}
	return false, tx.Commit(ctx)
}
func (a *App) analyzeItems(ctx context.Context, run Object) error {
	for ctx.Err() == nil {
		items, err := rows(ctx, a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1 AND status IN ('pending','processing','waiting_conflict') ORDER BY candidate_id", run["id"])
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		limit := max(1, min(20, int(num(run["ai_concurrency_limit"]))))
		slots := make(chan struct{}, limit)
		var wg sync.WaitGroup
		errorCh := make(chan error, len(items))
		for _, item := range items {
			select {
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			case slots <- struct{}{}:
			}
			wg.Add(1)
			go func(item Object) {
				defer wg.Done()
				defer func() { <-slots }()
				if err := a.processItem(ctx, run, item["id"]); err != nil {
					errorCh <- err
				}
			}(item)
		}
		wg.Wait()
		close(errorCh)
		for err := range errorCh {
			if err != nil {
				return err
			}
		}
		if err = a.summarizeRun(ctx, run["id"], false); err != nil {
			return err
		}
		if !pause(ctx, 250*time.Millisecond) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}
func forceReprocess(run Object) bool {
	scope := obj(run["scope"])
	return len(list(scope["system_statuses"])) > 0 || truth(scope["force_reprocess"]) || str(scope["source"]) == "ai_retry" || str(scope["trigger"]) == "feedback_rejected"
}
func (a *App) processItem(ctx context.Context, run Object, id any) error {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	live, err := one(ctx, tx, "SELECT row_to_json(r) FROM core_processingrun r WHERE id=$1 FOR UPDATE", run["id"])
	if err != nil {
		return err
	}
	if live["cancel_requested_at"] != nil {
		return taskError("agent_cancelled", "任务已取消")
	}
	item, err := one(ctx, tx, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return err
	}
	if terminalItem(str(item["status"])) {
		return nil
	}
	w, err := a.lockWorkflow(ctx, tx, item["candidate_id"])
	if err != nil {
		return err
	}
	if revisionChanged(item, w) {
		if err = a.itemOutcome(ctx, tx, item, "skipped_manual_change", "workflow_changed_after_submit", "流程已变化，已跳过"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if !forceReprocess(run) && contains([]string{"passed", "archived"}, str(w["status"])) {
		if err = a.itemOutcome(ctx, tx, item, "success", "terminal_workflow", "流程已结束，已保留现有结果"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if expiry, err := time.Parse(time.RFC3339Nano, str(w["active_processing_expires_at"])); err == nil && expiry.After(time.Now()) {
		if _, err = a.save(ctx, tx, "core_processingrunscopeitem", id, Object{"status": "waiting_conflict"}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	claim := token(16)
	if _, err = a.save(ctx, tx, "core_candidateworkflow", w["id"], Object{"active_processing_scope_item_id": id, "active_processing_token": claim, "active_processing_expires_at": time.Now().UTC().Add(20 * time.Second)}); err != nil {
		return err
	}
	item, err = a.save(ctx, tx, "core_processingrunscopeitem", id, Object{"status": "processing", "processing_node": "extracting", "dispatch_token": claim, "attempt_count": num(item["attempt_count"]) + 1, "started_at": time.Now().UTC(), "error_code": "", "error_message": ""})
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	itemCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		for pause(itemCtx, 2*time.Second) {
			tag, err := a.Pool.Exec(itemCtx, "UPDATE core_candidateworkflow SET active_processing_expires_at=now()+interval '20 seconds' WHERE id=$1 AND active_processing_scope_item_id=$2 AND active_processing_token=$3::uuid AND revision=$4", w["id"], id, claim, w["revision"])
			if err != nil || tag.RowsAffected() != 1 {
				cancel()
				return
			}
		}
	}()
	frozen := obj(item["kernel_snapshot"])
	d := obj(frozen["preflight"])
	var result Object
	var text pdftext.Text
	var workErr error
	if str(d["status"]) != "ready" {
		workErr = taskError("agent_snapshot_unavailable", "当前志愿未通过准入或岗位检查，请从准入检查重新处理")
	} else {
		var volunteer Object
		for _, v := range list(obj(frozen["snapshot"])["volunteers"]) {
			if obj(v)["ref"] == d["current_volunteer_ref"] {
				volunteer = obj(v)
			}
		}
		var artifact Object
		text, artifact, workErr = a.extractText(itemCtx, item, volunteer)
		if workErr == nil {
			volunteer["artifact"] = artifact
			_, workErr = a.save(itemCtx, a.Pool, "core_processingrunscopeitem", id, Object{"kernel_snapshot": frozen})
			if workErr == nil && text.Status != "ready" {
				workErr = taskError("resume_text_needs_attention", "材料可能是扫描件或内容不完整，请提供可复制文字的 PDF；"+strings.Join(text.Warnings, "；"))
			}
		}
		if workErr == nil {
			_, workErr = a.save(itemCtx, a.Pool, "core_processingrunscopeitem", id, Object{"processing_node": "analyzing"})
		}
		if workErr == nil {
			var release func()
			release, workErr = a.acquireModelSlot(itemCtx)
			if workErr == nil {
				result, workErr = a.analyze(itemCtx, item, frozen, text)
				release()
			}
		}
	}
	cancel()
	<-renewDone
	if ctx.Err() != nil {
		return ctx.Err()
	}
	tx, err = a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	live, err = one(ctx, tx, "SELECT row_to_json(r) FROM core_processingrun r WHERE id=$1 FOR UPDATE", run["id"])
	if err != nil {
		return err
	}
	item, err = one(ctx, tx, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return err
	}
	current, err := a.lockWorkflow(ctx, tx, item["candidate_id"])
	if err != nil {
		return err
	}
	if terminalItem(str(item["status"])) {
		return nil
	}
	matches := strings.ReplaceAll(str(current["active_processing_token"]), "-", "") == claim && num(current["active_processing_scope_item_id"]) == num(id)
	if !matches || num(current["revision"]) != num(w["revision"]) {
		if err = a.itemOutcome(ctx, tx, item, "skipped_manual_change", "workflow_changed_during_ai", "分析期间流程已变化，结果未写入"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err = a.save(ctx, tx, "core_candidateworkflow", w["id"], Object{"active_processing_scope_item_id": nil, "active_processing_token": nil, "active_processing_expires_at": nil}); err != nil {
		return err
	}
	if live["cancel_requested_at"] != nil {
		if err = a.itemOutcome(ctx, tx, item, "cancelled", "cancelled", "任务已取消"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if errors.Is(workErr, context.Canceled) {
		return workErr
	}
	if _, err = a.save(ctx, tx, "core_processingrunscopeitem", id, Object{"processing_node": "saving"}); err != nil {
		return err
	}
	resume, err := a.get(ctx, tx, "core_resume", item["prepared_resume_id"])
	if err != nil {
		workErr = taskError("pdf_missing", "简历材料已不存在")
	}
	if workErr == nil {
		workErr = a.validateLiveJobs(ctx, tx, frozen)
	}
	if resume != nil {
		if err = a.cancelOpenAttempts(ctx, tx, current, "rerun", []string{"ai", "rule"}); err != nil {
			return err
		}
		if err = a.touchWorkflow(ctx, tx, current, resume); err != nil {
			return err
		}
	}
	if workErr != nil {
		code, message := failureInfo(workErr)
		if resume != nil {
			if err = a.saveFailure(ctx, tx, live, current, resume, workErr); err != nil {
				return err
			}
		}
		if _, err = a.save(ctx, tx, "core_candidateworkflow", current["id"], Object{"block_reason": code, "block_detail": message, "revision": num(current["revision"]) + 1}); err != nil {
			return err
		}
		status := "needs_attention"
		if code == "llm_timeout" || code == "processing_error" {
			status = "failed"
		}
		if err = a.itemOutcome(ctx, tx, item, status, code, message); err != nil {
			return err
		}
	} else {
		code, message, err := a.applyAnalysis(ctx, tx, live, current, resume, item, frozen, result)
		if err != nil {
			return err
		}
		if _, err = a.save(ctx, tx, "core_processingrunscopeitem", id, Object{"kernel_result": result}); err != nil {
			return err
		}
		if _, err = a.save(ctx, tx, "core_candidateworkflow", current["id"], Object{"revision": num(current["revision"]) + 1}); err != nil {
			return err
		}
		if err = a.itemOutcome(ctx, tx, item, "success", code, message); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (a *App) validateLiveJobs(ctx context.Context, db DB, frozen Object) error {
	refs := stringValues(obj(frozen["preflight"])["job_refs"])
	liveJobs, err := rows(ctx, db, "SELECT row_to_json(j) FROM core_job j WHERE is_active ORDER BY id FOR UPDATE")
	if err != nil {
		return err
	}
	liveValues := []any{}
	for _, j := range liveJobs {
		dep, readErr := a.get(ctx, db, "core_department", j["department_id"])
		if readErr != nil && j["department_id"] != nil {
			return readErr
		}
		ref := ""
		if dep != nil {
			ref = a.ref("department", dep["id"])
		}
		liveValues = append(liveValues, Object{"ref": a.ref("job", j["id"]), "entity": j["entity"], "public_name": j["public_name"], "position_name": j["position_name"], "responsibilities": j["responsibilities"], "department_ref": ref, "department_level": dep["level"]})
	}
	liveSnapshot := clone(obj(frozen["snapshot"]))
	liveSnapshot["jobs"] = liveValues
	current := prepareSnapshot(liveSnapshot)
	liveRefs := stringValues(current["job_refs"])
	if current["status"] != "ready" || len(liveRefs) != len(refs) {
		return taskError("ai_reference_invalidated", "合规岗位池已变化，请重新分析")
	}
	for _, ref := range liveRefs {
		if !contains(refs, ref) {
			return taskError("ai_reference_invalidated", "合规岗位池已变化，请重新分析")
		}
	}
	for _, value := range list(obj(frozen["snapshot"])["jobs"]) {
		job := obj(value)
		if !contains(refs, str(job["ref"])) {
			continue
		}
		live, err := one(ctx, db, "SELECT row_to_json(j) FROM core_job j WHERE id=$1 FOR UPDATE", obj(frozen["job_ids"])[str(job["ref"])])
		if err != nil {
			return taskError("ai_reference_invalidated", "岗位已失效，请重新分析")
		}
		content, err := a.jobContent(ctx, db, live)
		if err != nil {
			return err
		}
		if fingerprint(content) != str(job["content_hash"]) {
			return taskError("ai_reference_invalidated", "岗位职责或所属部门已变化，请重新分析")
		}
	}
	return nil
}
func (a *App) saveFailure(ctx context.Context, db DB, run, w, resume Object, cause error) error {
	code, message := failureInfo(cause)
	values := a.auditValues(run)
	values["workflow_id"] = w["id"]
	values["resume_id"] = resume["id"]
	values["processing_run_id"] = run["id"]
	values["error_code"] = code
	values["error_message"] = message
	values["summary"] = "AI 未形成有效下发建议"
	values["risks"] = []any{message}
	values["risk_flags"] = []any{code}
	var t *taskFailure
	if errors.As(cause, &t) {
		values["safe_trace"] = t.Trace
		if len(t.Manifest) > 0 {
			values["kernel_result"] = Object{"manifest": t.Manifest, "safe_trace": t.Trace}
		}
	}
	_, err := a.save(ctx, db, "core_agentdispatchdecision", nil, values)
	return err
}
func (a *App) auditValues(run Object) Object {
	return Object{"model_name": str(run["model_name"]), "prompt_version": str(run["prompt_version"]), "decision_version": str(run["decision_version"]), "kernel_pin_id": str(run["pin_id"]), "kernel_build": str(run["kernel_build"]), "protocol_version": str(run["protocol_version"]), "toolset_version": str(run["toolset_version"])}
}
func floatValue(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return float64(num(v))
}
func recommendation(result, match, frozen Object) string {
	score := floatValue(match["score"])
	thresholds := obj(frozen["thresholds"])
	r := "archive"
	if score >= floatValue(thresholds["dispatch"]) {
		r = "dispatch"
	} else if score >= floatValue(thresholds["review"]) {
		r = "review"
	}
	if r == "dispatch" && contains(stringValues(obj(result["profile"])["risks"]), "profile_incomplete") {
		r = "review"
	}
	if frozen["lane"] == "review_only" {
		r = "review"
	}
	return r
}
func (a *App) applyAnalysis(ctx context.Context, db DB, run, w, resume, item, frozen, result Object) (string, string, error) {
	matches := list(result["matches"])
	if len(matches) == 0 {
		return "", "", taskError("agent_invalid_output", "分析未覆盖岗位")
	}
	selected := obj(matches[0])
	rec := recommendation(result, selected, frozen)
	capacities := map[int64]Object{}
	for _, ref := range stringValues(obj(frozen["preflight"])["job_refs"]) {
		jobID := obj(frozen["job_ids"])[ref]
		cap, err := one(ctx, db, "SELECT row_to_json(c) FROM core_processingrunjobcapacity c WHERE run_id=$1 AND job_id=$2 FOR UPDATE", run["id"], jobID)
		if err != nil {
			return "", "", err
		}
		job, err := a.get(ctx, db, "core_job", jobID)
		if err != nil {
			return "", "", err
		}
		cap, err = a.save(ctx, db, "core_processingrunjobcapacity", cap["id"], Object{"capacity": num(job["headcount"]) * num(run["job_hc_coefficient_snapshot"])})
		if err != nil {
			return "", "", err
		}
		capacities[num(jobID)] = cap
	}
	if rec != "archive" {
		for _, value := range matches {
			match := obj(value)
			cap := capacities[num(obj(frozen["job_ids"])[str(match["job_ref"])])]
			if num(cap["used_count"]) < num(cap["capacity"]) {
				selected = match
				rec = recommendation(result, selected, frozen)
				break
			}
		}
	}
	job, err := a.get(ctx, db, "core_job", obj(frozen["job_ids"])[str(selected["job_ref"])])
	if err != nil {
		return "", "", err
	}
	profile, err := a.persistProfile(ctx, db, resume, result)
	if err != nil {
		return "", "", err
	}
	risks := []any{}
	seen := map[string]bool{}
	for _, value := range append(list(selected["risks"]), list(obj(result["profile"])["risks"])...) {
		if !seen[str(value)] {
			risks = append(risks, value)
			seen[str(value)] = true
		}
	}
	evidence := []any{}
	for _, v := range list(selected["evidence"]) {
		evidence = append(evidence, obj(v)["quote"])
	}
	values := a.auditValues(run)
	pin := obj(result["pin"])
	values["kernel_pin_id"] = pin["pin_id"]
	values["kernel_build"] = pin["kernel_build"]
	values["workflow_id"] = w["id"]
	values["resume_id"] = resume["id"]
	values["profile_id"] = profile["id"]
	values["processing_run_id"] = run["id"]
	values["recommendation"] = rec
	values["evaluated_job_id"] = job["id"]
	values["recommended_job_id"] = job["id"]
	values["recommended_department_id"] = job["department_id"]
	values["matched_job_category"] = job["category"]
	values["confidence_score"] = selected["score"]
	values["score_breakdown"] = selected["dimensions"]
	values["summary"] = selected["reason"]
	values["reason"] = selected["reason"]
	values["evidence"] = evidence
	values["risks"] = risks
	values["risk_flags"] = risks
	values["kernel_result"] = result
	values["safe_trace"] = result["safe_trace"]
	decision, err := a.save(ctx, db, "core_agentdispatchdecision", nil, values)
	if err != nil {
		return "", "", err
	}
	if _, err = a.save(ctx, db, "core_resume", resume["id"], Object{"job_id": job["id"], "job_category": job["category"], "category_mode": "ai", "category_reason": selected["reason"]}); err != nil {
		return "", "", err
	}
	var capacityID any
	if rec != "archive" && frozen["lane"] != "review_only" {
		cap := capacities[num(job["id"])]
		if num(cap["used_count"]) >= num(cap["capacity"]) {
			message := "当前任务岗位 HC 容量已用尽，保留当前志愿等待重新分配"
			_, err = a.save(ctx, db, "core_candidateworkflow", w["id"], Object{"block_reason": "job_hc_exhausted", "block_detail": message})
			return "job_hc_exhausted", message, err
		}
		if _, err = a.save(ctx, db, "core_processingrunjobcapacity", cap["id"], Object{"used_count": num(cap["used_count"]) + 1}); err != nil {
			return "", "", err
		}
		capacityID = cap["id"]
	}
	if rec == "archive" {
		err = a.archiveWorkflow(ctx, db, w, "agent_no_recommendation", "AI 建议归档或置信度低于人工复核阈值")
		return "ai_archived", str(selected["reason"]), err
	}
	target, err := a.get(ctx, db, "core_department", job["department_id"])
	if err != nil {
		return "", "", err
	}
	attemptValues := Object{"source": "ai", "match_mode": "ai", "matched_rule_id": item["matched_rule_id"], "agent_decision_id": decision["id"], "confidence_score": selected["score"], "match_reason": selected["reason"], "capacity_reservation_id": capacityID, "review_required": rec == "review"}
	if rec == "review" {
		attemptValues["status"] = "pending_review"
	}
	if _, err = a.createAttempt(ctx, db, w, resume, target, attemptValues, nil); err != nil {
		return "", "", err
	}
	code := "ai_dispatched"
	if rec == "review" {
		code = "ai_review"
	}
	return code, str(selected["reason"]), nil
}
func (a *App) persistProfile(ctx context.Context, db DB, resume, result Object) (Object, error) {
	existing, _ := one(ctx, db, "SELECT row_to_json(p) FROM core_resumeprofile p WHERE resume_id=$1", resume["id"])
	values := Object{"resume_id": resume["id"], "file_checksum": obj(result["manifest"])["resume_checksum"], "raw_text": obj(result["profile"])["source_text"], "parse_model": obj(result["pin"])["kernel_build"], "profile_version": protocolVersion, "education_experiences": []any{}, "project_experiences": []any{}, "internship_experiences": []any{}, "skills": []any{}, "certificates": []any{}, "profile_risk_flags": obj(result["profile"])["risks"], "parse_status": "parsed", "parse_error": "", "parsed_at": time.Now().UTC()}
	summaries, majors := []string{}, []string{}
	for _, v := range list(obj(result["profile"])["claims"]) {
		c := obj(v)
		summary := str(c["summary"])
		summaries = append(summaries, summary)
		details := obj(c["details"])
		evidence := ""
		if es := list(c["evidence"]); len(es) > 0 {
			evidence = str(obj(es[0])["quote"])
		}
		switch str(c["kind"]) {
		case "major_direction":
			majors = append(majors, summary)
		case "skill":
			values["skills"] = append(list(values["skills"]), summary)
		case "certificate":
			values["certificates"] = append(list(values["certificates"]), summary)
		case "education":
			e := brief(details, "school_name", "degree", "major", "period")
			e["evidence"] = evidence
			values["education_experiences"] = append(list(values["education_experiences"]), e)
		case "project", "internship":
			key := str(c["kind"]) + "_experiences"
			e := brief(details, "name", "role", "period")
			if str(e["name"]) == "" {
				e["name"] = truncate(summary, 128)
			}
			e["description"] = summary
			e["evidence"] = evidence
			values[key] = append(list(values[key]), e)
		}
	}
	values["summary"] = strings.Join(summaries, "；")
	values["major_direction"] = truncate(strings.Join(majors, "；"), 128)
	return a.save(ctx, db, "core_resumeprofile", existing["id"], values)
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
