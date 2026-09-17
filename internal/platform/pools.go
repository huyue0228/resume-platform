package platform

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) closePoolMemberships(ctx context.Context, db DB, workflowID, exceptResume any, reason string) error {
	members, err := rows(ctx, db, "UPDATE platform_pool_memberships m SET status='closed',revision=revision+1,updated_at=now() WHERE workflow_id=$1 AND ($2::bigint IS NULL OR resume_id<>$2) AND status IN ('pending_review','pending_allocation','allocated','needs_reanalysis') RETURNING row_to_json(m)", workflowID, exceptResume)
	if err != nil {
		return err
	}
	for _, m := range members {
		if err = a.poolEvent(ctx, db, m, "closed", Object{"reason": reason}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) savePoolAssessment(ctx context.Context, db DB, run, w, resume, item, frozen, result Object) (string, string, error) {
	d := obj(frozen["preflight"])
	refs := stringValues(d["job_refs"])
	matches := list(result["matches"])
	if len(refs) != 1 || len(matches) != 1 || str(obj(matches[0])["job_ref"]) != refs[0] || num(obj(frozen["volunteer_ids"])[str(d["current_volunteer_ref"])]) != num(resume["id"]) {
		return "", "", taskError("agent_invalid_output", "评估结果与本次投递不一致")
	}
	if _, err := a.save(ctx, db, "core_resume", resume["id"], Object{"job_id": nil, "job_category": "", "category_mode": "", "category_reason": ""}); err != nil {
		return "", "", err
	}
	match := obj(matches[0])
	rec := recommendation(result, match, frozen)
	profile, err := a.persistProfile(ctx, db, resume, result)
	if err != nil {
		return "", "", err
	}
	values := a.auditValues(run)
	pin := obj(result["pin"])
	for k, v := range (Object{"kernel_pin_id": pin["pin_id"], "kernel_build": pin["kernel_build"], "workflow_id": w["id"], "resume_id": resume["id"], "profile_id": profile["id"], "processing_run_id": run["id"], "recommendation": rec, "confidence_score": match["score"], "score_breakdown": match["dimensions"], "summary": match["reason"], "reason": match["reason"], "evidence": match["evidence"], "risks": match["risks"], "risk_flags": obj(result["profile"])["risks"], "kernel_result": result, "safe_trace": result["safe_trace"]}) {
		values[k] = v
	}
	decision, err := a.save(ctx, db, "core_agentdispatchdecision", nil, values)
	if err != nil {
		return "", "", err
	}
	if err = a.closePoolMemberships(ctx, db, w["id"], nil, "reassessed"); err != nil {
		return "", "", err
	}
	if rec == "archive" {
		err = a.applicationState(ctx, db, resume["id"], "ai_rejected", str(match["reason"]), Object{"run_id": run["id"], "decision_id": decision["id"]})
		return "ai_rejected", str(match["reason"]), err
	}
	policy := obj(obj(frozen["snapshot"])["pool_policy"])
	standard := policyItem(policy, "standards", str(d["standard_code"]))
	pool := policyItem(policy, "pools", str(d["pool_code"]))
	status := "pending_allocation"
	if err = a.applicationState(ctx, db, resume["id"], status, str(match["reason"]), Object{"run_id": run["id"], "decision_id": decision["id"]}); err != nil {
		return "", "", err
	}
	assessment := Object{"admission_threshold": obj(frozen["thresholds"])["dispatch"], "standard": standard, "pool": pool, "requirement": standardJob(policy, standard), "tag_catalog": obj(frozen["snapshot"])["tag_catalog"], "candidate": brief(obj(obj(frozen["snapshot"])["candidate"]), "highest_major", "highest_education"), "score": match["score"], "reason": match["reason"], "evidence": match["evidence"], "pin": pin}
	tags := list(obj(result["profile"])["tags"])
	member, err := one(ctx, db, `INSERT INTO platform_pool_memberships(candidate_id,workflow_id,resume_id,decision_id,run_id,standard_code,pool_code,status,tags,assessment,file_checksum) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,$11) RETURNING row_to_json(platform_pool_memberships)`, item["candidate_id"], w["id"], resume["id"], decision["id"], run["id"], d["standard_code"], d["pool_code"], status, string(canonicalJSON(tags, false)), string(canonicalJSON(assessment, false)), obj(result["manifest"])["resume_checksum"])
	if err != nil {
		return "", "", err
	}
	if err = a.poolEvent(ctx, db, member, "assessment_saved", Object{"status": status, "tags": tags, "decision_id": decision["id"]}, nil); err != nil {
		return "", "", err
	}
	w, err = a.get(ctx, db, "core_candidateworkflow", w["id"])
	if err != nil {
		return "", "", err
	}
	if _, err = a.saveScreeningQualification(ctx, db, member, true); err != nil {
		return "", "", err
	}
	return "allocation_queued", "筛选通过，已入池等待独立分配", nil
}

func (a *App) poolMembersAPI(w http.ResponseWriter, r *http.Request, path string, p *Principal) error {
	if !p.has("resume.view") {
		return &apiError{403, "无职位池查看权限"}
	}
	ctx := r.Context()
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 2 && r.Method == "GET" {
		q := r.URL.Query()
		args := []any{}
		where := []string{"true"}
		for _, field := range []string{"pool_code", "status", "candidate_id"} {
			if value := q.Get(field); value != "" {
				args = append(args, value)
				where = append(where, "m."+field+"=$"+strconv.Itoa(len(args)))
			}
		}
		if name := q.Get("pool_name"); name != "" {
			args = append(args, "%"+strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), "%", `\%`), "_", `\_`)+"%")
			where = append(where, "m.assessment->'pool'->>'name' ILIKE $"+strconv.Itoa(len(args)))
		}
		members, err := rows(ctx, a.Pool, "SELECT to_jsonb(m)||jsonb_build_object('candidate_name',c.name,'apply_id',r.apply_id,'position_name',r.position_name) FROM platform_pool_memberships m JOIN core_candidate c ON c.id=m.candidate_id JOIN core_resume r ON r.id=m.resume_id WHERE "+strings.Join(where, " AND ")+" ORDER BY m.created_at,m.id", args...)
		if err != nil {
			return err
		}
		visible := []Object{}
		for _, m := range members {
			if !a.canReadPoolMember(ctx, p, m) {
				continue
			}
			visible = append(visible, m)
			delete(m, "file_checksum")
		}
		return paginate(w, r, visible)
	}
	if len(parts) < 3 {
		return &apiError{404, "未找到记录"}
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return bad("无效入池记录")
	}
	member, err := one(ctx, a.Pool, "SELECT row_to_json(m) FROM platform_pool_memberships m WHERE id=$1", id)
	if err != nil {
		return err
	}
	if !a.canReadPoolMember(ctx, p, member) {
		return &apiError{403, "No access to this pool membership"}
	}
	if len(parts) == 3 && r.Method == "GET" {
		v, err := a.poolMemberDetail(ctx, a.Pool, member)
		if err != nil {
			return err
		}
		write(w, 200, v)
		return nil
	}
	if len(parts) != 4 || r.Method != "POST" {
		return &apiError{405, "请求方法不允许"}
	}
	if !p.has("attempt.dispatch") {
		return &apiError{403, "无职位池分配权限"}
	}
	if parts[3] == "review" {
		return &apiError{410, "待复核阶段已取消，请按当前入池状态继续处理"}
	}
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	wf, err := a.lockWorkflow(ctx, tx, member["candidate_id"])
	if err != nil {
		return err
	}
	member, err = one(ctx, tx, "SELECT row_to_json(m) FROM platform_pool_memberships m WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return err
	}
	if num(body["revision"]) != num(member["revision"]) {
		return &apiError{409, "入池记录已变化，请刷新后重试"}
	}
	if num(wf["current_resume_id"]) != num(member["resume_id"]) {
		return bad("该记录不属于当前有效志愿")
	}
	code, message := "", ""
	var allocationTask Object
	switch parts[3] {
	case "allocate":
		scope, e := a.allocationScope(ctx, tx, member, false)
		if e != nil {
			return e
		}
		if !a.canAccessAllocationScope(ctx, tx, p, scope, true) {
			return &apiError{403, "无当前范围分配权限"}
		}
		allocationTask, err = a.enqueuePoolAllocation(ctx, tx, member, p)
		code, message = "allocation_queued", "分配任务已提交，可在分配任务中查看进度"
	case "tags":
		if member["status"] != "pending_allocation" {
			return bad("仅待分配记录可修订标签")
		}
		if strings.TrimSpace(str(body["note"])) == "" {
			return bad("人工修订必须填写依据")
		}
		if _, ok := body["tags"].([]any); !ok {
			return bad("标签必须为列表")
		}
		tags := []any{}
		seen := map[string]bool{}
		for _, value := range list(body["tags"]) {
			tag := obj(value)
			key := str(tag["code"])
			known := false
			for _, definition := range list(obj(member["assessment"])["tag_catalog"]) {
				if obj(definition)["code"] == key {
					known = true
				}
			}
			if !known || seen[key] || !contains([]string{"supported", "needs_verification"}, str(tag["status"])) {
				return bad("标签不存在、重复或状态无效")
			}
			seen[key] = true
			evidence := []any{}
			for _, prior := range list(member["tags"]) {
				if obj(prior)["code"] == key {
					evidence = list(obj(prior)["evidence"])
				}
			}
			tags = append(tags, Object{"code": key, "status": tag["status"], "confidence": 1, "source": "manual", "note": body["note"], "evidence": evidence})
		}
		if err = a.poolEvent(ctx, tx, member, "tags_revised", Object{"previous": member["tags"], "tags": tags, "note": body["note"]}, p); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE platform_pool_memberships SET tags=$2::jsonb,revision=revision+1,updated_at=now() WHERE id=$1", id, string(canonicalJSON(tags, false)))
		if err != nil {
			return err
		}
		revised, e := one(ctx, tx, "SELECT row_to_json(m) FROM platform_pool_memberships m WHERE id=$1", id)
		if e != nil {
			return e
		}
		if _, err = a.saveScreeningQualification(ctx, tx, revised, false); err != nil {
			return err
		}
		code, message = "pool_tags_updated", "标签已修订，可重新执行分配"
	default:
		return &apiError{404, "未找到操作"}
	}
	if err != nil {
		return err
	}
	if _, err = a.invalidateWorkflow(ctx, tx, wf); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	a.wakeAllocation(ctx)
	response := Object{"code": code, "detail": message}
	status := 200
	if allocationTask != nil {
		response["task_id"] = allocationTask["id"]
		status = 202
	}
	write(w, status, response)
	return nil
}

