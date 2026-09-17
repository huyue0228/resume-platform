package platform

import "testing"

func configurationJob(id int, entity, posting, internal, requirement string) Object {
	return Object{"id": id, "entity": entity, "public_name": posting, "position_name": internal, "responsibilities": requirement, "education": "本科", "required_majors": []any{"机械工程"}, "is_active": true, "department_level": 2, "reception_state": "receiving"}
}

func TestJobPolicyDerivationPreservesPostingBoundariesAndExplicitRules(t *testing.T) {
	jobs := []Object{configurationJob(1, "A", "机械校招", "研发", "机械设计"), configurationJob(2, "A", "软件校招", "研发", "软件开发"), configurationJob(3, "B", "机械校招", "研发", "机械设计")}
	p := deriveJobPolicy(emptyPoolPolicy(), jobs)
	if len(list(p["standards"])) != 3 || len(list(p["pools"])) != 2 {
		t.Fatal("postings or entities merged", p)
	}
	d := resolveApplicationStandard(Object{"pool_policy": p}, Object{"entity": "A", "position_name": "机械校招"}, Object{})
	s := policyItem(p, "standards", str(d["standard_code"]))
	if s["responsibilities"] != "机械设计" || d["status"] != "ready" {
		t.Fatal("assessment expanded", d, s)
	}
	if issue := allocationConfigurationIssue(p, s, jobs); issue != "allocation_rules_missing" {
		t.Fatal("draft rules treated as ready", issue)
	}
	if len(list(p["tags"])) != 0 {
		t.Fatal("invented ability tags")
	}
	obj(list(p["rules"])[0])["priority"] = 7
	again := deriveJobPolicy(p, jobs)
	if fingerprint(p) != fingerprint(again) {
		t.Fatal("generation is not idempotent")
	}
}

func TestJobPolicyConflictsRequireExplicitSourceAndTrackChanges(t *testing.T) {
	jobs := []Object{configurationJob(1, "A", "机械校招", "机械", "机械设计"), configurationJob(2, "A", "机械校招", "机械", "产品验证")}
	p := deriveJobPolicy(emptyPoolPolicy(), jobs)
	s := obj(list(p["standards"])[0])
	resolve := func(p Object) Object {
		return resolveApplicationStandard(Object{"pool_policy": p}, Object{"entity": "A", "position_name": "机械校招"}, Object{})
	}
	if resolve(p)["status"] != "source_job_conflict" {
		t.Fatal("conflicting requirements guessed")
	}
	s["source_job_id"] = 2
	p = deriveJobPolicy(p, jobs)
	s = obj(list(p["standards"])[0])
	if resolve(p)["status"] != "ready" || s["responsibilities"] != "产品验证" {
		t.Fatal("explicit source not honored", p)
	}
	before := standardJob(p, s)["content_hash"]
	jobs[1]["responsibilities"] = "产品验证和测试"
	p = deriveJobPolicy(p, jobs)
	if standardJob(p, obj(list(p["standards"])[0]))["content_hash"] == before {
		t.Fatal("source change did not invalidate assessment")
	}
	jobs[1]["is_active"] = false
	p = deriveJobPolicy(p, jobs)
	if resolve(p)["status"] != "source_job_missing" {
		t.Fatal("inactive selected source silently replaced")
	}
}

func TestJobPolicyMissingRequirementsAndMovedOrDeletedDemand(t *testing.T) {
	jobs := []Object{configurationJob(1, "A", "机械校招", "机械", "")}
	p := deriveJobPolicy(emptyPoolPolicy(), jobs)
	if obj(list(p["standards"])[0])["configuration_issue"] != "job_responsibility_missing" {
		t.Fatal("missing requirements shown as ready")
	}
	obj(list(p["rules"])[0])["active"] = true
	jobs[0]["entity"], jobs[0]["responsibilities"] = "B", "机械设计"
	p = deriveJobPolicy(p, jobs)
	rule := obj(list(p["rules"])[0])
	if rule["active"] != false || policyItem(p, "pools", str(rule["pool_code"]))["entity"] != "B" {
		t.Fatal("entity change retained active cross-entity routing")
	}
	p = deriveJobPolicy(p, nil)
	if len(list(p["rules"])) != 0 {
		t.Fatal("deleted job left dangling rule")
	}
	for _, value := range list(p["standards"]) {
		if obj(value)["configuration_issue"] != "source_job_missing" {
			t.Fatal("missing source is not reported")
		}
	}
}
