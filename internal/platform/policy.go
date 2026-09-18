package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

const protocolVersion = "resume-analysis/v5"
const resultVersion = "resume-application-assessment/v1"
const policyVersion = "application-pool-policy/v1"

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
	return resolveApplicationStandard(s, current, d)
}
func (a *App) jobContent(ctx context.Context, db DB, j Object) (Object, error) {
	dep, _ := a.get(ctx, db, "core_department", j["department_id"])
	majors, err := rows(ctx, db, "SELECT row_to_json(m) FROM core_jobmajor m WHERE job_id=$1 ORDER BY major,id", j["id"])
	if err != nil {
		return nil, err
	}
	return jobContentFromRelations(j, dep, majors), nil
}

func jobContentFromRelations(j, dep Object, majors []Object) Object {
	content := brief(j, "entity", "public_name", "position_name", "category", "job_family", "location", "education", "responsibilities", "is_active")
	content["department_name"] = str(dep["name"])
	content["department_level"] = num(dep["level"])
	content["department_identity"] = j["department_id"]
	names := []any{}
	for _, major := range majors {
		names = append(names, major["major"])
	}
	content["required_majors"] = names
	return content
}
