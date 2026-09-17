package platform

import (
	"context"
	"sort"
	"strings"
	"time"
)

func (a *App) source(ctx context.Context, table string, row Object, path string) any {
	parts := strings.Split(path, ".")
	if len(parts) == 0 || row == nil {
		return nil
	}
	field, ok := fieldFor(a, table, parts[0])
	if !ok {
		return row[parts[0]]
	}
	value := row[field.Column]
	if len(parts) == 1 {
		return value
	}
	if field.Relation == "" || value == nil {
		return nil
	}
	related, err := a.get(ctx, a.Pool, field.Relation, value)
	if err != nil {
		return nil
	}
	return a.source(ctx, field.Relation, related, strings.Join(parts[1:], "."))
}
func (a *App) hierarchy(ctx context.Context, department Object) (Object, Object, Object) {
	return a.hierarchyWithDB(ctx, a.Pool, department)
}
func (a *App) hierarchyWithDB(ctx context.Context, db DB, department Object) (Object, Object, Object) {
	var primary, secondary, tertiary Object
	visited := map[int64]bool{}
	for department != nil && !visited[num(department["id"])] {
		visited[num(department["id"])] = true
		switch num(department["level"]) {
		case 1:
			primary = department
		case 2:
			secondary = department
		case 3:
			tertiary = department
		}
		if department["parent_id"] == nil {
			break
		}
		department, _ = a.get(ctx, db, "core_department", department["parent_id"])
	}
	return primary, secondary, tertiary
}
func brief(row Object, keys ...string) Object {
	if row == nil {
		return nil
	}
	result := Object{}
	for _, key := range keys {
		result[key] = row[key]
	}
	return result
}
func resumeBrief(row Object) Object {
	return brief(row, "id", "apply_id", "entity", "org", "position_name", "status", "apply_date", "volunteer_rank", "assigned_entity", "job_category", "category_mode", "category_reason", "resume_file", "lifecycle_status", "lifecycle_status_label", "lifecycle_reason", "lifecycle_completed_at")
}
func (a *App) serialize(ctx context.Context, resource string, row Object, p *Principal, detail bool) (Object, error) {
	if row == nil {
		return nil, nil
	}
	spec := a.Spec.Resources[resource]
	result := Object{}
	for name, field := range spec.Fields {
		if name == "permission_codes" || name == "major_names" || name == "first_degree_tag_ids" || name == "highest_degree_tag_ids" {
			continue
		}
		if field.Source == "*" {
			result[name] = nil
			continue
		}
		value := a.source(ctx, spec.Table, row, field.Source)
		if value == nil && (field.Type == "CharField" || field.Type == "SerializerMethodField") {
			value = ""
		}
		result[name] = value
	}
	switch resource {
	case "users":
		other, err := a.userPrincipal(ctx, row)
		if err != nil {
			return nil, err
		}
		result["roles"] = other.Roles
		result["department_grants"] = []any{}
		for _, grant := range other.Grants {
			_, label := contactRole(str(grant.Contact["contact_level"]))
			result["department_grants"] = append(list(result["department_grants"]), Object{"id": grant.Contact["id"], "department_name": grant.Department["name"], "role_name": label, "is_active": grant.Contact["is_active"]})
		}
		codes := []string{}
		for c := range other.Permissions {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		result["permissions"] = codes
		result["is_protected"] = str(row["username"]) == "012358"
		groups, err := rows(ctx, a.Pool, "SELECT row_to_json(g) FROM accounts_user_groups g WHERE user_id=$1 ORDER BY group_id", row["id"])
		if err != nil {
			return nil, err
		}
		ids := []any{}
		for _, g := range groups {
			ids = append(ids, g["group_id"])
		}
		result["role_ids"] = ids
	case "roles":
		_, result["is_builtin"] = a.Spec.RolePermissions[str(row["name"])]
		allowed := []string{}
		for _, module := range a.Spec.PermissionTree {
			for _, value := range list(module["children"]) {
				code := str(obj(value)["code"])
				if a.rolePermissionAllowed(str(row["name"]), code) {
					allowed = append(allowed, code)
				}
			}
		}
		result["allowed_permission_codes"] = allowed
		permissions, err := rows(ctx, a.Pool, "SELECT row_to_json(p) FROM auth_permission p JOIN auth_group_permissions gp ON gp.permission_id=p.id WHERE gp.group_id=$1 ORDER BY p.codename", row["id"])
		if err != nil {
			return nil, err
		}
		codes := []string{}
		for _, permission := range permissions {
			code := strings.ReplaceAll(str(permission["codename"]), "__", ".")
			if a.rolePermissionAllowed(str(row["name"]), code) {
				codes = append(codes, code)
			}
		}
		result["permissions"] = codes
	case "departments", "jobs":
		department := row
		if resource == "jobs" {
			setting, e := one(ctx, a.Pool, `SELECT row_to_json(s) FROM platform_demand_settings s WHERE demand_id=$1`, row["id"])
			if e == nil {
				result["reception_state"] = setting["reception_state"]
				result["reception_revision"] = setting["revision"]
			} else {
				result["reception_state"] = "paused"
				result["reception_revision"] = int64(1)
			}
			department, _ = a.get(ctx, a.Pool, "core_department", row["department_id"])
		}
		primary, secondary, _ := a.hierarchy(ctx, department)
		result["primary_department_id"] = primary["id"]
		result["primary_department_name"] = str(primary["name"])
		if resource == "jobs" {
			result["secondary_department_id"] = secondary["id"]
			result["secondary_department_name"] = str(secondary["name"])
			result["department_name"] = str(department["name"])
			majors, err := rows(ctx, a.Pool, "SELECT row_to_json(m) FROM core_jobmajor m WHERE job_id=$1 ORDER BY id", row["id"])
			if err != nil {
				return nil, err
			}
			values := []any{}
			for _, m := range majors {
				values = append(values, m["major"])
			}
			result["majors"] = values
		}
	case "school-tag-rules":
		for _, degree := range []string{"first", "highest"} {
			tags, err := rows(ctx, a.Pool, "SELECT row_to_json(t) FROM core_schooltag t JOIN core_schooltagruletag l ON l.school_tag_id=t.id WHERE l.rule_id=$1 AND l.degree_type=$2 ORDER BY t.code,t.id", row["id"], degree)
			if err != nil {
				return nil, err
			}
			values := []any{}
			for _, t := range tags {
				values = append(values, brief(t, "id", "code", "name"))
			}
			result[degree+"_degree_tags"] = values
		}
		educations, err := rows(ctx, a.Pool, "SELECT row_to_json(e) FROM core_schooltagruleeducation e WHERE rule_id=$1 ORDER BY id", row["id"])
		if err != nil {
			return nil, err
		}
		values := []any{}
		for _, e := range educations {
			values = append(values, e["education"])
		}
		result["allowed_highest_educations"] = values
	case "resumes":
		candidate, err := a.get(ctx, a.Pool, "core_candidate", row["candidate_id"])
		if err != nil {
			return nil, err
		}
		tags, err := a.candidateTags(ctx, candidate)
		if err != nil {
			return nil, err
		}
		names := []string{}
		for _, tag := range tags {
			names = append(names, str(tag["name"]))
		}
		result["school_tags"] = names
		result["school_tag"] = strings.Join(names, "、")
	case "candidates":
		return a.candidateJSON(ctx, row, result, p, detail)
	case "workflows":
		resume, _ := a.get(ctx, a.Pool, "core_resume", row["current_resume_id"])
		result["current_resume"] = row["current_resume_id"]
		result["current_apply_id"] = str(resume["apply_id"])
		result["current_position_name"] = str(resume["position_name"])
		result["current_rank"] = row["current_rank"]
	case "workflow-attempts":
		result["can_export"] = a.canExportAttempt(ctx, p, row)
		result["can_dispatch"] = a.canManageAttempt(ctx, a.Pool, p, row, "attempt.dispatch")
		result["assigned_screener"] = row["assigned_screener_id"]
		result["assigned_screener_name"] = row["assigned_screener_name_snapshot"]
		result["assigned_screener_employee_no"] = row["assigned_screener_employee_no_snapshot"]
		result["screener_assigned_at"] = row["screener_assigned_at"]
		pending := row["status"] == "dispatched" && row["feedback_at"] == nil
		result["can_transfer"] = pending && a.canTransferAttempt(ctx, a.Pool, p, row, nil)
		result["can_feedback"] = pending && canFeedbackAttempt(p, row)
		initial, _ := a.get(ctx, a.Pool, "core_department", row["initial_department_id"])
		current, _ := a.get(ctx, a.Pool, "core_department", row["current_department_id"])
		primary, _, _ := a.hierarchy(ctx, current)
		result["initial_department_name"] = str(initial["name"])
		result["current_department_name"] = str(current["name"])
		result["primary_department_id"] = primary["id"]
		result["primary_department_name"] = str(primary["name"])
		result["match_reason"] = str(row["match_reason"])
		decision, _ := a.get(ctx, a.Pool, "core_agentdispatchdecision", row["agent_decision_id"])
		result["agent_decision"] = nil
		result["agent_decision_summary"] = nil
		if decision != nil && (p.has("attempt.view_all") || truth(result["can_dispatch"])) {
			result["agent_decision"] = decision["id"]
			summary := brief(decision, "id", "recommendation", "matched_job_category", "confidence_score", "score_breakdown", "summary", "reason", "evidence", "risks", "risk_flags", "error_code", "error_message")
			summary["recommended_job"] = decision["recommended_job_id"]
			result["agent_decision_summary"] = summary
		}
		events, err := rows(ctx, a.Pool, "SELECT row_to_json(e) FROM core_assignmenthandlingevent e WHERE attempt_id=$1 ORDER BY occurred_at,id", row["id"])
		if err != nil {
			return nil, err
		}
		values := []Object{}
		var previous any
		for _, e := range events {
			value := a.handlingEventJSON(ctx, e, p)
			value["duration_since_previous_seconds"] = elapsed(previous, e["occurred_at"])
			previous = e["occurred_at"]
			values = append(values, value)
		}
		result["handling_events"] = values
		for _, kind := range []string{"initial", "current"} {
			if name := str(row[kind+"_department_name_snapshot"]); name != "" {
				result[kind+"_department_name"] = name
			}
		}

	case "agent-decisions":
		var open bool
		if err := a.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core_candidateworkflow w LEFT JOIN platform_applications a ON a.resume_id=$2 WHERE w.id=$1 AND w.current_resume_id=$2 AND w.status NOT IN ('passed','talent_pool') AND COALESCE(a.status,'pending') NOT IN ('ai_rejected','department_rejected','review_rejected','passed') AND NOT EXISTS(SELECT 1 FROM core_assignmentattempt at WHERE at.workflow_id=w.id AND at.status IN ('pending_review','pending_dispatch','dispatched','passed')))`, row["workflow_id"], row["resume_id"]).Scan(&open); err != nil {
			return nil, err
		}
		member, err := one(ctx, a.Pool, "SELECT row_to_json(m) FROM platform_pool_memberships m WHERE decision_id=$1 ORDER BY id DESC LIMIT 1", row["id"])
		if err != nil && !noPoolRecord(err) {
			return nil, err
		}
		if member != nil {
			result["pool_membership"] = member
			delete(member, "file_checksum")
		}
		result["can_retry"] = open && (str(row["error_code"]) != "" || row["recommendation"] == "archive" || (row["confidence_score"] != nil && floatValue(row["confidence_score"]) < floatValue(a.configValue(ctx, "ai_dispatch_threshold", .75))) || member["status"] == "needs_reanalysis")

		for _, key := range []string{"evaluated_job", "recommended_job"} {
			job, _ := a.get(ctx, a.Pool, "core_job", row[key+"_id"])
			name := str(job["public_name"])
			if name == "" {
				name = str(job["position_name"])
			}
			result[key+"_name"] = name
		}
		result["kernel_result"] = a.analysisPresentation(ctx, row, detail)
	case "pipeline/runs":
		stages, err := rows(ctx, a.Pool, "SELECT row_to_json(s) FROM core_processingrunstage s WHERE run_id=$1 ORDER BY sequence,id", row["id"])
		if err != nil {
			return nil, err
		}
		for _, s := range stages {
			delete(s, "id")
			delete(s, "run_id")
			s["elapsed_seconds"] = elapsed(s["started_at"], s["finished_at"])
			s["description"] = stageDescriptions[str(s["step"])]
		}
		result["stages"] = stages
		result["elapsed_seconds"] = elapsed(row["created_at"], row["finished_at"])
		result["activity"] = nil
		if str(row["current_stage"]) == "step4" && activeRun(str(row["status"])) {
			counts, err := rows(ctx, a.Pool, "SELECT row_to_json(c) FROM (SELECT status,count(*) n FROM core_processingrunscopeitem WHERE run_id=$1 GROUP BY status) c", row["id"])
			if err != nil {
				return nil, err
			}
			activity := Object{"processing": int64(0), "queued": int64(0), "waiting_conflict": int64(0)}
			for _, c := range counts {
				key := str(c["status"])
				if key == "pending" {
					key = "queued"
				}
				if _, ok := activity[key]; ok {
					activity[key] = num(activity[key]) + num(c["n"])
				}
			}
			result["activity"] = activity
		}
	}
	return result, nil
}
func elapsed(start, end any) any {
	if start == nil {
		return nil
	}
	s, err := time.Parse(time.RFC3339Nano, str(start))
	if err != nil {
		return int64(0)
	}
	e := time.Now()
	if end != nil {
		if parsed, err := time.Parse(time.RFC3339Nano, str(end)); err == nil {
			e = parsed
		}
	}
	return max(int64(0), int64(e.Sub(s).Seconds()))
}
func (a *App) candidateTags(ctx context.Context, c Object) ([]Object, error) {
	tags, err := rows(ctx, a.Pool, "SELECT row_to_json(t) FROM core_schooltag t JOIN core_candidate_school_tags l ON l.schooltag_id=t.id WHERE l.candidate_id=$1 ORDER BY t.code,t.id", c["id"])
	if err != nil {
		return nil, err
	}
	if len(tags) > 0 {
		return tags, nil
	}
	seen := map[int64]bool{}
	for _, key := range []string{"first_degree_tag_id", "highest_degree_tag_id"} {
		if id := num(c[key]); id != 0 && !seen[id] {
			tag, err := a.get(ctx, a.Pool, "core_schooltag", id)
			if err != nil {
				return nil, err
			}
			tags = append(tags, tag)
			seen[id] = true
		}
	}
	return tags, nil
}

var systemLabels = map[string]string{"raw": "待处理", "talent_pool": "人才库", "pending_allocation": "入池待分配", "archived": "已归档", "pending_reallocation": "待重新分配", "pending_dispatch": "待下发", "pending_screening": "待业务反馈", "screening_passed": "通过", "screening_rejected": "不通过"}

func (a *App) candidateJSON(ctx context.Context, c, result Object, p *Principal, detail bool) (Object, error) {
	workflow, _ := one(ctx, a.Pool, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1", c["id"])
	resumes, err := rows(ctx, a.Pool, `SELECT row_to_json(v) FROM (SELECT r.*,COALESCE(a.status,'pending') lifecycle_status,COALESCE(a.reason,'') lifecycle_reason,a.completed_at lifecycle_completed_at FROM core_resume r LEFT JOIN platform_applications a ON a.resume_id=r.id WHERE r.candidate_id=$1 ORDER BY volunteer_rank NULLS LAST,apply_date NULLS LAST,r.id) v`, c["id"])
	if err != nil {
		return nil, err
	}
	var current, preview, attempt Object
	if len(resumes) > 0 {
		current = resumes[0]
	}
	for _, r := range resumes {
		r["lifecycle_status_label"] = applicationLabels[str(r["lifecycle_status"])]
		if num(r["id"]) == num(workflow["current_resume_id"]) {
			current = r
		}
	}
	if workflow["status"] == "waiting_next" {
		for _, r := range resumes {
			if !closedApplication(str(r["lifecycle_status"])) {
				current = r
				break
			}
		}
	}
	attempts := []Object{}
	if workflow != nil {
		attempts, err = rows(ctx, a.Pool, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE workflow_id=$1 ORDER BY attempt_no,id", workflow["id"])
		if err != nil {
			return nil, err
		}
	}
	visible := []Object{}
	for _, at := range attempts {
		if p.has("resume.view") || a.visibleAttempt(ctx, p, at) {
			visible = append(visible, at)
			if str(at["status"]) != "cancelled" && (!p.has("resume.view") || num(at["resume_id"]) == num(current["id"])) {
				attempt = at
			}
		}
	}
	if !p.has("resume.view") {
		if attempt == nil {
			return nil, &apiError{404, "未找到记录"}
		}
		for _, resume := range resumes {
			if num(resume["id"]) == num(attempt["resume_id"]) {
				current = resume
				break
			}
		}
		resumes = []Object{current}
		result["phone"] = ""
	} else {
		result["phone"] = c["phone"]
	}
	if str(current["resume_file"]) != "" {
		preview = current
	} else {
		for _, r := range resumes {
			if str(r["resume_file"]) != "" {
				preview = r
				break
			}
		}
	}
	result["current_resume"] = resumeBrief(current)
	result["preview_resume"] = resumeBrief(preview)
	result["current_apply_id"] = str(current["apply_id"])
	result["current_apply_date"] = current["apply_date"]
	result["current_rank"] = current["volunteer_rank"]
	if num(result["current_rank"]) == 0 {
		result["current_rank"] = current["volunteer_rank"]
	}
	result["workflow_id"] = workflow["id"]
	result["workflow_status"] = str(workflow["status"])
	if workflow == nil {
		result["workflow_status"] = "pending"
	}
	if !p.has("resume.view") {
		result["workflow_status"] = "in_progress"
		if attempt["status"] == "passed" {
			result["workflow_status"] = "passed"
		}
	}
	status := "raw"
	switch str(attempt["status"]) {
	case "passed":
		status = "screening_passed"
	case "rejected":
		status = "screening_rejected"
	case "dispatched":
		status = "pending_screening"
	case "pending_review":
		status = "raw"
	case "pending_dispatch":
		status = "pending_dispatch"
	default:
		if str(workflow["block_reason"]) == "job_hc_exhausted" {
			status = "pending_reallocation"
		} else if workflow["started_at"] != nil || str(workflow["archive_reason"]) != "" || len(attempts) > 0 {
			status = "archived"
		}
	}
	if p.has("resume.view") {
		member, err := one(ctx, a.Pool, "SELECT row_to_json(m) FROM platform_pool_memberships m WHERE candidate_id=$1 AND resume_id=$2 AND status IN ('pending_review','pending_allocation','allocated','needs_reanalysis') ORDER BY id DESC LIMIT 1", c["id"], current["id"])
		if err != nil && !noPoolRecord(err) {
			return nil, err
		}
		if member != nil {
			result["pool_membership"] = member
			delete(member, "file_checksum")
			if attempt == nil {
				if member["status"] == "pending_allocation" || member["status"] == "needs_reanalysis" {
					status = "pending_allocation"
				}
			}
		}
	}
	if p.has("resume.view") {
		switch str(workflow["status"]) {
		case "waiting_next":
			status = "raw"
		case "talent_pool":
			status = "talent_pool"
		case "passed":
			status = "screening_passed"
		}
	}
	result["system_status"] = status
	result["system_status_label"] = systemLabels[status]
	tags, err := a.candidateTags(ctx, c)
	if err != nil {
		return nil, err
	}
	tagValues := []any{}
	names := []string{}
	for _, t := range tags {
		tagValues = append(tagValues, brief(t, "id", "code", "name"))
		names = append(names, str(t["name"]))
	}
	result["school_tags"] = tagValues
	result["school_tag"] = strings.Join(names, "、")
	field, _ := fieldFor(a, "core_candidate", "highest_education")
	result["highest_education_label"] = str(c["highest_education"])
	for _, choice := range field.Choices {
		if str(choice[0]) == str(c["highest_education"]) {
			result["highest_education_label"] = choice[1]
		}
	}
	job, _ := a.get(ctx, a.Pool, "core_job", current["job_id"])
	department, _ := a.get(ctx, a.Pool, "core_department", job["department_id"])
	_, secondary, _ := a.hierarchy(ctx, department)
	result["job_department_name"] = str(secondary["name"])
	department, _ = a.get(ctx, a.Pool, "core_department", attempt["current_department_id"])
	primary, _, _ := a.hierarchy(ctx, department)
	result["current_department_id"] = department["id"]
	result["current_department_name"] = str(department["name"])
	result["current_primary_department_id"] = primary["id"]
	result["current_primary_department_name"] = str(primary["name"])
	reasonType, reason := "", ""
	if p.has("resume.view") && workflow["status"] == "waiting_next" {
		reasonType, reason = "waiting_next", "当前志愿已结束，等待下一轮定时任务处理剩余志愿"
	} else if p.has("resume.view") && contains([]string{"archived", "talent_pool"}, str(workflow["status"])) {
		reasonType = "archive"
		if workflow["status"] == "talent_pool" {
			reasonType = "talent_pool"
		}
		reason = str(workflow["archive_detail"])
		if reason == "" {
			reason = str(workflow["archive_reason"])
		}
	} else if p.has("resume.view") && str(workflow["block_reason"]) != "" {
		reasonType = "block"
		reason = str(workflow["block_detail"])
		if reason == "" {
			reason = str(workflow["block_reason"])
		}
	} else if attempt != nil {
		reasonType = "assignment"
		for _, key := range []string{"feedback_reason_label_snapshot", "feedback_note", "manual_reason", "match_reason"} {
			if str(attempt[key]) != "" {
				reason = str(attempt[key])
				break
			}
		}
	} else if str(current["category_reason"]) != "" {
		reasonType = "classification"
		reason = str(current["category_reason"])
	}
	result["reason_type"] = reasonType
	result["reason_text"] = reason
	result["archive_reason"] = ""
	result["archive_detail"] = ""
	result["reason_code"] = ""
	result["processing_result"] = ""
	result["allocation_source"] = str(attempt["source"])
	if p.has("resume.view") {
		result["archive_reason"] = str(workflow["archive_reason"])
		result["archive_detail"] = str(workflow["archive_detail"])
		if attempt == nil && workflow["started_at"] != nil {
			result["allocation_source"] = str(workflow["dispatch_strategy"])
		}
		item, _ := one(ctx, a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE candidate_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1", c["id"])
		result["reason_code"] = str(item["reason_code"])
		result["processing_result"] = str(item["result_type"])
	}
	result["resumes"] = []any{}
	result["attempts"] = []any{}
	result["current_attempt"] = nil
	if detail && p.has("resume.view") {
		history, err := rows(ctx, a.Pool, `SELECT row_to_json(v) FROM (SELECT e.*,r.apply_id,r.volunteer_rank,r.position_name FROM platform_application_events e JOIN core_resume r ON r.id=e.resume_id WHERE r.candidate_id=$1 ORDER BY e.id) v`, c["id"])
		if err != nil {
			return nil, err
		}
		for _, e := range history {
			e["status_label"] = applicationLabels[str(e["to_status"])]
		}
		result["application_history"] = history
	}
	{
		rv := []any{}
		for _, r := range resumes {
			rv = append(rv, resumeBrief(r))
		}
		result["resumes"] = rv
		av := []any{}
		for _, at := range visible {
			v, err := a.serialize(ctx, "workflow-attempts", at, p, true)
			if !p.has("resume.view") && !truth(v["can_dispatch"]) {
				delete(v, "agent_decision")
				delete(v, "agent_decision_summary")
			}
			if err != nil {
				return nil, err
			}
			av = append(av, v)
		}
		result["attempts"] = av
	}
	if attempt != nil {
		result["current_attempt"], err = a.serialize(ctx, "workflow-attempts", attempt, p, detail)
		if !p.has("resume.view") && !truth(obj(result["current_attempt"])["can_dispatch"]) {
			delete(obj(result["current_attempt"]), "agent_decision")
			delete(obj(result["current_attempt"]), "agent_decision_summary")
		}
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (a *App) handlingEventJSON(ctx context.Context, e Object, p *Principal) Object {
	result := brief(e, "id", "event_type", "actor_username_snapshot", "note", "batch_operation_id", "is_system_auto", "metadata", "occurred_at")
	result["actor"] = e["actor_id"]
	result["event_type_label"] = e["event_type"]
	field, _ := fieldFor(a, "core_assignmenthandlingevent", "event_type")
	for _, c := range field.Choices {
		if c[0] == e["event_type"] {
			result["event_type_label"] = c[1]
		}
	}
	for _, kind := range []string{"from", "to"} {
		result[kind+"_department"] = e[kind+"_department_id"]
		name := str(e[kind+"_department_name_snapshot"])
		if name == "" {
			dep, _ := a.get(ctx, a.Pool, "core_department", e[kind+"_department_id"])
			name = str(dep["name"])
		}
		result[kind+"_department_name"] = name
	}
	if !p.has("attempt.view_all") {
		metadata := obj(e["metadata"])
		nested := obj(metadata["welink"])
		target := metadata
		if len(nested) > 0 {
			target = nested
		}
		safe := Object{}
		for _, key := range []string{"enabled", "delivery_status", "recipient_count", "skipped_reason", "error"} {
			if v, ok := target[key]; ok {
				safe[key] = v
			}
		}
		if len(nested) > 0 {
			result["metadata"] = Object{"welink": safe}
		} else {
			result["metadata"] = safe
		}
		if e["event_type"] == "screener_assigned" {
			result["metadata"] = brief(metadata, "to_screener_name", "to_screener_employee_no")
		}
	}
	return result
}