func noPoolRecord(err error) bool { var e *apiError; return errors.As(err, &e) && e.Status == 404 }

func (a *App) poolMemberDetail(ctx context.Context, db DB, member Object) (Object, error) {
	if member == nil {
		return nil, nil
	}
	value := clone(member)
	delete(value, "file_checksum")
	events, err := rows(ctx, db, "SELECT row_to_json(e) FROM platform_pool_events e WHERE member_id=$1 ORDER BY id", member["id"])
	if err != nil {
		return nil, err
	}
	value["events"] = events
	item, e := one(ctx, db, `SELECT row_to_json(i) FROM platform_allocation_work_items i WHERE member_id=$1 ORDER BY id DESC LIMIT 1`, member["id"])
	if e != nil && !noPoolRecord(e) {
		return nil, e
	}
	target, e := one(ctx, db, `SELECT row_to_json(t) FROM platform_assignment_targets t JOIN core_assignmentattempt a ON a.id=t.attempt_id WHERE t.member_id=$1 AND a.status IN ('pending_review','pending_dispatch','dispatched','passed') ORDER BY a.id DESC LIMIT 1`, member["id"])
	if e != nil && !noPoolRecord(e) {
		return nil, e
	}
	value["allocation"] = Object{"task_id": item["task_id"], "status": item["status"], "reason_code": item["reason_code"], "reason": allocationReason(str(item["reason_code"])), "target": target}
	return value, nil
}
