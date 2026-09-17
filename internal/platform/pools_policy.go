package platform

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

var poolCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func emptyPoolPolicy() Object {
	return Object{"tags": []any{}, "pools": []any{}, "standards": []any{}, "rules": []any{}}
}

func (a *App) migratePositionPools(ctx context.Context, db DB) error {
	_, err := db.Exec(ctx, `
CREATE TABLE IF NOT EXISTS platform_pool_policy(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),version bigint NOT NULL DEFAULT 1,policy jsonb NOT NULL);
INSERT INTO platform_pool_policy(singleton,policy) VALUES(true,'{"tags":[],"pools":[],"standards":[],"rules":[]}') ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS platform_pool_config_events(id bigserial PRIMARY KEY,version bigint NOT NULL,actor_id bigint REFERENCES accounts_user(id),policy jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS platform_pool_memberships(
 id bigserial PRIMARY KEY,candidate_id bigint NOT NULL REFERENCES core_candidate(id),workflow_id bigint NOT NULL REFERENCES core_candidateworkflow(id),
 resume_id bigint NOT NULL REFERENCES core_resume(id),decision_id bigint NOT NULL REFERENCES core_agentdispatchdecision(id),run_id bigint NOT NULL REFERENCES core_processingrun(id),
 standard_code text NOT NULL,pool_code text NOT NULL,status text NOT NULL CHECK(status IN ('pending_review','pending_allocation','allocated','needs_reanalysis','closed','rejected')),
 tags jsonb NOT NULL DEFAULT '[]',assessment jsonb NOT NULL,file_checksum text NOT NULL,revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX IF NOT EXISTS platform_pool_active_candidate ON platform_pool_memberships(candidate_id) WHERE status IN ('pending_review','pending_allocation','allocated','needs_reanalysis');
CREATE TABLE IF NOT EXISTS platform_pool_events(id bigserial PRIMARY KEY,member_id bigint NOT NULL REFERENCES platform_pool_memberships(id),kind text NOT NULL,payload jsonb NOT NULL,actor_id bigint REFERENCES accounts_user(id),created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS platform_pool_events_member ON platform_pool_events(member_id,id);
CREATE INDEX IF NOT EXISTS platform_pool_members_waiting ON platform_pool_memberships(pool_code,status,created_at,id);
`)
	return err
}

func (a *App) poolPolicy(ctx context.Context, db DB) (Object, error) {
	return one(ctx, db, "SELECT row_to_json(p) FROM platform_pool_policy p WHERE singleton")
}

func policyItem(policy Object, kind, code string) Object {
	for _, value := range list(policy[kind]) {
		if obj(value)["code"] == code {
			return obj(value)
		}
	}
	return nil
}

func activePolicyItem(item Object) bool { return item != nil && item["active"] != false }

