package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var bulkCandidateFilters = []string{"system_status", "system_statuses", "pool_status", "current_entity_in", "current_position_name_in", "job_department_name_in", "current_job_category_in", "school_tag_in", "allocation_source", "reason_code"}

func validateBulkFilters(filters Object) error {
	nonempty := false
	for key, value := range filters {
		if !contains(bulkCandidateFilters, key) {
			return bad("不支持的批量筛选字段：" + key)
		}
		switch v := value.(type) {
		case nil:
		case string:
			nonempty = nonempty || strings.TrimSpace(v) != ""
		case []any:
			for _, item := range v {
				if _, ok := item.(string); !ok {
					return bad(key + " 必须是字符串数组")
				}
				nonempty = nonempty || strings.TrimSpace(str(item)) != ""
			}
		default:
			return bad(key + " 必须是字符串或字符串数组")
		}
	}
	if !nonempty {
		return bad("candidate_filters 至少包含一个非空筛选值")
	}
	return nil
}

func validateCandidateFilters(r *http.Request, p *Principal) error {
	q := r.URL.Query()
	if !p.has("resume.view") {
		for key := range q {
			if contains([]string{"processing_run_id", "processing_result", "workflow_status", "reason_code", "pool_status"}, key) || strings.HasPrefix(key, "analytics_") {
				return bad("部门接口人不可使用筛选字段：" + key)
			}
		}
	}
	for _, status := range queryValues(r, "pool_status") {
		if !contains([]string{"admitted", "none", "pending_allocation", "allocated", "needs_reanalysis"}, status) {
			return bad("不支持的入池状态：" + status)
		}
	}
	for _, key := range []string{"system_status", "system_statuses"} {
		values := []string{}
		for _, s := range queryValues(r, key) {
			if s == "pending_review" {
				values = append(values, "raw", "pending_allocation")
			} else {
				values = append(values, s)
			}
		}
		if len(values) > 0 {
			q.Set(key, strings.Join(values, ","))
		}
		for _, s := range values {
			if _, ok := systemLabels[s]; !ok {
				return bad("不支持的简历状态：" + s)
			}
		}
	}
	r.URL.RawQuery = q.Encode()
	for _, key := range []string{"current_department_id", "current_primary_department_id"} {
		for _, v := range queryValues(r, key) {
			if n, e := strconv.ParseInt(v, 10, 64); e != nil || n <= 0 {
				return bad(key + " 必须是正整数")
			}
		}
	}
	if q.Get("processing_run_id") != "" && q.Get("processing_result") != "" && !contains([]string{"success", "completed", "needs_attention", "failed", "review", "dispatch", "archive", "skipped", "cancelled"}, q.Get("processing_result")) {
		return bad("不支持的处理结果")
	}
	for _, key := range []string{"current_apply_date_from", "current_apply_date_to", "imported_after", "imported_before"} {
		if value := q.Get(key); value != "" {
			if _, e := time.Parse("2006-01-02", value); e != nil {
				return bad(key + " 日期格式必须为 YYYY-MM-DD")
			}
		}
	}
	if q.Get("current_apply_date_from") != "" && q.Get("current_apply_date_to") != "" && q.Get("current_apply_date_from") > q.Get("current_apply_date_to") {
		return bad("投递时间开始日期不能晚于结束日期")
	}
	return nil
}

