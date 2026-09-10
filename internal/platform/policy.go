package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

const protocolVersion = "resume-analysis/v2"
const resultVersion = "resume-job-match/v1"
const policyVersion = "go-policy-gate/v4"

func (a *App) ref(kind string, id any) string {
	mac := hmac.New(sha256.New, []byte(a.Config.Secret))
	mac.Write([]byte(kind + ":" + str(id)))
	return hex.EncodeToString(mac.Sum(nil))
}
func preferredEntity(c Object) string {
	for _, key := range []string{"household_province", "highest_degree_province", "first_degree_province"} {
		v := str(c[key])
		for _, province := range strings.Fields("北京 天津 河北 山西 内蒙古 辽宁 吉林 黑龙江 山东 河南 陕西 甘肃 宁夏 新疆 青海") {
			if strings.Contains(v, province) {
				return "GW"
			}
		}
		for _, province := range strings.Fields("上海 江苏 浙江 安徽 福建 江西 湖北 湖南 广东 广西 海南 重庆 四川 贵州 云南 西藏") {
			if strings.Contains(v, province) {
				return "YLS"
			}
		}
	}
	return ""
}
func stringValues(value any) []string {
	out := []string{}
	for _, v := range list(value) {
		out = append(out, str(v))
	}
	return out
}
func prepareSnapshot(s Object) Object {
	c := obj(s["candidate"])
	preferred := preferredEntity(c)
	volunteers := append([]any{}, list(s["volunteers"])...)
	sort.SliceStable(volunteers, func(i, j int) bool {
		a, b := obj(volunteers[i]), obj(volunteers[j])
		ap, bp := preferred != "" && a["entity"] == preferred, preferred != "" && b["entity"] == preferred
		if ap != bp {
			return ap
		}
		ad, bd := str(a["apply_date"]), str(b["apply_date"])
		if ad == "" {
			ad = "9999-12-31"
		}
		if bd == "" {
			bd = "9999-12-31"
		}
		return ad < bd
	})
	d := Object{"first_degree_tag_ref": str(c["first_degree_tag_ref"]), "highest_degree_tag_ref": str(c["highest_degree_tag_ref"]), "volunteer_order": []any{}, "current_volunteer_ref": "", "current_rank": 0, "admission_passed": false, "admission_rule_ref": "", "job_refs": []any{}, "status": "no_effective_volunteer"}
	order := []any{}
	var current Object
	retry := str(obj(s["workflow"])["retry_volunteer_ref"])
	for i, v := range volunteers {
		volunteer := obj(v)
		order = append(order, volunteer["ref"])
		if current == nil && !truth(volunteer["rejected"]) && (retry == "" || volunteer["ref"] == retry) {
			current = volunteer
			d["current_rank"] = i + 1
			d["current_volunteer_ref"] = volunteer["ref"]
		}
	}
	d["volunteer_order"] = order
	if current == nil {
		d["admission_passed"] = true
		return d
	}
	rules := append([]any{}, list(s["admission_rules"])...)
	sort.SliceStable(rules, func(i, j int) bool { return num(obj(rules[i])["priority"]) < num(obj(rules[j])["priority"]) })
	d["admission_passed"] = len(rules) == 0
	tagMatch := false
	for _, v := range rules {
		rule := obj(v)
		if contains(stringValues(rule["first_tag_refs"]), str(d["first_degree_tag_ref"])) && contains(stringValues(rule["highest_tag_refs"]), str(d["highest_degree_tag_ref"])) {
			tagMatch = true
			educations := stringValues(rule["educations"])
			if len(educations) == 0 || contains(educations, str(c["highest_education"])) {
				d["admission_passed"] = true
				d["admission_rule_ref"] = rule["ref"]
				break
			}
		}
	}
	if !truth(d["admission_passed"]) {
		d["status"] = "school_not_eligible"
		if tagMatch {
			d["status"] = "education_not_eligible"
		}
		return d
	}
	names, entities := map[string]bool{}, map[string]bool{}
	mapped := 0
	for _, v := range list(s["jobs"]) {
		j := obj(v)
		if normalized(str(current["position_name"])) != "" && normalized(str(j["public_name"])) == normalized(str(current["position_name"])) && (normalized(str(current["entity"])) == "" || normalized(str(j["entity"])) == normalized(str(current["entity"]))) {
			mapped++
			names[normalized(str(j["position_name"]))] = true
			entities[normalized(str(j["entity"]))] = true
		}
	}
	switch {
	case mapped == 0:
		d["status"] = "job_not_found"
	case len(names) == 1 && names[""]:
		d["status"] = "internal_position_name_missing"
	case len(names) != 1 || names[""] || len(entities) != 1:
		d["status"] = "job_mapping_ambiguous"
	default:
		refs := []string{}
		missing := false
		for _, v := range list(s["jobs"]) {
			j := obj(v)
			if entities[normalized(str(j["entity"]))] && names[normalized(str(j["position_name"]))] && str(j["department_ref"]) != "" && isJobDepartment(j["department_level"]) {
				refs = append(refs, str(j["ref"]))
				if strings.TrimSpace(str(j["responsibilities"])) == "" {
					missing = true
				}
			}
		}
		sort.Strings(refs)
		raw := []any{}
		for _, ref := range refs {
			raw = append(raw, ref)
		}
		d["job_refs"] = raw
		d["status"] = "ready"
		if len(refs) == 0 {
			d["status"] = "job_pool_empty"
		}
		if missing {
			d["status"] = "job_responsibility_missing"
		}
	}
	return d
}
func (a *App) jobContent(ctx context.Context, db DB, j Object) (Object, error) {
	content := brief(j, "entity", "public_name", "position_name", "category", "job_family", "location", "education", "responsibilities", "is_active")
	dep, _ := a.get(ctx, db, "core_department", j["department_id"])
	content["department_name"] = str(dep["name"])
	content["department_level"] = num(dep["level"])
	content["department_identity"] = j["department_id"]
	majors, err := rows(ctx, db, "SELECT row_to_json(m) FROM core_jobmajor m WHERE job_id=$1 ORDER BY major,id", j["id"])
	if err != nil {
		return nil, err
	}
	names := []any{}
	for _, major := range majors {
		names = append(names, major["major"])
	}
	content["required_majors"] = names
	return content, nil
}
func (a *App) freezeCase(ctx context.Context, run, candidate Object, pin Object) (Object, error) {
	if !contains([]string{"review_only", "enforced"}, env("AGENT_KERNEL_ROLLOUT", "review_only")) {
		return nil, taskError("agent_snapshot_unavailable", "运行策略不支持，请配置 review_only 或 enforced")
	}
	schools, err := a.all(ctx, a.Pool, "core_school")
	if err != nil {
		return nil, err
	}
	tags, err := a.all(ctx, a.Pool, "core_schooltag")
	if err != nil {
		return nil, err
	}
	var fallback, nonTarget Object
	for _, tag := range tags {
		if !truth(tag["is_active"]) {
			continue
		}
		if truth(tag["is_default"]) && fallback == nil {
			fallback = tag
		}
		if tag["code"] == "NON_TARGET" || normalized(str(tag["name"])) == "非目标院校" {
			nonTarget = tag
		}
	}
	if nonTarget == nil {
		nonTarget, err = a.save(ctx, a.Pool, "core_schooltag", nil, Object{"code": "NON_TARGET", "name": "非目标院校", "is_default": false, "is_active": true})
		if err != nil {
			return nil, err
		}
		tags = append(tags, nonTarget)
	}
	if fallback == nil {
		fallback = nonTarget
	}
	schoolMap := map[string]Object{}
	for _, s := range schools {
		schoolMap[normalized(str(s["name"]))] = s
	}
	tagIDs := Object{}
	for _, tag := range tags {
		tagIDs[a.ref("tag", tag["id"])] = tag["id"]
	}
	c := brief(candidate, "highest_major", "highest_education", "household_province", "first_degree_school", "highest_degree_school")
	c["ref"] = token(16)
	for _, degree := range []string{"first", "highest"} {
		school := schoolMap[normalized(str(candidate[degree+"_degree_school"]))]
		tag := nonTarget
		if school != nil {
			tag = fallback
			if school["school_tag_id"] != nil {
				tag, _ = a.get(ctx, a.Pool, "core_schooltag", school["school_tag_id"])
			}
		}
		c[degree+"_degree_tag_ref"] = a.ref("tag", tag["id"])
		c[degree+"_degree_province"] = str(school["province"])
	}
	workflow, _ := one(ctx, a.Pool, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1", candidate["id"])
	resumes, err := rows(ctx, a.Pool, "SELECT row_to_json(r) FROM core_resume r WHERE candidate_id=$1 ORDER BY id", candidate["id"])
	if err != nil {
		return nil, err
	}
	rejected := map[int64]bool{}
	if workflow != nil {
		attempts, err := rows(ctx, a.Pool, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE workflow_id=$1 AND feedback_result='rejected'", workflow["id"])
		if err != nil {
			return nil, err
		}
		for _, at := range attempts {
			rejected[num(at["resume_id"])] = true
		}
	}
	volunteers := []any{}
	volunteerIDs := Object{}
	for _, resume := range resumes {
		ref := a.ref("volunteer", resume["id"])
		volunteerIDs[ref] = resume["id"]
		volunteers = append(volunteers, Object{"ref": ref, "entity": resume["entity"], "position_name": resume["position_name"], "apply_date": str(resume["apply_date"]), "rejected": rejected[num(resume["id"])], "artifact": Object{"path": resume["resume_file"]}})
	}
	jobs, err := rows(ctx, a.Pool, "SELECT row_to_json(j) FROM core_job j WHERE is_active ORDER BY id")
	if err != nil {
		return nil, err
	}
	jobValues := []any{}
	jobIDs := Object{}
	for _, job := range jobs {
		content, err := a.jobContent(ctx, a.Pool, job)
		if err != nil {
			return nil, err
		}
		hash := fingerprint(content)
		delete(content, "department_identity")
		delete(content, "is_active")
		ref := a.ref("job", job["id"])
		jobIDs[ref] = job["id"]
		content["ref"] = ref
		content["content_hash"] = hash
		content["department_ref"] = ""
		if job["department_id"] != nil {
			content["department_ref"] = a.ref("department", job["department_id"])
		}
		jobValues = append(jobValues, content)
	}
	rules, err := rows(ctx, a.Pool, "SELECT row_to_json(r) FROM core_schooltagrule r WHERE is_active ORDER BY priority,id")
	if err != nil {
		return nil, err
	}
	ruleValues := []any{}
	ruleIDs := Object{}
	for _, rule := range rules {
		ref := a.ref("rule", rule["id"])
		ruleIDs[ref] = rule["id"]
		value := Object{"ref": ref, "priority": rule["priority"]}
		for _, degree := range []string{"first", "highest"} {
			links, err := rows(ctx, a.Pool, "SELECT row_to_json(l) FROM core_schooltagruletag l WHERE rule_id=$1 AND degree_type=$2", rule["id"], degree)
			if err != nil {
				return nil, err
			}
			refs := []any{}
			for _, link := range links {
				refs = append(refs, a.ref("tag", link["school_tag_id"]))
			}
			value[degree+"_tag_refs"] = refs
		}
		links, err := rows(ctx, a.Pool, "SELECT row_to_json(l) FROM core_schooltagruleeducation l WHERE rule_id=$1", rule["id"])
		if err != nil {
			return nil, err
		}
		educations := []any{}
		for _, link := range links {
			educations = append(educations, link["education"])
		}
		value["educations"] = educations
		ruleValues = append(ruleValues, value)
	}
	aliases, err := rows(ctx, a.Pool, "SELECT row_to_json(a) FROM (SELECT a.name,c.name category,a.match_type FROM core_majoralias a JOIN core_majorcategory c ON c.id=a.category_id WHERE a.is_active AND c.is_active ORDER BY a.id) a")
	if err != nil {
		return nil, err
	}
	taxonomy := []any{}
	for _, alias := range aliases {
		taxonomy = append(taxonomy, alias)
	}
	wf := Object{"revision": num(workflow["revision"]), "current_rank": num(workflow["current_rank"]), "retry_volunteer_ref": ""}
	if retry := obj(run["scope"])["retry_resume_id"]; num(retry) > 0 {
		wf["retry_volunteer_ref"] = a.ref("volunteer", retry)
	}
	snapshot := Object{"candidate": c, "workflow": wf, "volunteers": volunteers, "jobs": jobValues, "admission_rules": ruleValues, "taxonomy": taxonomy}
	frozen := Object{"snapshot": snapshot, "volunteer_ids": volunteerIDs, "job_ids": jobIDs, "rule_ids": ruleIDs, "tag_ids": tagIDs, "task_id": token(16), "pin": pin, "lane": env("AGENT_KERNEL_ROLLOUT", "review_only"), "thresholds": Object{"dispatch": a.configValue(ctx, "ai_dispatch_threshold", .75), "review": a.configValue(ctx, "ai_review_threshold", .5)}}
	frozen["preflight"] = prepareSnapshot(snapshot)
	return frozen, nil
}
