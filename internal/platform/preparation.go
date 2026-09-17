package platform

import (
	"context"
	"errors"
	"fmt"
)

// preparationData is read-only after loading and belongs to one preparation pass.
// Each candidate still gets its own workflow, volunteers, preflight and task ID.
type preparationData struct {
	schools, tags           map[string]Object
	fallback, nonTarget     Object
	jobs, rules             []any
	jobIDs, ruleIDs, tagIDs Object
	thresholds              Object
	policy                  Object
	lane                    string
}

func (a *App) loadPreparationData(ctx context.Context) (*preparationData, error) {
	lane := env("AGENT_KERNEL_ROLLOUT", "enforced")
	if !contains([]string{"review_only", "enforced"}, lane) {
		return nil, taskError("agent_snapshot_unavailable", "运行策略不支持，请配置 review_only 或 enforced")
	}
	d := &preparationData{schools: map[string]Object{}, tags: map[string]Object{}, jobs: []any{}, rules: []any{}, jobIDs: Object{}, ruleIDs: Object{}, tagIDs: Object{}, lane: lane}
	schools, err := a.all(ctx, a.Pool, "core_school")
	if err != nil {
		return nil, err
	}
	for _, school := range schools {
		d.schools[normalized(str(school["name"]))] = school
	}
	tags, err := a.all(ctx, a.Pool, "core_schooltag")
	if err != nil {
		return nil, err
	}
	for _, tag := range tags {
		if !truth(tag["is_active"]) {
			continue
		}
		if truth(tag["is_default"]) && d.fallback == nil {
			d.fallback = tag
		}
		if tag["code"] == "NON_TARGET" || normalized(str(tag["name"])) == "非目标院校" {
			d.nonTarget = tag
		}
	}
	if d.nonTarget == nil {
		d.nonTarget, err = a.save(ctx, a.Pool, "core_schooltag", nil, Object{"code": "NON_TARGET", "name": "非目标院校", "is_default": false, "is_active": true})
		if err != nil {
			return nil, err
		}
		tags = append(tags, d.nonTarget)
	}
	if d.fallback == nil {
		d.fallback = d.nonTarget
	}
	for _, tag := range tags {
		d.tags[str(tag["id"])] = tag
		d.tagIDs[a.ref("tag", tag["id"])] = tag["id"]
	}

	jobs, err := rows(ctx, a.Pool, "SELECT row_to_json(j) FROM core_job j WHERE is_active ORDER BY id")
	if err != nil {
		return nil, err
	}
	departments, err := a.all(ctx, a.Pool, "core_department")
	if err != nil {
		return nil, err
	}
	departmentByID := map[string]Object{}
	for _, department := range departments {
		departmentByID[str(department["id"])] = department
	}
	majors, err := rows(ctx, a.Pool, "SELECT row_to_json(m) FROM core_jobmajor m JOIN core_job j ON j.id=m.job_id WHERE j.is_active ORDER BY m.job_id,m.major,m.id")
	if err != nil {
		return nil, err
	}
	majorsByJob := map[string][]Object{}
	for _, major := range majors {
		key := str(major["job_id"])
		majorsByJob[key] = append(majorsByJob[key], major)
	}
	for _, job := range jobs {
		content := jobContentFromRelations(job, departmentByID[str(job["department_id"])], majorsByJob[str(job["id"])])
		hash := fingerprint(content)
		delete(content, "department_identity")
		delete(content, "is_active")
		ref := a.ref("job", job["id"])
		d.jobIDs[ref] = job["id"]
		content["ref"] = ref
		content["content_hash"] = hash
		content["department_ref"] = ""
		if job["department_id"] != nil {
			content["department_ref"] = a.ref("department", job["department_id"])
		}
		d.jobs = append(d.jobs, content)
	}

	rules, err := rows(ctx, a.Pool, "SELECT row_to_json(r) FROM core_schooltagrule r WHERE is_active ORDER BY priority,id")
	if err != nil {
		return nil, err
	}
	tagLinks, err := rows(ctx, a.Pool, "SELECT row_to_json(l) FROM core_schooltagruletag l JOIN core_schooltagrule r ON r.id=l.rule_id WHERE r.is_active ORDER BY l.id")
	if err != nil {
		return nil, err
	}
	tagsByRule := map[string]map[string][]any{}
	for _, link := range tagLinks {
		key, degree := str(link["rule_id"]), str(link["degree_type"])
		if tagsByRule[key] == nil {
			tagsByRule[key] = map[string][]any{}
		}
		tagsByRule[key][degree] = append(tagsByRule[key][degree], a.ref("tag", link["school_tag_id"]))
	}
	educationLinks, err := rows(ctx, a.Pool, "SELECT row_to_json(l) FROM core_schooltagruleeducation l JOIN core_schooltagrule r ON r.id=l.rule_id WHERE r.is_active ORDER BY l.id")
	if err != nil {
		return nil, err
	}
	educationsByRule := map[string][]any{}
	for _, link := range educationLinks {
		key := str(link["rule_id"])
		educationsByRule[key] = append(educationsByRule[key], link["education"])
	}
	for _, rule := range rules {
		ref, key := a.ref("rule", rule["id"]), str(rule["id"])
		d.ruleIDs[ref] = rule["id"]
		value := Object{"ref": ref, "priority": rule["priority"], "educations": append([]any{}, educationsByRule[key]...)}
		for _, degree := range []string{"first", "highest"} {
			value[degree+"_tag_refs"] = append([]any{}, tagsByRule[key][degree]...)
		}
		d.rules = append(d.rules, value)
	}
	policy, err := a.poolPolicy(ctx, a.Pool)
	if err != nil {
		return nil, err
	}
	d.policy = obj(policy["policy"])
	d.thresholds = Object{"dispatch": a.configValue(ctx, "ai_dispatch_threshold", .75)}
	return d, nil
}