// 在已经完成部门数据范围投影的记录上应用简历筛选，不能从不可见志愿中匹配。
func (a *App) candidateFilter(ctx context.Context, r *http.Request, p *Principal, row, value Object, key, q string) (bool, bool, error) {
	current, at := obj(value["current_resume"]), obj(value["current_attempt"])
	switch key {
	case "pool_status":
		member := obj(value["pool_membership"])
		status := str(member["status"])
		admitted := contains([]string{"pending_allocation", "allocated", "needs_reanalysis"}, status)
		for _, expected := range queryValues(r, key) {
			if expected == status || expected == "admitted" && admitted || expected == "none" && !admitted {
				return true, true, nil
			}
		}
		return false, true, nil
	case "search", "name":
		texts := []string{str(row["name"]), str(row["name_pinyin"]), str(row["name_pinyin_initials"])}
		if key == "search" {
			if p.has("resume.view") {
				texts = append(texts, str(row["phone"]))
				var resumes []Object
				var err error
				if data := candidateSummaries(ctx); data != nil {
					resumes = data.resumes[num(row["id"])]
				} else {
					resumes, err = rows(ctx, a.Pool, "SELECT row_to_json(r) FROM (SELECT apply_id,position_name FROM core_resume WHERE candidate_id=$1) r", row["id"])
				}
				if err != nil {
					return false, true, err
				}
				for _, v := range resumes {
					texts = append(texts, str(v["apply_id"]), str(v["position_name"]))
				}
			} else {
				texts = append(texts, str(current["apply_id"]), str(current["position_name"]))
			}
		}
		for _, text := range texts {
			if textMatches(text, q) {
				return true, true, nil
			}
		}
		return false, true, nil
	case "current_apply_date_from", "current_apply_date_to":
		date := str(value["current_apply_date"])
		return date != "" && (key == "current_apply_date_from" && date >= q || key == "current_apply_date_to" && date <= q), true, nil
	case "current_department_id", "current_primary_department_id":
		return contains(queryValues(r, key), str(value[key])), true, nil
	case "school_tag_in":
		for _, tag := range list(value["school_tags"]) {
			for _, expected := range queryValues(r, key) {
				if strings.EqualFold(str(obj(tag)["name"]), expected) {
					return true, true, nil
				}
			}
		}
		return false, true, nil
	case "school_tag":
		if textMatches(str(row["first_degree_platform"]), q) || textMatches(str(row["highest_degree_platform"]), q) {
			return true, true, nil
		}
		for _, tag := range list(value["school_tags"]) {
			if textMatches(str(obj(tag)["name"]), q) || textMatches(str(obj(tag)["code"]), q) {
				return true, true, nil
			}
		}
		return false, true, nil
	case "feedback_reason_code":
		return contains(queryValues(r, key), str(at["feedback_reason_code"])), true, nil
	case "reason_type":
		if q == "none" {
			q = ""
		}
		return str(value["reason_type"]) == q, true, nil
	case "workflow_status", "status":
		allowed := []string{}
		f, _ := fieldFor(a, "core_candidateworkflow", "status")
		for _, choice := range f.Choices {
			allowed = append(allowed, str(choice[0]))
		}
		expected := []string{}
		for _, s := range queryValues(r, key) {
			if contains(allowed, s) {
				expected = append(expected, s)
			}
		}
		actual := str(value["workflow_status"])
		if actual == "" {
			actual = "pending"
		}
		return len(expected) == 0 || contains(expected, actual), true, nil
	}
	return false, false, nil
}