func (a *App) validatePoolPolicy(ctx context.Context, db DB, policy Object) error {
	for _, kind := range []string{"tags", "pools", "standards", "rules"} {
		values, ok := policy[kind].([]any)
		if !ok || len(values) > 2000 || (kind == "tags" && len(values) > 200) {
			return bad("配置列表无效或超出上限")
		}
		seen := map[string]bool{}
		for _, value := range values {
			item := obj(value)
			code := str(item["code"])
			if kind == "rules" {
				code = str(item["job_id"])
			}
			if code == "" || seen[code] {
				return bad("配置标识缺失或重复：" + kind)
			}
			seen[code] = true
			if kind != "rules" && (!poolCode.MatchString(code) || strings.TrimSpace(str(item["name"])) == "" || len([]rune(str(item["name"]))) > 100) {
				return bad("标识仅支持小写字母、数字、下划线，名称不能为空")
			}
			if active, ok := item["active"]; ok {
				if _, ok := active.(bool); !ok {
					return bad("启用状态必须为布尔值")
				}
			}
			switch kind {
			case "tags":
				if !contains([]string{"direction", "skill", "experience"}, str(item["category"])) || strings.TrimSpace(str(item["description"])) == "" || len([]rune(str(item["description"]))) > 1000 {
					return bad("能力标签必须填写类别和证据判定说明")
				}
			case "pools":
				if strings.TrimSpace(str(item["entity"])) == "" {
					return bad("内部职位池必须指定招聘主体")
				}
			case "standards", "rules":
				pool := policyItem(policy, "pools", str(item["pool_code"]))
				if pool == nil {
					return bad("未找到关联的内部职位池")
				}
				fields := []string{"required_tags", "preferred_tags"}
				if kind == "standards" {
					fields = []string{"tag_codes"}
					for _, field := range []string{"application_names", "required_majors"} {
						if err := validatePolicyStrings(item[field]); err != nil {
							return err
						}
					}
					if str(item["configuration_issue"]) == "" && (item["entity"] != pool["entity"] || len(stringValues(item["application_names"])) == 0 || strings.TrimSpace(str(item["responsibilities"])) == "") {
						return bad("投递评估标准必须指定同主体职位池、投递映射和职责")
					}
					for _, name := range stringValues(item["application_names"]) {
						if normalized(name) == "" {
							return bad("投递映射名称不能为空")
						}
					}
				} else {
					if v := floatValue(item["priority"]); v < 0 || v > 999 || v != float64(num(item["priority"])) {
						return bad("分配优先级必须为 0 到 999 的整数")
					}
					jobID := num(item["job_id"])
					if jobID <= 0 || str(jobID) != str(item["job_id"]) {
						return bad("部门需求必须选择有效岗位")
					}
					job, err := a.get(ctx, db, "core_job", jobID)
					if err != nil || normalized(str(job["entity"])) != normalized(str(pool["entity"])) {
						return bad("部门岗位与职位池的招聘主体不一致")
					}
					if activePolicyItem(item) && len(list(item["required_tags"]))+len(list(item["preferred_tags"])) == 0 {
						return bad("分配规则至少需要一个必需或优先标签")
					}
				}
				for _, field := range fields {
					if err := validatePolicyStrings(item[field]); err != nil {
						return err
					}
					seenTags := map[string]bool{}
					for _, tag := range stringValues(item[field]) {
						if policyItem(policy, "tags", tag) == nil || seenTags[tag] {
							return bad("标签不存在或重复：" + tag)
						}
						seenTags[tag] = true
					}
				}
			}
		}
	}
	for _, value := range list(policy["pools"]) {
		pool := obj(value)
		count := 0
		for _, v := range list(policy["rules"]) {
			rule := obj(v)
			if activePolicyItem(rule) && rule["pool_code"] == pool["code"] {
				count++
			}
		}
		if count > 200 {
			return bad("同一职位池最多启用 200 条需求")
		}
	}
	for _, value := range list(policy["standards"]) {
		standard := obj(value)
		if !activePolicyItem(standard) || str(standard["configuration_issue"]) != "" {
			continue
		}
		codes := stringValues(standard["tag_codes"])
		for _, v := range list(policy["rules"]) {
			rule := obj(v)
			if !activePolicyItem(rule) || rule["pool_code"] != standard["pool_code"] {
				continue
			}
			for _, field := range []string{"required_tags", "preferred_tags"} {
				for _, tag := range stringValues(rule[field]) {
					if !contains(codes, tag) {
						return bad("投递标准 " + str(standard["code"]) + " 缺少可达需求标签：" + tag)
					}
				}
			}
		}
	}
	// An imported application may identify exactly one reviewed standard.
	mappings := map[string]bool{}
	for _, value := range list(policy["standards"]) {
		s := obj(value)
		if !activePolicyItem(s) {
			continue
		}
		for _, name := range stringValues(s["application_names"]) {
			key := normalized(str(s["entity"])) + "\x1f" + normalized(name)
			if mappings[key] {
				return bad("同主体投递映射存在多个评估标准")
			}
			mappings[key] = true
		}
	}
	return nil
}

func standardJob(policy, standard Object) Object {
	tags := []any{}
	for _, code := range stringValues(standard["tag_codes"]) {
		if tag := policyItem(policy, "tags", code); activePolicyItem(tag) {
			tags = append(tags, brief(tag, "code", "name", "category", "description"))
		}
	}
	sort.Slice(tags, func(i, j int) bool { return str(obj(tags[i])["code"]) < str(obj(tags[j])["code"]) })
	job := Object{"ref": str(standard["code"]), "entity": standard["entity"], "public_name": standard["name"], "position_name": standard["name"], "category": "", "job_family": "", "location": "", "education": str(standard["education"]), "required_majors": stringValues(standard["required_majors"]), "responsibilities": str(standard["responsibilities"]), "department_ref": "", "department_name": ""}
	job["content_hash"] = fingerprint(Object{"requirement": clone(job), "pool": brief(policyItem(policy, "pools", str(standard["pool_code"])), "code", "entity"), "tags": tags})
	return job
}

