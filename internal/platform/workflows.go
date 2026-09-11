package platform

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (a *App) touchWorkflow(ctx context.Context, db DB, w, resume Object) error {
	if err := a.closePoolMemberships(ctx, db, w["id"], resume["id"], "volunteer_changed"); err != nil {
		return err
	}
	values := Object{"status": "in_progress", "current_resume_id": resume["id"], "current_rank": resume["volunteer_rank"], "dispatch_strategy": "ai", "archive_reason": "", "archive_detail": "", "block_reason": "", "block_detail": "", "completed_at": nil}
	if w["started_at"] == nil {
		values["started_at"] = time.Now().UTC()
	}
	_, err := a.save(ctx, db, "core_candidateworkflow", w["id"], values)
	return err
}
func (a *App) archiveWorkflow(ctx context.Context, db DB, w Object, reason, detail string) error {
	if err := a.closePoolMemberships(ctx, db, w["id"], nil, reason); err != nil {
		return err
	}
	_, err := a.save(ctx, db, "core_candidateworkflow", w["id"], Object{"status": "archived", "archive_reason": reason, "archive_detail": detail, "block_reason": "", "block_detail": "", "completed_at": time.Now().UTC()})
	return err
}
func (a *App) invalidateWorkflow(ctx context.Context, db DB, w Object) (Object, error) {
	return a.save(ctx, db, "core_candidateworkflow", w["id"], Object{"active_processing_scope_item_id": nil, "active_processing_token": nil, "active_processing_expires_at": nil, "revision": num(w["revision"]) + 1})
}
func (a *App) releaseCapacity(ctx context.Context, db DB, at Object) error {
	if at["capacity_reservation_id"] == nil || at["capacity_released_at"] != nil {
		return nil
	}
	if _, err := db.Exec(ctx, "UPDATE core_processingrunjobcapacity SET used_count=GREATEST(0,used_count-1) WHERE id=$1", at["capacity_reservation_id"]); err != nil {
		return err
	}
	_, err := a.save(ctx, db, "core_assignmentattempt", at["id"], Object{"capacity_released_at": time.Now().UTC()})
	return err
}
func (a *App) cancelOpenAttempts(ctx context.Context, db DB, w Object, reason string, sources []string) error {
	attempts, err := rows(ctx, db, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE workflow_id=$1 AND status IN ('pending_review','pending_dispatch','dispatched') ORDER BY id FOR UPDATE", w["id"])
	if err != nil {
		return err
	}
	for _, at := range attempts {
		if len(sources) > 0 && !contains(sources, str(at["source"])) {
			continue
		}
		if err = a.releaseCapacity(ctx, db, at); err != nil {
			return err
		}
		if _, err = a.save(ctx, db, "core_assignmentattempt", at["id"], Object{"status": "cancelled", "cancel_reason": reason, "cancelled_at": time.Now().UTC()}); err != nil {
			return err
		}
		if err = a.event(ctx, db, at, "cancelled", at["current_department_id"], nil, reason, nil, nil, Object{"cancel_reason": reason}); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) receivingDepartment(ctx context.Context, db DB, target Object) (Object, error) {
	if isJobDepartment(target["level"]) {
		return target, nil
	}
	return nil, bad("目标部门必须是有效一级或二级部门")
}
func (a *App) event(ctx context.Context, db DB, at Object, event string, from, to any, note string, p *Principal, batch any, metadata Object, automatic ...bool) error {
	values := Object{"attempt_id": at["id"], "event_type": event, "from_department_id": from, "to_department_id": to, "note": note, "batch_operation_id": batch, "is_system_auto": p == nil, "metadata": metadata, "occurred_at": time.Now().UTC()}
	if len(automatic) > 0 {
		values["is_system_auto"] = automatic[0]
	}
	if metadata == nil {
		values["metadata"] = Object{}
	}
	for _, key := range []string{"from", "to"} {
		dep, _ := a.get(ctx, db, "core_department", values[key+"_department_id"])
		values[key+"_department_name_snapshot"] = str(dep["name"])
	}
	if p != nil {
		values["actor_id"] = p.User["id"]
		values["actor_username_snapshot"] = p.User["username"]
	}
	_, err := a.save(ctx, db, "core_assignmenthandlingevent", nil, values)
	return err
}
func (a *App) notificationMetadata(ctx context.Context, db DB, departmentID any) Object {
	enabled := truth(a.configValue(ctx, "welink_enabled", false))
	contacts, _ := rows(ctx, db, "SELECT row_to_json(c) FROM core_contact c WHERE department_id=$1 AND contact_level='secondary' AND is_active ORDER BY id", departmentID)
	ids, employees := []any{}, []any{}
	for _, c := range contacts {
		ids = append(ids, c["id"])
		employees = append(employees, c["employee_no"])
	}
	status, reason := "skipped", "welink_disabled"
	if enabled {
		reason = "no_active_recipient"
		if len(contacts) > 0 {
			status = "stubbed"
			reason = "welink_sender_not_configured"
		}
	}
	return Object{"welink": Object{"enabled": enabled, "recipient_count": len(ids), "recipient_ids": ids, "recipient_employee_nos": employees, "delivery_status": status, "skipped_reason": reason, "error": ""}}
}
func (a *App) createAttempt(ctx context.Context, db DB, w, resume, target, values Object, p *Principal) (Object, error) {
	receiver, err := a.receivingDepartment(ctx, db, target)
	if err != nil {
		return nil, err
	}
	var attemptNo int64
	if err = db.QueryRow(ctx, "SELECT COALESCE(max(attempt_no),0)+1 FROM core_assignmentattempt WHERE workflow_id=$1", w["id"]).Scan(&attemptNo); err != nil {
		return nil, err
	}
	values = clone(values)
	values["workflow_id"] = w["id"]
	values["resume_id"] = resume["id"]
	values["attempt_no"] = attemptNo
	values["initial_department_id"] = receiver["id"]
	values["current_department_id"] = target["id"]
	values["initial_department_name_snapshot"] = receiver["name"]
	values["current_department_name_snapshot"] = target["name"]
	values["resume_apply_id_snapshot"] = resume["apply_id"]
	values["position_name_snapshot"] = resume["position_name"]
	if p != nil {
		values["created_by_id"] = p.User["id"]
		values["created_by_username_snapshot"] = p.User["username"]
	}
	at, err := a.save(ctx, db, "core_assignmentattempt", nil, values)
	if err != nil {
		return nil, err
	}
	if err = a.event(ctx, db, at, "attempt_created", nil, nil, str(values["match_reason"]), p, nil, Object{"source": at["source"], "initial_department_id": receiver["id"]}, at["source"] != "manual"); err != nil {
		return nil, err
	}
	if err = a.touchWorkflow(ctx, db, w, resume); err != nil {
		return nil, err
	}
	if _, err = a.save(ctx, db, "core_candidateworkflow", w["id"], Object{"dispatch_strategy": at["match_mode"]}); err != nil {
		return nil, err
	}
	return at, nil
}
func (a *App) manualAssign(ctx context.Context, resumeID, targetID any, reason string, p *Principal) (Object, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	resume, err := a.get(ctx, tx, "core_resume", resumeID)
	if err != nil {
		return nil, err
	}
	w, err := a.lockWorkflow(ctx, tx, resume["candidate_id"])
	if err != nil {
		return nil, err
	}
	if w["status"] == "passed" {
		return nil, &apiError{409, "已通过候选人不可再强制分配"}
	}
	target, err := a.get(ctx, tx, "core_department", targetID)
	if err != nil {
		return nil, err
	}
	if _, err = a.receivingDepartment(ctx, tx, target); err != nil {
		return nil, err
	}
	w, err = a.invalidateWorkflow(ctx, tx, w)
	if err != nil {
		return nil, err
	}
	if err = a.cancelOpenAttempts(ctx, tx, w, "manual_replaced", nil); err != nil {
		return nil, err
	}
	if err := a.closePoolMemberships(ctx, tx, w["id"], nil, "manual_replaced"); err != nil {
		return nil, err
	}
	at, err := a.createAttempt(ctx, tx, w, resume, target, Object{"source": "manual", "match_mode": "manual", "match_reason": "HR 手动强制分配", "manual_reason": reason}, p)
	if err != nil {
		return nil, err
	}
	return at, tx.Commit(ctx)
}
func canTransfer(p *Principal) bool {
	if p.has("attempt.view_all") && p.has("attempt.transfer_department") {
		return true
	}
	for _, grant := range p.departmentGrants() {
		if grantCanDelegate(p, grant) {
			return true
		}
	}
	return false
}
func (a *App) departmentOptions(ctx context.Context, p *Principal) ([]Object, error) {
	departments, err := rows(ctx, a.Pool, "SELECT row_to_json(d) FROM core_department d WHERE level IN (1,2) ORDER BY level,name,id")
	if err != nil {
		return nil, err
	}
	values := []Object{}
	for _, d := range departments {
		value, err := a.serialize(ctx, "departments", d, p, false)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}
func (a *App) mutateAttempt(ctx context.Context, id any, action string, body Object, p *Principal, batch any) (Object, error) {
	initial, err := a.get(ctx, a.Pool, "core_assignmentattempt", id)
	if err != nil {
		return nil, err
	}
	if !a.visibleAttempt(ctx, p, initial) {
		return nil, &apiError{404, "未找到记录"}
	}
	if action == "transfer_to_manual" {
		return a.manualAssign(ctx, initial["resume_id"], body["target_department_id"], str(body["manual_reason"]), p)
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	wf, err := a.get(ctx, tx, "core_candidateworkflow", initial["workflow_id"])
	if err != nil {
		return nil, err
	}
	w, err := a.lockWorkflow(ctx, tx, wf["candidate_id"])
	if err != nil {
		return nil, err
	}
	at, err := one(ctx, tx, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return nil, err
	}
	if num(at["current_department_id"]) != num(initial["current_department_id"]) {
		return nil, &apiError{409, "当前接收部门已变更，请刷新后重试"}
	}
	if num(at["assigned_screener_id"]) != num(initial["assigned_screener_id"]) {
		return nil, &apiError{409, "当前简历筛选人已变更，请刷新后重试"}
	}
	if !a.visibleAttempt(ctx, p, at) {
		return nil, &apiError{404, "未找到记录"}
	}
	stateError := func(message string) (Object, error) { return nil, &apiError{409, message} }
	values := Object{}
	eventType := ""
	var from, to any
	note := str(body["note"])
	metadata := Object{}
	switch action {
	case "dispatch_welink":
		if !a.canManageAttempt(ctx, tx, p, at, "attempt.dispatch") {
			return nil, &apiError{403, "无当前部门下发权限"}
		}
		if at["status"] != "pending_dispatch" {
			return stateError("仅待下发尝试可以下发")
		}
		values["status"] = "dispatched"
		values["dispatched_at"] = time.Now().UTC()
		eventType = "department_dispatched"
		to = at["current_department_id"]
		metadata = a.notificationMetadata(ctx, tx, to)
	case "transfer":
		if !a.canTransferAttempt(ctx, tx, p, at, nil) {
			return nil, &apiError{403, "当前接口人没有部门转派权限"}
		}
		if at["status"] != "dispatched" {
			return stateError("仅已下发且未反馈的尝试可以转派")
		}
		if body["target_screener_id"] != nil {
			if body["target_department_id"] != nil {
				return nil, bad("一次只能选择部门或简历筛选人")
			}
			screener, err := one(ctx, tx, "SELECT row_to_json(c) FROM core_contact c WHERE id=$1 FOR SHARE", body["target_screener_id"])
			if err != nil || !a.eligibleScreener(ctx, tx, screener, at) {
				return nil, bad("请选择当前接收部门内已启用并有反馈权限的简历筛选人")
			}
			if num(at["assigned_screener_id"]) == num(screener["id"]) {
				return nil, bad("该简历已转派给此筛选人")
			}
			values["assigned_screener_id"] = screener["id"]
			values["assigned_screener_employee_no_snapshot"] = screener["employee_no"]
			values["assigned_screener_name_snapshot"] = screener["name"]
			values["screener_assigned_at"] = time.Now().UTC()
			eventType = "screener_assigned"
			metadata = Object{"from_screener_id": at["assigned_screener_id"], "to_screener_id": screener["id"], "to_screener_employee_no": screener["employee_no"], "to_screener_name": screener["name"], "department_id": at["current_department_id"]}
			break
		}
		target, err := a.get(ctx, tx, "core_department", body["target_department_id"])
		if err != nil {
			return nil, bad("目标部门不存在")
		}
		if _, err = a.receivingDepartment(ctx, tx, target); err != nil {
			return nil, err
		}
		if !a.canTransferAttempt(ctx, tx, p, at, target) {
			return nil, &apiError{403, "当前部门授权不允许转派到该目标部门"}
		}
		if num(target["id"]) == num(at["current_department_id"]) {
			return nil, bad("目标部门与当前接收部门相同")
		}
		values["assigned_screener_id"] = nil
		values["assigned_screener_employee_no_snapshot"] = ""
		values["assigned_screener_name_snapshot"] = ""
		values["screener_assigned_at"] = nil
		values["current_department_id"] = target["id"]
		values["current_department_name_snapshot"] = target["name"]
		eventType = "department_transferred"
		from = at["current_department_id"]
		to = target["id"]
		metadata = a.notificationMetadata(ctx, tx, to)
	case "confirm_review":
		if !a.canManageAttempt(ctx, tx, p, at, "attempt.dispatch") {
			return nil, &apiError{403, "无当前部门复核权限"}
		}
		if at["status"] != "pending_review" {
			return stateError("仅待 HR 复核尝试可以确认")
		}
		decision, _ := a.get(ctx, tx, "core_agentdispatchdecision", at["agent_decision_id"])
		if len(obj(decision["kernel_result"])) > 0 {
			item, err := one(ctx, tx, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1 AND candidate_id=$2", decision["processing_run_id"], w["candidate_id"])
			if err != nil {
				return nil, err
			}
			if err = a.validateLiveJobs(ctx, tx, obj(item["kernel_snapshot"])); err != nil {
				return stateError("岗位要求已变化，请重新分析或人工分配")
			}
			if at["capacity_reservation_id"] == nil {
				job, err := a.get(ctx, tx, "core_job", decision["recommended_job_id"])
				if err != nil {
					return nil, err
				}
				run, err := a.get(ctx, tx, "core_processingrun", decision["processing_run_id"])
				if err != nil {
					return nil, err
				}
				capacity, err := one(ctx, tx, "SELECT row_to_json(c) FROM core_processingrunjobcapacity c WHERE run_id=$1 AND job_id=$2 FOR UPDATE", run["id"], job["id"])
				if err != nil {
					return nil, err
				}
				limit := num(job["headcount"]) * num(run["job_hc_coefficient_snapshot"])
				if num(capacity["used_count"]) >= limit {
					return stateError("岗位 HC 已用尽，请选择其他岗位")
				}
				if _, err = a.save(ctx, tx, "core_processingrunjobcapacity", capacity["id"], Object{"capacity": limit, "used_count": num(capacity["used_count"]) + 1}); err != nil {
					return nil, err
				}
				values["capacity_reservation_id"] = capacity["id"]
			}
		}
		values["status"] = "pending_dispatch"
		values["review_required"] = false
		eventType = "review_confirmed"
	case "cancel_attempt", "cancel_review":
		if !a.canManageAttempt(ctx, tx, p, at, "attempt.dispatch") {
			return nil, &apiError{403, "无当前部门复核或下发权限"}
		}
		required := "pending_dispatch"
		if action == "cancel_review" {
			required = "pending_review"
		}
		if at["status"] != required {
			return stateError("当前尝试状态不可取消")
		}
		if err = a.releaseCapacity(ctx, tx, at); err != nil {
			return nil, err
		}
		reason := str(body["reason"])
		if reason == "" {
			reason = "hr_cancelled"
		}
		values["status"] = "cancelled"
		values["cancelled_at"] = time.Now().UTC()
		values["cancel_reason"] = reason
		eventType = "cancelled"
		from = at["current_department_id"]
		note = reason
		metadata["cancel_reason"] = reason
	case "feedback":
		if at["status"] != "dispatched" || at["feedback_at"] != nil {
			return stateError("仅已下发且未反馈的尝试可以反馈")
		}
		if !canFeedbackAttempt(p, at) {
			return nil, &apiError{403, "只有当前接收部门的启用接口人可以提交反馈"}
		}
		result, reason := str(body["result"]), str(body["reason_code"])
		label := ""
		if result != "passed" && result != "rejected" {
			return nil, bad("反馈结果必须是 passed 或 rejected")
		}
		field, _ := fieldFor(a, "core_assignmentattempt", "feedback_reason_code")
		for _, choice := range field.Choices {
			if choice[0] == reason {
				label = str(choice[1])
			}
		}
		if result == "passed" && reason != "" {
			return nil, bad("通过反馈不能填写不通过原因")
		}
		if result == "rejected" && (label == "" || (reason == "other" && strings.TrimSpace(note) == "")) {
			return nil, bad("不通过时必须选择有效原因；选择其他时必须填写备注")
		}
		values["status"] = result
		values["feedback_result"] = result
		values["feedback_note"] = note
		values["feedback_reason_code"] = reason
		values["feedback_reason_label_snapshot"] = label
		values["feedback_at"] = time.Now().UTC()
		eventType = "feedback_" + result
		from = at["current_department_id"]
		if result == "rejected" {
			metadata["reason_code"] = reason
		}
	default:
		return nil, bad("不支持的流程操作")
	}
	w, err = a.invalidateWorkflow(ctx, tx, w)
	if err != nil {
		return nil, err
	}
	at, err = a.save(ctx, tx, "core_assignmentattempt", id, values)
	if err != nil {
		return nil, err
	}
	if err = a.event(ctx, tx, at, eventType, from, to, note, p, batch, metadata); err != nil {
		return nil, err
	}
	if action == "feedback" {
		if at["status"] == "passed" {
			if _, err = a.save(ctx, tx, "core_candidateworkflow", w["id"], Object{"status": "passed", "passed_attempt_id": id, "block_reason": "", "block_detail": "", "completed_at": time.Now().UTC()}); err != nil {
				return nil, err
			}
			if err = a.cancelOpenAttempts(ctx, tx, w, "workflow_passed", nil); err != nil {
				return nil, err
			}
		} else {
			resume, findErr := one(ctx, tx, "SELECT row_to_json(r) FROM core_resume r WHERE candidate_id=$1 AND NOT EXISTS(SELECT 1 FROM core_assignmentattempt a WHERE a.workflow_id=$2 AND a.resume_id=r.id AND a.feedback_result='rejected') ORDER BY volunteer_rank NULLS LAST,apply_date NULLS LAST,id LIMIT 1", w["candidate_id"], w["id"])
			if findErr != nil {
				var missing *apiError
				if !errors.As(findErr, &missing) || missing.Status != 404 {
					return nil, findErr
				}
				if err = a.archiveWorkflow(ctx, tx, w, "all_rejected", "全部可尝试志愿均已反馈未通过"); err != nil {
					return nil, err
				}
			} else {
				if err = a.touchWorkflow(ctx, tx, w, resume); err != nil {
					return nil, err
				}
				scope := Object{"trigger": "feedback_rejected", "retry_resume_id": resume["id"], "expected_workflow_revision": w["revision"]}
				if _, err = a.createRun(ctx, tx, "step2", scope, []int64{num(w["candidate_id"])}, p); err != nil {
					return nil, err
				}
			}
		}
	}
	if action == "cancel_attempt" || action == "cancel_review" {
		var open bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_assignmentattempt WHERE workflow_id=$1 AND status IN ('pending_review','pending_dispatch','dispatched'))", w["id"]).Scan(&open); err != nil {
			return nil, err
		}
		if !open {
			reason := "hr_cancelled"
			if at["source"] == "ai" {
				reason = "agent_no_recommendation"
			}
			if err = a.archiveWorkflow(ctx, tx, w, reason, "HR 已取消当前建议，可重试 Agent 或手动分配"); err != nil {
				return nil, err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	if action == "feedback" {
		a.wakeQueue(ctx)
	}
	return at, nil
}
