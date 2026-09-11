package platform

import "testing"

func TestPoolAllocationRequiresConfirmedTagsAndStaysInPoolAndEntity(t *testing.T) {
	policy := Object{"tags": []any{Object{"code": "cad"}, Object{"code": "design"}, Object{"code": "disabled", "active": false}}, "pools": []any{Object{"code": "mechanical", "entity": "YLS"}}, "rules": []any{
		Object{"job_id": 1, "pool_code": "software", "required_tags": []any{"cad"}},
		Object{"job_id": 2, "pool_code": "mechanical", "required_tags": []any{"cad"}},
		Object{"job_id": 3, "pool_code": "mechanical", "required_tags": []any{"cad"}, "preferred_tags": []any{"design"}, "priority": 5},
		Object{"job_id": 4, "pool_code": "mechanical", "required_tags": []any{"cad"}, "preferred_tags": []any{"design"}, "priority": 1},
		Object{"job_id": 5, "pool_code": "mechanical", "required_tags": []any{"disabled"}},
		Object{"job_id": 6, "pool_code": "mechanical", "required_tags": []any{"cad", "unverified"}},
		Object{"job_id": 7, "pool_code": "mechanical", "required_tags": []any{"cad"}},
	}}
	jobs := map[int64]Object{}
	for i := int64(1); i <= 7; i++ {
		jobs[i] = Object{"id": i, "entity": "YLS", "is_active": true}
	}
	jobs[2]["entity"] = "GW"
	member := Object{"pool_code": "mechanical", "tags": []any{Object{"code": "cad", "confidence": .9, "status": "supported"}, Object{"code": "design", "confidence": 1, "status": "supported", "source": "manual"}, Object{"code": "disabled", "confidence": 1, "status": "supported"}, Object{"code": "unverified", "confidence": 1, "status": "needs_verification"}}}
	options := eligiblePoolRules(policy, member, jobs)
	if len(options) != 3 || num(options[0].job["id"]) != 4 || num(options[1].job["id"]) != 3 || num(options[2].job["id"]) != 7 {
		t.Fatalf("pool/tag/rank boundaries violated: %+v", options)
	}
	obj(list(member["tags"])[0])["confidence"] = .7
	if len(eligiblePoolRules(policy, member, jobs)) != 0 {
		t.Fatal("low confidence met a required tag")
	}
}

func TestApplicationMappingDoesNotExpandByJobNames(t *testing.T) {
	policy := Object{"pools": []any{Object{"code": "mechanical", "entity": "YLS"}, Object{"code": "software", "entity": "YLS"}}, "standards": []any{
		Object{"code": "mech_standard", "pool_code": "mechanical", "entity": "YLS", "application_names": []any{"机械工程师（校招）"}, "responsibilities": "机械设计与验证"},
		Object{"code": "soft_standard", "pool_code": "software", "entity": "YLS", "application_names": []any{"软件工程师"}, "responsibilities": "软件开发"},
	}}
	snapshot := Object{"pool_policy": policy, "jobs": []any{Object{"public_name": "机械工程师（校招）", "position_name": "软件工程师"}}}
	d := resolveApplicationStandard(snapshot, Object{"entity": "YLS", "position_name": "机械工程师（校招）"}, Object{})
	if d["standard_code"] != "mech_standard" || d["pool_code"] != "mechanical" || len(list(d["job_refs"])) != 1 {
		t.Fatalf("expanded outside submitted application: %v", d)
	}
	d = resolveApplicationStandard(snapshot, Object{"entity": "YLS", "position_name": "未映射名称"}, Object{})
	if d["status"] != "assessment_standard_missing" {
		t.Fatal("unmapped application guessed a pool")
	}
}