func drillValues(r *http.Request, key string) ([]string, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil, nil
	}
	var values []any
	if json.Unmarshal([]byte(raw), &values) != nil || len(values) > 20 {
		return nil, bad(key + " 必须是不超过 20 项的字符串或整数数组")
	}
	out := []string{}
	for _, v := range values {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case float64:
			if x != float64(int64(x)) {
				return nil, bad(key + " 必须是字符串或整数数组")
			}
			out = append(out, str(int64(x)))
		default:
			return nil, bad(key + " 必须是字符串或整数数组")
		}
	}
	return out, nil
}
func (a *App) drilldownIDs(ctx context.Context, r *http.Request) (map[int64]bool, error) {
	dim := strings.TrimSpace(r.URL.Query().Get("analytics_dimension"))
	if dim == "" {
		return nil, nil
	}
	if !contains([]string{"candidate", "classified", "allocated", "dispatched", "feedback", "passed", "archived", "source", "ai_recommendation", "ai_error", "job", "primary_department", "department", "school_tag", "education", "archive_reason", "rejection_reason"}, dim) {
		return nil, bad("analytics_dimension 不是有效的看板下钻类型")
	}
	values, err := drillValues(r, "analytics_values")
	if err != nil {
		return nil, err
	}
	labels, err := drillValues(r, "analytics_value_labels")
	if err != nil {
		return nil, err
	}
	if len(labels) > 0 && len(labels) != len(values) {
		return nil, bad("analytics_value_labels 与 analytics_values 数量不一致")
	}
	if contains([]string{"source", "ai_recommendation", "ai_error", "job", "primary_department", "department", "school_tag", "education", "archive_reason", "rejection_reason"}, dim) && len(values) == 0 {
		return nil, bad("当前看板下钻类型必须提供 analytics_values")
	}
	matches := func(key any, label string) bool {
		for i, v := range values {
			if v == str(key) && (len(labels) == 0 || labels[i] == "" || labels[i] == label) {
				return true
			}
		}
		return false
	}
	filterReq := r.Clone(ctx)
	u := *r.URL
	filterReq.URL = &u
	q := url.Values{}
	for _, key := range []string{"date_from", "date_to", "entity", "job_id", "primary_department_id", "department_id", "school_tag_id", "education", "source"} {
		q.Set(key, r.URL.Query().Get("analytics_"+key))
	}
	u.RawQuery = q.Encode()
	filters, start, end, err := a.analyticsFilters(filterReq)
	if err != nil {
		return nil, err
	}
	data, err := a.loadAnalytics(ctx)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	for _, c := range data.cohort(filters, start, end) {
		hit := false
		at := c.Attempt
		switch dim {
		case "candidate":
			hit = true
		case "classified":
			hit = str(c.Resume["job_category"]) != "" && (len(c.Tags) > 0 || str(c.Candidate["first_degree_platform"]) != "" || str(c.Candidate["highest_degree_platform"]) != "")
		case "allocated":
			hit = at != nil
		case "dispatched":
			hit = contains([]string{"dispatched", "passed", "rejected"}, str(at["status"]))
		case "feedback":
			hit = contains([]string{"passed", "rejected"}, str(at["status"]))
		case "passed":
			hit = at["status"] == "passed"
		case "source":
			hit = at != nil && contains(values, str(at["source"]))
		case "archived":
			hit = c.Workflow["status"] == "archived"
		case "archive_reason":
			hit = c.Workflow["status"] == "archived" && contains(values, str(c.Workflow["archive_reason"]))
		case "education":
			hit = contains(values, str(c.Candidate["highest_education"]))
		case "rejection_reason":
			hit = at["status"] == "rejected" && contains(values, str(at["feedback_reason_code"]))
		case "ai_recommendation", "ai_error":
			for _, d := range data.Decisions[num(c.Effective["id"])] {
				key := "recommendation"
				if dim == "ai_error" {
					key = "error_code"
				}
				v := str(d[key])
				// 保留历史统计筛选的含义，平台不再执行专项路由。
				if dim == "ai_error" && v == "ai_special_route_unavailable" {
					v = "ai_connection_error"
				}
				hit = hit || contains(values, v)
			}
		case "job":
			job := data.Jobs[num(c.Effective["job_id"])]
			label := str(job["public_name"])
			if label == "" {
				label = str(job["position_name"])
			}
			if label == "" {
				label = str(c.Effective["position_name"])
			}
			if label == "" {
				label = "未分类"
			}
			key := job["id"]
			if key == nil {
				key = "text:" + label
			}
			hit = matches(key, label)
		case "school_tag":
			for _, tag := range c.Tags {
				hit = hit || matches(tag["id"], str(tag["name"]))
			}
		case "primary_department", "department":
			if at != nil {
				primary, secondary, _ := data.hierarchy(at["current_department_id"])
				dep := secondary
				fallback := "未分配"
				if dim == "primary_department" {
					dep = primary
					fallback = "未归属一级部门"
				}
				label := str(dep["name"])
				if label == "" {
					label = fallback
				}
				key := dep["id"]
				if key == nil {
					key = "text:" + label
				}
				hit = matches(key, label)
			}
		}
		if hit {
			ids[num(c.Candidate["id"])] = true
		}
	}
	return ids, nil
}
