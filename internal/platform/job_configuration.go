package platform

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

// Job-derived requirements have one owner: the job record. Policy owns routing,
// tag definitions and department rules; a conflicting posting needs an explicit source.
func jobPolicyCode(prefix, entity, name string) string {
	return prefix + "_" + fingerprint([]string{normalized(entity), normalized(name)})[:24]
}

func (a *App) configurationJobs(ctx context.Context, db DB) ([]Object, error) {
	return rows(ctx, db, `SELECT to_jsonb(j)||jsonb_build_object('department_name',d.name,'department_level',d.level,'required_majors',COALESCE((SELECT jsonb_agg(m.major ORDER BY m.major) FROM core_jobmajor m WHERE m.job_id=j.id),'[]'::jsonb),'reception_state',COALESCE(ds.reception_state,'receiving')) FROM core_job j LEFT JOIN core_department d ON d.id=j.department_id LEFT JOIN platform_demand_settings ds ON ds.demand_id=j.id ORDER BY j.id`)
}

func jobRequirementFields(job Object) Object {
	return Object{"responsibilities": strings.TrimSpace(str(job["responsibilities"])), "education": strings.TrimSpace(str(job["education"])), "required_majors": append([]any{}, list(job["required_majors"])...)}
}

func deriveJobPolicy(policy Object, jobs []Object) Object {
	policy = clone(policy)
	groups := map[string][]Object{}
	jobsByID := map[int64]Object{}
	for _, job := range jobs {
		jobsByID[num(job["id"])] = job
	}
	rules := []any{}
	for _, value := range list(policy["rules"]) {
		if jobsByID[num(obj(value)["job_id"])] != nil {
			rules = append(rules, value)
		}
	}
	policy["rules"] = rules
	for _, job := range jobs {
		if !truth(job["is_active"]) || normalized(str(job["entity"])) == "" || normalized(str(job["position_name"])) == "" {
			continue
		}
		poolCode := jobPolicyCode("pool", str(job["entity"]), str(job["position_name"]))
		if policyItem(policy, "pools", poolCode) == nil {
			policy["pools"] = append(list(policy["pools"]), Object{"code": poolCode, "entity": job["entity"], "name": job["position_name"], "active": true, "generated": true})
		}
		var rule Object
		for _, v := range list(policy["rules"]) {
			if num(obj(v)["job_id"]) == num(job["id"]) {
				rule = obj(v)
				break
			}
		}
		if rule == nil {
			policy["rules"] = append(list(policy["rules"]), Object{"job_id": job["id"], "pool_code": poolCode, "active": false, "required_tags": []any{}, "preferred_tags": []any{}, "priority": 0})
		} else if normalized(str(policyItem(policy, "pools", str(rule["pool_code"]))["entity"])) != normalized(str(job["entity"])) {
			// A demand moved to another entity needs its routing explicitly enabled again.
			rule["pool_code"], rule["active"] = poolCode, false
		}
		if normalized(str(job["public_name"])) != "" {
			code := jobPolicyCode("standard", str(job["entity"]), str(job["public_name"]))
			groups[code] = append(groups[code], job)
		}
	}
	codes := []string{}
	for code := range groups {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		group := groups[code]
		s := policyItem(policy, "standards", code)
		if s == nil {
			// An explicitly maintained posting mapping takes precedence over generation.
			mapped := false
			for _, value := range list(policy["standards"]) {
				other := obj(value)
				for _, name := range stringValues(other["application_names"]) {
					if normalized(str(other["entity"])) == normalized(str(group[0]["entity"])) && normalized(name) == normalized(str(group[0]["public_name"])) {
						mapped = true
					}
				}
			}
			if mapped {
				continue
			}
			s = Object{"code": code, "name": group[0]["public_name"], "entity": group[0]["entity"], "application_names": []any{group[0]["public_name"]}, "active": true, "generated": true, "tag_codes": []any{}, "required_majors": []any{}, "responsibilities": "", "education": ""}
			policy["standards"] = append(list(policy["standards"]), s)
		}
	}
	for _, value := range list(policy["standards"]) {
		s := obj(value)
		if !truth(s["generated"]) {
			continue
		}
		group := groups[str(s["code"])]
		s["configuration_issue"] = ""
		s["source_job_ids"] = []any{}
		var source Object
		signatures := map[string]bool{}
		for _, job := range group {
			s["source_job_ids"] = append(list(s["source_job_ids"]), job["id"])
			signatures[fingerprint(Object{"pool": normalized(str(job["position_name"])), "requirement": jobRequirementFields(job)})] = true
			if num(s["source_job_id"]) == num(job["id"]) {
				source = job
			}
		}
		if len(group) == 0 {
			s["configuration_issue"] = "source_job_missing"
			continue
		}
		if num(s["source_job_id"]) > 0 && source == nil {
			s["configuration_issue"] = "source_job_missing"
			continue
		}
		s["pool_code"] = jobPolicyCode("pool", str(group[0]["entity"]), str(group[0]["position_name"]))
		s["entity"], s["name"], s["application_names"] = group[0]["entity"], group[0]["public_name"], []any{group[0]["public_name"]}
		if source == nil {
			if len(signatures) > 1 {
				s["configuration_issue"] = "source_job_conflict"
				continue
			}
			source = group[0]
		}
		s["pool_code"] = jobPolicyCode("pool", str(source["entity"]), str(source["position_name"]))
		for key, value := range jobRequirementFields(source) {
			s[key] = value
		}
		if str(s["responsibilities"]) == "" {
			s["configuration_issue"] = "job_responsibility_missing"
		}
	}
	return policy
}