func (a *App) freezeCase(ctx context.Context, run, candidate, pin Object, data *preparationData) (Object, error) {
	c := brief(candidate, "highest_major", "highest_education", "household_province", "first_degree_school", "highest_degree_school")
	c["ref"] = token(16)
	for _, degree := range []string{"first", "highest"} {
		school := data.schools[normalized(str(candidate[degree+"_degree_school"]))]
		tag := data.nonTarget
		if school != nil {
			tag = data.fallback
			if school["school_tag_id"] != nil {
				tag = data.tags[str(school["school_tag_id"])]
			}
		}
		c[degree+"_degree_tag_ref"] = a.ref("tag", tag["id"])
		c[degree+"_degree_province"] = str(school["province"])
	}
	workflow, err := one(ctx, a.Pool, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1", candidate["id"])
	var notFound *apiError
	if err != nil && (!errors.As(err, &notFound) || notFound.Status != 404) {
		return nil, err
	}
	resumes, err := rows(ctx, a.Pool, "SELECT row_to_json(r) FROM core_resume r WHERE candidate_id=$1 ORDER BY id", candidate["id"])
	if err != nil {
		return nil, err
	}
	rejected := map[int64]bool{}
	if workflow != nil {
		attempts, err := rows(ctx, a.Pool, "SELECT row_to_json(a) FROM platform_applications a WHERE candidate_id=$1 AND status IN ('ai_rejected','department_rejected','review_rejected','passed')", candidate["id"])
		if err != nil {
			return nil, err
		}
		for _, at := range attempts {
			rejected[num(at["resume_id"])] = true
		}
	}
	volunteers, volunteerIDs := []any{}, Object{}
	for _, resume := range resumes {
		ref := a.ref("volunteer", resume["id"])
		volunteerIDs[ref] = resume["id"]
		volunteers = append(volunteers, Object{"ref": ref, "entity": resume["entity"], "position_name": resume["position_name"], "apply_date": str(resume["apply_date"]), "rejected": rejected[num(resume["id"])], "artifact": Object{"path": resume["resume_file"]}})
	}
	wf := Object{"revision": num(workflow["revision"]), "current_rank": num(workflow["current_rank"]), "retry_volunteer_ref": ""}
	if retry := obj(run["scope"])["retry_resume_id"]; num(retry) > 0 {
		wf["retry_volunteer_ref"] = a.ref("volunteer", retry)
	}
	snapshot := Object{"candidate": c, "workflow": wf, "volunteers": volunteers, "jobs": data.jobs, "admission_rules": data.rules, "pool_policy": data.policy}
	frozen := Object{"snapshot": snapshot, "volunteer_ids": volunteerIDs, "job_ids": data.jobIDs, "rule_ids": data.ruleIDs, "tag_ids": data.tagIDs, "task_id": token(16), "pin": pin, "lane": data.lane, "thresholds": data.thresholds}
	frozen["preflight"] = prepareSnapshot(snapshot)
	configureAssessmentSnapshot(frozen)
	return frozen, nil
}

func configureAssessmentSnapshot(frozen Object) {
	snapshot := obj(frozen["snapshot"])
	policy := obj(snapshot["pool_policy"])
	d := obj(frozen["preflight"])
	snapshot["jobs"] = []any{}
	snapshot["tag_catalog"] = []any{}
	frozen["job_ids"] = Object{}
	if d["status"] == "ready" {
		standard := policyItem(policy, "standards", str(d["standard_code"]))
		snapshot["jobs"] = []any{standardJob(policy, standard)}
		tags := []any{}
		for _, code := range stringValues(standard["tag_codes"]) {
			if tag := policyItem(policy, "tags", code); activePolicyItem(tag) {
				tags = append(tags, brief(tag, "code", "name", "category", "description"))
			}
		}
		snapshot["tag_catalog"] = tags
	}
}

func (a *App) preparationProgress(ctx context.Context, run Object, processed, total int) error {
	if str(run["step"]) == "step1" {
		return nil
	}
	message := fmt.Sprintf("已准备 %d / %d 名候选人", processed, total)
	_, err := a.Pool.Exec(ctx, "UPDATE core_processingrunstage SET processed_count=$2,success_count=$2,message=$3 WHERE run_id=$1 AND step='preparing' AND status='running'", run["id"], processed, message)
	return err
}

func (a *App) prepareRunSnapshots(ctx context.Context, run, pin Object) error {
	// Recover counts without reading every previously saved (potentially large) snapshot.
	items, err := rows(ctx, a.Pool, "SELECT json_build_object('id',id,'candidate_id',candidate_id,'status',status,'prepared',kernel_snapshot<>'{}'::jsonb) FROM core_processingrunscopeitem WHERE run_id=$1 ORDER BY candidate_id", run["id"])
	if err != nil {
		return err
	}
	processed := 0
	for _, item := range items {
		if terminalItem(str(item["status"])) || truth(item["prepared"]) {
			processed++
		}
	}
	if err := a.preparationProgress(ctx, run, processed, len(items)); err != nil {
		return err
	}
	var data *preparationData
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if terminalItem(str(item["status"])) || truth(item["prepared"]) {
			continue
		}
		if data == nil {
			data, err = a.loadPreparationData(ctx)
			if err != nil {
				return err
			}
		}
		candidate, err := a.get(ctx, a.Pool, "core_candidate", item["candidate_id"])
		if err != nil {
			return err
		}
		frozen, err := a.freezeCase(ctx, run, candidate, pin, data)
		if err != nil {
			return err
		}
		d := obj(frozen["preflight"])
		values := Object{"kernel_snapshot": frozen, "workflow_revision_at_prepare": obj(obj(frozen["snapshot"])["workflow"])["revision"], "prepared_resume_id": obj(frozen["volunteer_ids"])[str(d["current_volunteer_ref"])], "matched_rule_id": obj(frozen["rule_ids"])[str(d["admission_rule_ref"])]}
		if _, err = a.save(ctx, a.Pool, "core_processingrunscopeitem", item["id"], values); err != nil {
			return err
		}
		processed++
		if err := a.preparationProgress(ctx, run, processed, len(items)); err != nil {
			return err
		}
	}
	return nil
}
