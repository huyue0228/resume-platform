package platform

import (
	"context"
	"errors"
	"net/http"
	"sort"
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
		err = a.archiveWorkflow(ctx, db, w, "agent_no_recommendation", "当前投递岗位契合度未通过")
		return "ai_archived", str(match["reason"]), err
	}
	policy := obj(obj(frozen["snapshot"])["pool_policy"])
	standard := policyItem(policy, "standards", str(d["standard_code"]))
	pool := policyItem(policy, "pools", str(d["pool_code"]))
	status := "pending_allocation"
	if rec == "review" {
		status = "pending_review"
	}
	assessment := Object{"standard": standard, "pool": pool, "requirement": standardJob(policy, standard), "tag_catalog": obj(frozen["snapshot"])["tag_catalog"], "candidate": brief(obj(obj(frozen["snapshot"])["candidate"]), "highest_major", "highest_education"), "score": match["score"], "reason": match["reason"], "evidence": match["evidence"], "pin": pin}
	tags := list(obj(result["profile"])["tags"])
	member, err := one(ctx, db, `INSERT INTO platform_pool_memberships(candidate_id,workflow_id,resume_id,decision_id,run_id,standard_code,pool_code,status,tags,assessment,file_checksum) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,$11) RETURNING row_to_json(platform_pool_memberships)`, item["candidate_id"], w["id"], resume["id"], decision["id"], run["id"], d["standard_code"], d["pool_code"], status, string(canonicalJSON(tags, false)), string(canonicalJSON(assessment, false)), obj(result["manifest"])["resume_checksum"])
	if err != nil {
		return "", "", err
	}
	if err = a.poolEvent(ctx, db, member, "assessment_saved", Object{"status": status, "tags": tags, "decision_id": decision["id"]}, nil); err != nil {
		return "", "", err
	}
	if status == "pending_review" {
		return "pool_pending_review", "投递评估待复核，通过后可入池分配", nil
	}
	w, err = a.get(ctx, db, "core_candidateworkflow", w["id"])
	if err != nil {
		return "", "", err
	}
	return a.allocatePoolMember(ctx, db, member, w, nil)
}

type poolOption struct {
	job, rule Object
	hits      []string
	score     int
}

func eligiblePoolRules(policy, member Object, jobs map[int64]Object) []poolOption {
	supported := map[string]bool{}
	for _, value := range list(member["tags"]) {
		tag := obj(value)
		if activePolicyItem(policyItem(policy, "tags", str(tag["code"]))) && tag["status"] == "supported" && (tag["source"] == "manual" || floatValue(tag["confidence"]) >= .8) {
			supported[str(tag["code"])] = true
		}
	}
	options := []poolOption{}
	for _, value := range list(policy["rules"]) {
		rule := obj(value)
		job := jobs[num(rule["job_id"])]
		if !activePolicyItem(rule) || rule["pool_code"] != member["pool_code"] || job == nil || !truth(job["is_active"]) {
			continue
		}
		pool := policyItem(policy, "pools", str(member["pool_code"]))
		if normalized(str(job["entity"])) != normalized(str(pool["entity"])) {
			continue
		}
		required, preferred := stringValues(rule["required_tags"]), stringValues(rule["preferred_tags"])
		if len(required)+len(preferred) == 0 {
			continue
		}
		ok := true
		hits := []string{}
		seen := map[string]bool{}
		score := 0
		for _, code := range required {
			if !supported[code] {
				ok = false
				break
			}
			if !seen[code] {
				hits = append(hits, code)
				seen[code] = true
			}
		}
		if !ok {
			continue
		}
		for _, code := range preferred {
			if supported[code] {
				score++
				if !seen[code] {
					hits = append(hits, code)
					seen[code] = true
				}
			}
		}
		if len(hits) == 0 {
			continue
		}
		options = append(options, poolOption{job: job, rule: rule, hits: hits, score: score})
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].score != options[j].score {
			return options[i].score > options[j].score
		}
		if num(options[i].rule["priority"]) != num(options[j].rule["priority"]) {
			return num(options[i].rule["priority"]) < num(options[j].rule["priority"])
		}
		return num(options[i].job["id"]) < num(options[j].job["id"])
	})
	return options
}

func (a *App) waitInPool(ctx context.Context, db DB, member Object, code, message string, p *Principal) (string, string, error) {
	status := "pending_allocation"
	if code == "assessment_changed" {
		status = "needs_reanalysis"
	}
	_, err := db.Exec(ctx, "UPDATE platform_pool_memberships SET status=$2,revision=revision+1,updated_at=now() WHERE id=$1", member["id"], status)
	if err != nil {
		return "", "", err
	}
	err = a.poolEvent(ctx, db, member, "allocation_waiting", Object{"code": code, "message": message}, p)
	return code, message, err
}