func (a *App) syncJobPolicy(ctx context.Context, db DB, principal *Principal) error {
	current, err := one(ctx, db, "SELECT row_to_json(p) FROM platform_pool_policy p WHERE singleton FOR UPDATE")
	if err != nil {
		return err
	}
	jobs, err := a.configurationJobs(ctx, db)
	if err != nil {
		return err
	}
	policy := deriveJobPolicy(obj(current["policy"]), jobs)
	if fingerprint(policy) != fingerprint(current["policy"]) {
		if _, err = a.applyConfigurationImpact(ctx, db, obj(current["policy"]), policy, true); err != nil {
			return err
		}
		if _, err = db.Exec(ctx, "UPDATE platform_pool_policy SET policy=$1::jsonb,version=version+1 WHERE singleton", string(canonicalJSON(policy, false))); err != nil {
			return err
		}
		if _, err = db.Exec(ctx, "INSERT INTO platform_pool_config_events(version,policy,actor_id) SELECT version,policy,$1 FROM platform_pool_policy WHERE singleton", principal.User["id"]); err != nil {
			return err
		}
	}
	return a.syncAllocationConfig(ctx, db)
}

func (a *App) applyConfigurationImpact(ctx context.Context, db DB, before, after Object, apply bool) (Object, error) {
	codes := []string{}
	for _, v := range list(before["standards"]) {
		s := obj(v)
		next := policyItem(after, "standards", str(s["code"]))
		changed := next == nil || !activePolicyItem(next) || !activePolicyItem(policyItem(after, "pools", str(next["pool_code"]))) || str(next["configuration_issue"]) != ""
		if !changed {
			changed = standardJob(before, s)["content_hash"] != standardJob(after, next)["content_hash"] || fingerprint(s["application_names"]) != fingerprint(next["application_names"])
		}
		if changed {
			codes = append(codes, str(s["code"]))
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM platform_pool_memberships WHERE standard_code=ANY($1) AND status='pending_allocation'`, codes).Scan(&count); err != nil {
		return nil, err
	}
	if apply && count > 0 {
		members, err := rows(ctx, db, `UPDATE platform_pool_memberships SET status='needs_reanalysis',revision=revision+1,updated_at=now() WHERE standard_code=ANY($1) AND status='pending_allocation' RETURNING row_to_json(platform_pool_memberships)`, codes)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if _, err = db.Exec(ctx, `UPDATE platform_allocation_work_items SET status='cancelled',reason_code='qualification_stale' WHERE member_id=$1 AND status IN ('pending','leased','waiting')`, m["id"]); err != nil {
				return nil, err
			}
			if err = a.poolEvent(ctx, db, m, "assessment_changed", Object{"message": "岗位标准或能力标签已变化，需要重新评估"}, nil); err != nil {
				return nil, err
			}
		}
	}
	return Object{"changed_standard_codes": codes, "reassessment_count": count}, nil
}

var configurationMessages = map[string]string{
	"assessment_standard_missing": "未找到同主体、同投递名称的评估标准",
	"source_job_missing":          "标准来源岗位已停用或改名，请重新选择来源",
	"source_job_conflict":         "同一投递对应不同职责或内部职位，请明确标准来源岗位",
	"job_responsibility_missing":  "标准来源岗位缺少工作职责",
	"job_mapping_ambiguous":       "同一投递关联了多个评估标准",
	"allocation_rules_missing":    "尚未启用部门分配规则，可筛选入池，暂不能分配",
	"allocation_tags_missing":     "部门规则标签未纳入评估标签，需补齐后重新评估",
	"no_receiving_demand":         "关联需求均暂停接收或已停用，可筛选入池等待",
}

func (a *App) writePoolConfiguration(w http.ResponseWriter, r *http.Request) error {
	value, err := a.poolPolicy(r.Context(), a.Pool)
	if err != nil {
		return err
	}
	jobs, err := a.configurationJobs(r.Context(), a.Pool)
	if err != nil {
		return err
	}
	value["job_options"] = jobs
	policy := obj(value["policy"])
	coverage := []Object{}
	for _, v := range list(policy["standards"]) {
		s := obj(v)
		d := resolveApplicationStandard(Object{"pool_policy": policy}, Object{"entity": s["entity"], "position_name": firstPolicyName(s)}, Object{})
		code := str(d["status"])
		if code == "ready" {
			code = allocationConfigurationIssue(policy, s, jobs)
		}
		demands := []any{}
		for _, v := range list(policy["rules"]) {
			rule := obj(v)
			if rule["pool_code"] == s["pool_code"] && activePolicyItem(rule) {
				demands = append(demands, rule["job_id"])
			}
		}
		coverage = append(coverage, Object{"code": s["code"], "entity": s["entity"], "name": s["name"], "status": code, "message": configurationMessages[code], "screening_ready": d["status"] == "ready", "demand_ids": demands})
	}
	value["coverage"] = coverage
	write(w, 200, value)
	return nil
}

func firstPolicyName(s Object) string {
	names := stringValues(s["application_names"])
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// Reprocessing is explicitly requested after repair. Existing effective assignments
// remain protected by retainApplicationWork; the normal queue owns all new work.
func (a *App) reprocessConfiguration(w http.ResponseWriter, r *http.Request, p *Principal) error {
	if !p.has("pipeline.run") || !p.has("settings.manage_config") {
		return &apiError{403, "无配置修复重处理权限"}
	}
	if r.Method != "POST" {
		return &apiError{405, "请求方法不允许"}
	}
	ctx := r.Context()
	candidates, err := rows(ctx, a.Pool, `SELECT DISTINCT jsonb_build_object('id',w.candidate_id) FROM core_candidateworkflow w WHERE w.block_reason IN ('assessment_standard_missing','source_job_missing','source_job_conflict','job_mapping_ambiguous','job_responsibility_missing') OR EXISTS(SELECT 1 FROM platform_pool_memberships m WHERE m.workflow_id=w.id AND m.status='needs_reanalysis')`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for _, c := range candidates {
		ids = append(ids, num(c["id"]))
	}
	if len(ids) == 0 {
		return bad("没有因配置受阻或需要重新评估的候选人")
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	run, err := a.createRun(ctx, tx, "step2", Object{"force_reprocess": true, "task_name": "配置修复后重新处理"}, ids, p)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	a.wakeQueue(ctx)
	write(w, 202, Object{"run_id": run["id"], "candidate_count": len(ids)})
	return nil
}

func allocationConfigurationIssue(policy, standard Object, jobs []Object) string {
	found, receiving := false, false
	byID := map[int64]Object{}
	for _, j := range jobs {
		byID[num(j["id"])] = j
	}
	for _, v := range list(policy["rules"]) {
		rule := obj(v)
		if !activePolicyItem(rule) || rule["pool_code"] != standard["pool_code"] {
			continue
		}
		found = true
		for _, field := range []string{"required_tags", "preferred_tags"} {
			for _, code := range stringValues(rule[field]) {
				if !contains(stringValues(standard["tag_codes"]), code) || !activePolicyItem(policyItem(policy, "tags", code)) {
					return "allocation_tags_missing"
				}
			}
		}
		j := byID[num(rule["job_id"])]
		if truth(j["is_active"]) && normalized(str(j["entity"])) == normalized(str(standard["entity"])) && isJobDepartment(j["department_level"]) && j["reception_state"] == "receiving" {
			receiving = true
		}
	}
	if !found {
		return "allocation_rules_missing"
	}
	if !receiving {
		return "no_receiving_demand"
	}
	return ""
}

// Preview uses the same volunteer/admission/standard resolver as the worker.
func (a *App) processingConfigurationCheck(w http.ResponseWriter, r *http.Request, p *Principal) error {
	if !p.has("pipeline.run") {
		return &apiError{403, "无简历处理权限"}
	}
	if r.Method != "POST" {
		return &apiError{405, "请求方法不允许"}
	}
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	ids, err := a.resolveRunCandidates(r, p, obj(body["scope"]))
	if err != nil {
		return err
	}
	data, err := a.loadPreparationData(r.Context())
	if err != nil {
		return err
	}
	jobs, err := a.configurationJobs(r.Context(), a.Pool)
	if err != nil {
		return err
	}
	ready, blocked, waiting, admission := 0, 0, 0, 0
	groups := map[string]Object{}
	for _, id := range ids {
		candidate, err := a.get(r.Context(), a.Pool, "core_candidate", id)
		if err != nil {
			return err
		}
		frozen, err := a.freezeCase(r.Context(), Object{"scope": Object{}}, candidate, Object{}, data)
		if err != nil {
			return err
		}
		d := obj(frozen["preflight"])
		code := str(d["status"])
		var current Object
		for _, v := range list(obj(frozen["snapshot"])["volunteers"]) {
			if obj(v)["ref"] == d["current_volunteer_ref"] {
				current = obj(v)
			}
		}
		if code == "ready" {
			ready++
			code = allocationConfigurationIssue(data.policy, policyItem(data.policy, "standards", str(d["standard_code"])), jobs)
			if code == "" {
				continue
			}
			waiting++
		} else if _, ok := configurationMessages[code]; !ok {
			admission++
			continue
		} else {
			blocked++
		}
		key := fingerprint([]any{current["entity"], current["position_name"], code})
		if groups[key] == nil {
			groups[key] = Object{"entity": current["entity"], "application_name": current["position_name"], "code": code, "message": configurationMessages[code], "count": 0, "configuration_url": "/jobs?tab=configuration"}
		}
		groups[key]["count"] = num(groups[key]["count"]) + 1
	}
	issues := []Object{}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		issues = append(issues, groups[key])
	}
	write(w, 200, Object{"total": len(ids), "ready_count": ready, "blocked_count": blocked, "allocation_wait_count": waiting, "admission_or_completed_count": admission, "issues": issues})
	return nil
}
