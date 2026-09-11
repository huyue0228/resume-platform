package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func onlyFields(body Object, allowed ...string) error {
	for key := range body {
		if !contains(allowed, key) {
			return bad("不支持字段：" + key)
		}
	}
	return nil
}
func (a *App) resourceAction(w http.ResponseWriter, r *http.Request, resource, action string, id any, p *Principal) error {
	ctx := r.Context()
	body := Object{}
	var err error
	if r.Method == "POST" || r.Method == "PATCH" {
		body, err = readBody(w, r)
		if err != nil {
			return err
		}
	}
	if resource == "pipeline/runs" {
		run, err := a.get(ctx, a.Pool, "core_processingrun", id)
		if err != nil {
			return err
		}
		switch action {
		case "cancel":
			if !p.has("pipeline.run") {
				return &apiError{403, "无取消任务权限"}
			}
			run, err = a.cancelRun(ctx, id, p)
			if err != nil {
				return err
			}
			value, err := a.serialize(ctx, resource, run, p, true)
			if err != nil {
				return err
			}
			write(w, 200, value)
			return nil
		case "materials", "material_text":
			if !p.has("resume.view") {
				return &apiError{403, "无查看简历权限"}
			}
			return a.materials(w, r, run, action)
		}
	}
	if action == "preview" || action == "resume_preview" {
		resumeID := id
		if resource == "workflow-attempts" {
			at, err := a.get(ctx, a.Pool, "core_assignmentattempt", id)
			if err != nil {
				return err
			}
			if !a.visibleAttempt(ctx, p, at) {
				return &apiError{404, "未找到记录"}
			}
			resumeID = at["resume_id"]
		}
		return a.preview(w, r, resumeID)
	}
	if action == "manual_assignment_options" {
		values, err := a.departmentOptions(ctx, p)
		if err != nil {
			return err
		}
		write(w, 200, Object{"results": values})
		return nil
	}
	if action == "manual_assign" {
		if !p.has("resume.manual_assign") {
			return &apiError{403, "无手动分配权限"}
		}
		if err = onlyFields(body, "target_department_id", "manual_reason"); err != nil {
			return err
		}
		at, err := a.manualAssign(ctx, id, body["target_department_id"], str(body["manual_reason"]), p)
		if err != nil {
			return err
		}
		value, err := a.serialize(ctx, "workflow-attempts", at, p, true)
		if err != nil {
			return err
		}
		write(w, 200, value)
		return nil
	}
	if resource == "workflow-attempts" && id != nil {
		at, err := a.get(ctx, a.Pool, "core_assignmentattempt", id)
		if err != nil {
			return err
		}
		if !a.visibleAttempt(ctx, p, at) {
			return &apiError{404, "未找到记录"}
		}
		if action == "handling_events" {
			value, err := a.serialize(ctx, resource, at, p, true)
			if err != nil {
				return err
			}
			write(w, 200, Object{"results": value["handling_events"]})
			return nil
		}
		if action == "transfer_options" {
			if !a.canTransferAttempt(ctx, a.Pool, p, at, nil) {
				return &apiError{403, "当前接口人没有部门转派权限"}
			}
			if at["status"] != "dispatched" {
				return bad("当前分配状态不可转派")
			}
			departments, err := a.departmentOptions(ctx, p)
			if err != nil {
				return err
			}
			values := []Object{}
			for _, d := range departments {
				department := Object{"id": d["id"], "parent_id": d["parent"], "level": d["level"]}
				if num(d["id"]) != num(at["current_department_id"]) && a.canTransferAttempt(ctx, a.Pool, p, at, department) {
					values = append(values, d)
				}
			}
			screeners, err := a.screenerOptions(ctx, p, at)
			if err != nil {
				return err
			}
			write(w, 200, Object{"results": values, "screeners": screeners})
			return nil
		}
		switch action {
		case "feedback":
			err = onlyFields(body, "result", "reason_code", "note")
		case "transfer":
			err = onlyFields(body, "target_department_id", "target_screener_id", "note")
		case "transfer_to_manual":
			err = onlyFields(body, "target_department_id", "manual_reason")
		}
		if err != nil {
			return err
		}
		at, err = a.mutateAttempt(ctx, id, action, body, p, nil)
		if err != nil {
			return err
		}
		value, err := a.serialize(ctx, resource, at, p, true)
		if err != nil {
			return err
		}
		if action == "dispatch_welink" {
			write(w, 200, Object{"detail": "已下发至部门", "attempt": value})
		} else {
			write(w, 200, value)
		}
		return nil
	}
	if action == "feedback_reasons" {
		field, _ := fieldFor(a, "core_assignmentattempt", "feedback_reason_code")
		values := []Object{}
		for _, choice := range field.Choices {
			values = append(values, Object{"value": choice[0], "label": choice[1]})
		}
		write(w, 200, Object{"results": values})
		return nil
	}
	if action == "retry" {
		decision, err := a.get(ctx, a.Pool, "core_agentdispatchdecision", id)
		if err != nil {
			return err
		}
		member, memberErr := one(ctx, a.Pool, "SELECT row_to_json(m) FROM platform_pool_memberships m JOIN core_candidateworkflow w ON w.id=m.workflow_id WHERE m.decision_id=$1 AND m.status='needs_reanalysis' AND w.current_resume_id=m.resume_id ORDER BY m.id DESC LIMIT 1", id)
		if memberErr != nil && !noPoolRecord(memberErr) {
			return memberErr
		}
		if member == nil && str(decision["error_code"]) == "" && decision["recommendation"] != "archive" && (decision["confidence_score"] == nil || floatValue(decision["confidence_score"]) >= floatValue(a.configValue(ctx, "ai_dispatch_threshold", .75))) {
			return bad("仅失败、建议归档或低于自动下发阈值的 AI 决策可以重试")
		}
		workflow, err := a.get(ctx, a.Pool, "core_candidateworkflow", decision["workflow_id"])
		if err != nil {
			return err
		}
		tx, err := a.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		run, err := a.createRun(ctx, tx, "step2", Object{"source": "ai_retry", "retry_decision_id": id, "retry_resume_id": decision["resume_id"]}, []int64{num(workflow["candidate_id"])}, p)
		if err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		a.wakeQueue(ctx)
		value, err := a.serialize(ctx, "pipeline/runs", run, p, true)
		if err != nil {
			return err
		}
		write(w, 202, Object{"detail": "已创建 AI 重试任务，可在处理任务中心查看进度", "run": value})
		return nil
	}
	if strings.HasPrefix(action, "bulk_") {
		return a.bulkAction(w, r, resource, action, body, p)
	}
	if action == "filter_options" {
		return a.filterOptions(w, r, resource, p)
	}
	if action == "export_fields" {
		write(w, 200, a.Spec.ExportFields)
		return nil
	}
	if action == "export_resumes" || action == "export_jobs" || action == "result_report" {
		return a.export(w, r, resource, action, p)
	}
	return &apiError{404, "未找到操作"}
}
func (a *App) preview(w http.ResponseWriter, r *http.Request, id any) error {
	resume, err := a.get(r.Context(), a.Pool, "core_resume", id)
	if err != nil {
		return err
	}
	path, err := a.resumeFile(str(resume["resume_file"]))
	if err != nil {
		return &apiError{404, "简历文件不存在"}
	}
	file, err := os.Open(path)
	if err != nil {
		return &apiError{404, "简历文件不存在"}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(filepath.Base(path)))
	w.Header().Set("X-Resume-Filename", url.PathEscape(filepath.Base(path)))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
	return nil
}
func (a *App) materials(w http.ResponseWriter, r *http.Request, run Object, action string) error {
	if action == "material_text" {
		id, err := strconv.ParseInt(r.URL.Query().Get("item_id"), 10, 64)
		if err != nil || id <= 0 {
			return bad("材料引用无效")
		}
		row, err := one(r.Context(), a.Pool, "SELECT row_to_json(t) FROM core_resumetextextraction t JOIN core_processingrunscopeitem i ON i.text_extraction_id=t.id WHERE i.run_id=$1 AND i.id=$2", run["id"], id)
		if err != nil {
			return err
		}
		write(w, 200, row["payload"])
		return nil
	}
	items, err := rows(r.Context(), a.Pool, "SELECT row_to_json(i) FROM (SELECT i.*,c.name candidate_name,r.apply_id FROM core_processingrunscopeitem i JOIN core_candidate c ON c.id=i.candidate_id LEFT JOIN core_resume r ON r.id=i.prepared_resume_id WHERE i.run_id=$1 ORDER BY i.candidate_id) i", run["id"])
	if err != nil {
		return err
	}
	values := []Object{}
	nodes := []string{"extracting", "validating_text", "analyzing", "saving"}
	labels := map[string]string{"pending": "等待准入与岗位检查", "extracting": "提取文字", "validating_text": "校验文本", "analyzing": "Kernel 分析", "saving": "保存结果", "done": "已完成", "needs_attention": "材料或分析需处理", "failed": "处理失败", "cancelled": "已取消"}
	for _, item := range items {
		node := str(item["processing_node"])
		next := []string{}
		if !terminalItem(str(item["status"])) && activeRun(str(run["status"])) {
			following := node == "pending"
			for _, n := range nodes {
				if following {
					next = append(next, labels[n])
				}
				if n == node {
					following = true
				}
			}
		}
		message := str(item["error_message"])
		if message == "" {
			message = str(item["result_message"])
		}
		values = append(values, Object{"id": item["id"], "candidate_name": item["candidate_name"], "resume_id": item["prepared_resume_id"], "apply_id": str(item["apply_id"]), "status": item["status"], "node": node, "node_label": labels[node], "next_nodes": next, "message": message, "error_code": item["error_code"], "has_text": item["text_extraction_id"] != nil})
	}
	return paginate(w, r, values)
}
func filterOption(value any, label string) Object {
	full, initials := pinyinNames(label)
	return Object{"value": value, "label": label, "search_text": strings.ToLower(label) + " " + full + " " + initials}
}
func (a *App) filterOptions(w http.ResponseWriter, r *http.Request, resource string, p *Principal) error {
	clean := r.Clone(r.Context())
	u := *r.URL
	clean.URL = &u
	u.RawQuery = ""
	values, err := a.filtered(r.Context(), resource, clean, p)
	if err != nil {
		return err
	}
	fields := map[string][]string{"candidates": {"highest_major", "current_rank", "current_entity", "current_position_name", "job_department_name", "current_job_category", "school_tag", "current_department", "current_primary_department"}, "jobs": {"entity", "public_name", "position_name", "category", "job_family", "primary_department_name", "secondary_department_name", "department_name", "location", "education"}, "schools": {"platform", "school_tag"}, "contacts": {"department"}}[resource]
	result := Object{}
	for _, field := range fields {
		options := map[string]Object{}
		for _, v := range values {
			value := v[field]
			label := str(value)
			if strings.HasPrefix(field, "current_") {
				key := strings.TrimPrefix(field, "current_")
				if x, ok := obj(v["current_resume"])[key]; ok {
					value = x
					label = str(x)
				}
			}
			if strings.HasSuffix(field, "department") || field == "department" {
				value = v[field+"_id"]
				if value == nil {
					value = v[field]
				}
				label = str(v[field+"_name"])
			}
			if field == "school_tag" && resource == "candidates" {
				for _, t := range list(v["school_tags"]) {
					name := str(obj(t)["name"])
					options[name] = filterOption(name, name)
				}
				continue
			}
			if field == "school_tag" && resource == "schools" {
				continue
			}
			if value != nil && strings.TrimSpace(label) != "" {
				if !strings.HasSuffix(field, "department") {
					value = str(value)
				}
				options[str(value)] = filterOption(value, label)
			}
		}
		if resource == "schools" && field == "school_tag" {
			tags, err := rows(r.Context(), a.Pool, "SELECT row_to_json(t) FROM core_schooltag t WHERE is_active ORDER BY code,id")
			if err != nil {
				return err
			}
			for _, tag := range tags {
				options[str(tag["id"])] = filterOption(tag["id"], str(tag["name"]))
			}
		}
		sorted := []Object{}
		for _, v := range options {
			sorted = append(sorted, v)
		}
		sort.Slice(sorted, func(i, j int) bool {
			if field == "current_rank" {
				return num(sorted[i]["value"]) < num(sorted[j]["value"])
			}
			return str(sorted[i]["label"]) < str(sorted[j]["label"])
		})
		result[field] = sorted
	}
	write(w, 200, result)
	return nil
}
func (a *App) bulkAction(w http.ResponseWriter, r *http.Request, resource, action string, body Object, p *Principal) error {
	ctx := r.Context()
	if action == "bulk_delete" {
		if err := onlyFields(body, "candidate_ids"); err != nil {
			return err
		}
		ids, err := positiveIDs(body["candidate_ids"])
		if err != nil || len(ids) > 500 {
			return bad("candidate_ids 必须是最多 500 项的非空正整数数组")
		}
		results := []Object{}
		deleted := 0
		for _, id := range ids {
			err := a.deleteCandidateTransaction(ctx, id)
			if err != nil {
				results = append(results, Object{"candidate_id": id, "status": "failed", "detail": publicMessage(err)})
				continue
			}
			deleted++
			results = append(results, Object{"candidate_id": id, "status": "deleted"})
		}
		write(w, 200, Object{"total": len(ids), "deleted": deleted, "failed": len(ids) - deleted, "results": results})
		return nil
	}
	filterReq := r.Clone(ctx)
	u := *r.URL
	filterReq.URL = &u
	q := u.Query()
	if resource == "candidates" {
		_, hasIDs := body["candidate_ids"]
		_, hasFilters := body["candidate_filters"]
		if hasIDs == hasFilters {
			return bad("必须提供 candidate_ids 或 candidate_filters 中的一项")
		}
		if hasIDs {
			ids, err := positiveIDs(body["candidate_ids"])
			if err != nil {
				return err
			}
			text := []string{}
			for _, id := range ids {
				text = append(text, str(id))
			}
			q.Set("ids", strings.Join(text, ","))
		} else {
			filters, ok := body["candidate_filters"].(map[string]any)
			if !ok || len(filters) == 0 {
				return bad("candidate_filters 必须是非空对象")
			}
			if err := validateBulkFilters(filters); err != nil {
				return err
			}
			for key, value := range filters {
				if _, ok := value.([]any); ok {
					q.Set(key, strings.Join(stringValues(value), ","))
				} else {
					q.Set(key, str(value))
				}
			}
		}
	} else if ids := body["ids"]; ids != nil {
		if _, ok := ids.(string); ok {
			q.Set("ids", str(ids))
		} else {
			q.Set("ids", strings.Join(stringValues(ids), ","))
		}
	}
	u.RawQuery = q.Encode()
	values, err := a.filtered(ctx, resource, filterReq, p)
	if err != nil {
		return err
	}
	batch := token(16)
	if action == "bulk_transfer" {
		if !canTransfer(p) {
			return &apiError{403, "无部门转派权限"}
		}
		if body["target_screener_id"] == nil {
			target, err := a.get(ctx, a.Pool, "core_department", body["target_department_id"])
			if err != nil || !isJobDepartment(target["level"]) {
				return bad("批量转派目标必须是有效一级或二级部门")
			}
		} else if body["target_department_id"] != nil {
			return bad("一次只能选择部门或简历筛选人")
		}
	}
	done, eligible := 0, 0
	errorsList, results := []Object{}, []Object{}
	for _, value := range values {
		at := value
		if resource == "candidates" {
			at = obj(value["current_attempt"])
		}
		required := "pending_dispatch"
		verb := "dispatched"
		mutation := "dispatch_welink"
		if action == "bulk_transfer" {
			required = "dispatched"
			verb = "transferred"
			mutation = "transfer"
		}
		if at["status"] != required {
			results = append(results, Object{"candidate_id": value["id"], "attempt_id": at["id"], "status": "skipped", "detail": "当前简历不可执行该操作"})
			continue
		}
		eligible++
		_, err := a.mutateAttempt(ctx, at["id"], mutation, body, p, batch)
		if err != nil {
			errorsList = append(errorsList, Object{"candidate_id": value["id"], "id": at["id"], "detail": publicMessage(err)})
			continue
		}
		done++
		results = append(results, Object{"candidate_id": value["id"], "attempt_id": at["id"], "status": verb})
	}
	payload := Object{"total": len(values), "eligible": eligible, "failed": len(errorsList), "skipped": len(values) - eligible, "errors": errorsList}
	if action == "bulk_transfer" {
		payload["batch_operation_id"] = batch
		payload["transferred"] = done
		payload["results"] = results
	} else {
		payload["dispatched"] = done
		payload["detail"] = fmt.Sprintf("已下发 %d 条，跳过 %d 条，失败 %d 条", done, len(values)-eligible, len(errorsList))
	}
	write(w, 200, payload)
	return nil
}
func publicMessage(err error) string {
	var e *apiError
	if errors.As(err, &e) {
		return str(e.Detail)
	}
	var t *taskFailure
	if errors.As(err, &t) {
		return t.Message
	}
	return "操作失败，请重试"
}
func (a *App) deleteCandidateTransaction(ctx context.Context, id any) error {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = a.lockWorkflow(ctx, tx, id); err != nil {
		return err
	}
	if err = a.deleteCandidate(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