func resolveApplicationStandard(snapshot, current, d Object) Object {
	policy := obj(snapshot["pool_policy"])
	if strings.TrimSpace(str(current["entity"])) == "" {
		d["status"] = "assessment_standard_missing"
		return d
	}
	var found Object
	for _, value := range list(policy["standards"]) {
		s := obj(value)
		if !activePolicyItem(s) || !activePolicyItem(policyItem(policy, "pools", str(s["pool_code"]))) {
			continue
		}
		if entity := normalized(str(current["entity"])); entity != "" && entity != normalized(str(s["entity"])) {
			continue
		}
		for _, name := range stringValues(s["application_names"]) {
			if normalized(name) == normalized(str(current["position_name"])) {
				if found != nil && found["code"] != s["code"] {
					d["status"] = "job_mapping_ambiguous"
					return d
				}
				found = s
				break
			}
		}
	}
	if found == nil {
		d["status"] = "assessment_standard_missing"
		return d
	}
	if issue := str(found["configuration_issue"]); issue != "" {
		d["status"] = issue
		return d
	}
	if strings.TrimSpace(str(found["responsibilities"])) == "" {
		d["status"] = "job_responsibility_missing"
		return d
	}
	d["standard_code"], d["pool_code"], d["job_refs"], d["status"] = found["code"], found["pool_code"], []any{found["code"]}, "ready"
	return d
}

func (a *App) poolConfigAPI(w http.ResponseWriter, r *http.Request, p *Principal) error {
	if !p.has("settings.manage_config") {
		return &apiError{403, "无职位池配置权限"}
	}
	ctx := r.Context()
	if r.Method == "POST" {
		tx, err := a.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = a.lockAllAllocationScopes(ctx, tx); err != nil {
			return err
		}
		if err = a.syncJobPolicy(ctx, tx, p); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		a.wakeAllocation(ctx)
		return a.writePoolConfiguration(w, r)
	}
	if r.Method == "GET" {
		return a.writePoolConfiguration(w, r)
	}

	if r.Method != "PUT" {
		return &apiError{405, "请求方法不允许"}
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
	if err = a.lockAllAllocationScopes(ctx, tx); err != nil {
		return err
	}
	current, err := one(ctx, tx, "SELECT row_to_json(p) FROM platform_pool_policy p WHERE singleton FOR UPDATE")
	if err != nil {
		return err
	}
	if num(body["version"]) != num(current["version"]) {
		return &apiError{409, "配置已被修改，请刷新后重试"}
	}
	policy := obj(body["policy"])
	if policy == nil {
		return bad("配置不能为空")
	}
	for _, value := range list(obj(current["policy"])["standards"]) {
		old := obj(value)
		if truth(old["generated"]) {
			if next := policyItem(policy, "standards", str(old["code"])); next != nil {
				next["generated"] = true
			}
		}
	}
	jobs, err := a.configurationJobs(ctx, tx)
	if err != nil {
		return err
	}
	policy = deriveJobPolicy(policy, jobs)
	if err = a.validatePoolPolicy(ctx, tx, policy); err != nil {
		return err
	}
	// Codes are stable identities. Retain historical definitions by disabling them.
	for _, kind := range []string{"pools", "standards", "tags"} {
		for _, v := range list(obj(current["policy"])[kind]) {
			if policyItem(policy, kind, str(obj(v)["code"])) == nil {
				return bad("已保存的标识不可删除或改名，请停用旧项并新增")
			}
		}
	}
	impact, err := a.applyConfigurationImpact(ctx, tx, obj(current["policy"]), policy, !truth(body["preview"]))
	if err != nil {
		return err
	}
	if truth(body["preview"]) {
		write(w, 200, impact)
		return nil
	}
	value, err := one(ctx, tx, "UPDATE platform_pool_policy p SET version=version+1,policy=$1::jsonb WHERE singleton RETURNING row_to_json(p)", string(canonicalJSON(policy, false)))
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO platform_pool_config_events(version,actor_id,policy) VALUES($1,$2,$3::jsonb)", value["version"], p.User["id"], string(canonicalJSON(policy, false))); err != nil {
		return err
	}
	if err = a.syncAllocationConfig(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	a.wakeAllocation(ctx)
	return a.writePoolConfiguration(w, r)
}

func (a *App) poolEvent(ctx context.Context, db DB, member Object, kind string, payload Object, p *Principal) error {
	var actor any
	if p != nil {
		actor = p.User["id"]
	}
	_, err := db.Exec(ctx, "INSERT INTO platform_pool_events(member_id,kind,payload,actor_id) VALUES($1,$2,$3::jsonb,$4)", member["id"], kind, string(canonicalJSON(payload, false)), actor)
	return err
}

func validatePolicyStrings(value any) error {
	values, ok := value.([]any)
	if !ok || len(values) > 200 {
		return bad("映射、专业和标签必须为列表，最多 200 项")
	}
	for _, v := range values {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" || len([]rune(s)) > 200 {
			return bad("列表项必须为非空文本，最多 200 字")
		}
	}
	return nil
}
