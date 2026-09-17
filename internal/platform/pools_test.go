package platform

import "testing"

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