func (a *App) allocatePoolMember(ctx context.Context, db DB, member, w Object, p *Principal) (string, string, error) {
	if member["status"] != "pending_allocation" {
		return "", "", bad("仅入池待分配的候选人可执行分配")
	}
	if num(w["current_resume_id"]) != num(member["resume_id"]) || contains([]string{"passed", "archived"}, str(w["status"])) {
		return "", "", bad("当前有效志愿已变化，不能使用历史入池资格")
	}
	var active bool
	if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_assignmentattempt WHERE workflow_id=$1 AND status IN ('pending_review','pending_dispatch','dispatched','passed'))", w["id"]).Scan(&active); err != nil {
		return "", "", err
	}
	if active {
		return "", "", bad("候选人已有有效分配记录")
	}
	config, err := one(ctx, db, "SELECT row_to_json(p) FROM platform_pool_policy p WHERE singleton FOR SHARE")
	if err != nil {
		return "", "", err
	}
	policy := obj(config["policy"])
	standard := policyItem(policy, "standards", str(member["standard_code"]))
	pool := policyItem(policy, "pools", str(member["pool_code"]))
	if !activePolicyItem(standard) || !activePolicyItem(pool) || standard["pool_code"] != member["pool_code"] || standardJob(policy, standard)["content_hash"] != obj(obj(member["assessment"])["requirement"])["content_hash"] {
		return a.waitInPool(ctx, db, member, "assessment_changed", "投递标准或标签定义已变化，请重新评估", p)
	}
	resume, err := a.get(ctx, db, "core_resume", member["resume_id"])
	if err != nil {
		return "", "", err
	}
	path, err := a.resumeFile(str(resume["resume_file"]))
	if err != nil {
		return a.waitInPool(ctx, db, member, "assessment_changed", "简历文件不可用，请补充后重新评估", p)
	}
	checksum, _, err := fileDigest(ctx, path)
	if err != nil || checksum != member["file_checksum"] {
		return a.waitInPool(ctx, db, member, "assessment_changed", "简历正文已变化，请重新评估", p)
	}
	candidate, err := a.get(ctx, db, "core_candidate", member["candidate_id"])
	if err != nil {
		return "", "", err
	}
	if fingerprint(brief(candidate, "highest_major", "highest_education")) != fingerprint(obj(obj(member["assessment"])["candidate"])) {
		return a.waitInPool(ctx, db, member, "assessment_changed", "候选人基础信息已变化，请重新评估", p)
	}
	current := resolveApplicationStandard(Object{"pool_policy": policy}, resume, Object{})
	if current["standard_code"] != member["standard_code"] || current["pool_code"] != member["pool_code"] {
		return a.waitInPool(ctx, db, member, "assessment_changed", "投递关联已变化，请重新评估", p)
	}
	jobIDs := []int64{}
	for _, value := range list(policy["rules"]) {
		rule := obj(value)
		if activePolicyItem(rule) && rule["pool_code"] == member["pool_code"] {
			jobIDs = append(jobIDs, num(rule["job_id"]))
		}
	}
	jobValues, err := rows(ctx, db, "SELECT row_to_json(j) FROM core_job j WHERE id=ANY($1) AND is_active ORDER BY id FOR UPDATE", jobIDs)
	if err != nil {
		return "", "", err
	}
	jobs := map[int64]Object{}
	for _, job := range jobValues {
		dep, e := a.get(ctx, db, "core_department", job["department_id"])
		if e == nil && isJobDepartment(dep["level"]) {
			jobs[num(job["id"])] = job
		}
	}
	options := eligiblePoolRules(policy, member, jobs)
	if len(options) == 0 {
		return a.waitInPool(ctx, db, member, "pool_tags_unmatched", "尚无满足标签条件的部门需求，保留入池资格", p)
	}
	run, err := a.get(ctx, db, "core_processingrun", member["run_id"])
	if err != nil {
		return "", "", err
	}
	for _, option := range options {
		job := option.job
		capacity := num(job["headcount"]) * max(1, num(run["job_hc_coefficient_snapshot"]))
		_, err = db.Exec(ctx, "INSERT INTO core_processingrunjobcapacity(run_id,job_id,capacity,used_count,headcount_snapshot,coefficient_snapshot) VALUES($1,$2,$3,0,$4,$5) ON CONFLICT(run_id,job_id) DO NOTHING", run["id"], job["id"], capacity, job["headcount"], max(1, num(run["job_hc_coefficient_snapshot"])))
		if err != nil {
			return "", "", err
		}
		cap, err := one(ctx, db, "UPDATE core_processingrunjobcapacity c SET capacity=GREATEST(used_count,$3) WHERE run_id=$1 AND job_id=$2 RETURNING row_to_json(c)", run["id"], job["id"], capacity)
		if err != nil {
			return "", "", err
		}
		if num(cap["used_count"]) >= capacity {
			continue
		}
		if _, err = db.Exec(ctx, "UPDATE core_processingrunjobcapacity SET used_count=used_count+1 WHERE id=$1", cap["id"]); err != nil {
			return "", "", err
		}
		target, err := a.get(ctx, db, "core_department", job["department_id"])
		if err != nil {
			return "", "", err
		}
		reason := "职位池：" + str(pool["name"]) + "；命中标签：" + poolTagNames(policy, option.hits)
		attempt, err := a.createAttempt(ctx, db, w, resume, target, Object{"source": "ai", "match_mode": "ai", "agent_decision_id": member["decision_id"], "confidence_score": obj(member["assessment"])["score"], "match_reason": reason, "capacity_reservation_id": cap["id"], "review_required": false}, nil)
		if err != nil {
			return "", "", err
		}
		if _, err = a.save(ctx, db, "core_resume", resume["id"], Object{"job_id": job["id"], "job_category": job["category"], "category_mode": "ai", "category_reason": reason}); err != nil {
			return "", "", err
		}
		if _, err = a.save(ctx, db, "core_agentdispatchdecision", member["decision_id"], Object{"recommendation": "dispatch", "recommended_job_id": job["id"], "recommended_department_id": job["department_id"]}); err != nil {
			return "", "", err
		}
		if _, err = db.Exec(ctx, "UPDATE platform_pool_memberships SET status='allocated',revision=revision+1,updated_at=now() WHERE id=$1", member["id"]); err != nil {
			return "", "", err
		}
		err = a.poolEvent(ctx, db, member, "allocated", Object{"job_id": job["id"], "attempt_id": attempt["id"], "matched_tags": option.hits, "rule": option.rule, "policy_version": config["version"], "reason": reason}, p)
		return "pool_allocated", reason, err
	}
	return a.waitInPool(ctx, db, member, "job_hc_exhausted", "符合标签要求的部门名额已满，保留入池资格", p)
}

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
	return value, nil
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
		for _, m := range members {
			delete(m, "file_checksum")
		}
		return paginate(w, r, members)
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
		return &apiError{403, "无入池复核或分配权限"}
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
	switch parts[3] {
	case "review":
		if strings.TrimSpace(str(body["note"])) == "" {
			return bad("复核必须填写依据")
		}
		if member["status"] != "pending_review" {
			return bad("当前记录不处于待复核状态")
		}
		if body["decision"] == "approve" {
			member["status"] = "pending_allocation"
			if _, err = tx.Exec(ctx, "UPDATE platform_pool_memberships SET status='pending_allocation',revision=revision+1,updated_at=now() WHERE id=$1", id); err != nil {
				return err
			}
			if err = a.poolEvent(ctx, tx, member, "review_approved", Object{"note": str(body["note"])}, p); err != nil {
				return err
			}
			code, message, err = a.allocatePoolMember(ctx, tx, member, wf, p)
		} else if body["decision"] == "reject" {
			_, err = tx.Exec(ctx, "UPDATE platform_pool_memberships SET status='rejected',revision=revision+1,updated_at=now() WHERE id=$1", id)
			if err != nil {
				return err
			}
			if err = a.poolEvent(ctx, tx, member, "review_rejected", Object{"note": str(body["note"])}, p); err != nil {
				return err
			}
			if _, err = a.save(ctx, tx, "core_agentdispatchdecision", member["decision_id"], Object{"recommendation": "archive"}); err != nil {
				return err
			}
			err = a.archiveWorkflow(ctx, tx, wf, "agent_no_recommendation", "投递评估经人工复核未通过")
			code, message = "pool_rejected", "复核未通过"
		} else {
			return bad("请选择通过或不通过")
		}
	case "allocate":
		code, message, err = a.allocatePoolMember(ctx, tx, member, wf, p)
	case "tags":
		if !contains([]string{"pending_review", "pending_allocation"}, str(member["status"])) {
			return bad("仅待复核或待分配记录可修订标签")
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
	write(w, 200, Object{"code": code, "detail": message})
	return nil
}

func noPoolRecord(err error) bool { var e *apiError; return errors.As(err, &e) && e.Status == 404 }
