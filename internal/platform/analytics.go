package platform

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

var pageKeys = []string{"/analytics", "/processing-tasks", "/resumes", "/jobs", "/schools", "/departments", "/config", "/ai-connection", "/users"}

func metricsAuthorized(r *http.Request) bool {
	expected := os.Getenv("USAGE_METRICS_TOKEN")
	provided := r.Header.Get("X-Usage-Metrics-Key")
	return expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}
func (a *App) analytics(w http.ResponseWriter, r *http.Request, path string, p *Principal) error {
	if path == "analytics/usage/page-view" {
		if p == nil {
			return &apiError{401, "需要登录"}
		}
		if r.Method != "POST" {
			return &apiError{405, "请求方法不允许"}
		}
		body, err := readBody(w, r)
		if err != nil {
			return err
		}
		valid := regexp.MustCompile(`(?i)^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$`)
		if !valid.MatchString(str(body["event_id"])) || !valid.MatchString(str(body["session_id"])) || !contains(pageKeys, str(body["page_key"])) {
			return bad("页面事件、会话 ID 或页面键无效")
		}
		tag, err := a.Pool.Exec(r.Context(), "INSERT INTO core_usagepageview(event_id,session_id,user_id,employee_no_snapshot,page_key,occurred_at) VALUES($1::uuid,$2::uuid,$3,$4,$5,now()) ON CONFLICT(event_id) DO NOTHING", body["event_id"], body["session_id"], p.User["id"], p.User["username"], body["page_key"])
		if err != nil {
			return err
		}
		status := 201
		if tag.RowsAffected() == 0 {
			status = 200
		}
		write(w, status, Object{"accepted": true, "duplicate": tag.RowsAffected() == 0})
		return nil
	}
	if r.Method != "GET" {
		return &apiError{405, "请求方法不允许"}
	}
	if !p.has("analytics.view") && !(path == "analytics/usage/overview" && metricsAuthorized(r)) {
		return &apiError{403, "无分析数据查看权限"}
	}
	if path == "analytics/usage/overview" {
		return a.usageOverview(w, r)
	}
	if path != "analytics/recruitment-overview" {
		return &apiError{404, "未找到"}
	}
	filters, start, end, err := a.analyticsFilters(r)
	if err != nil {
		return err
	}
	cacheKey := "resume:go:analytics:v2:" + fingerprint(filters)
	if raw, err := a.Redis.Get(r.Context(), cacheKey).Bytes(); err == nil {
		var cached Object
		if json.Unmarshal(raw, &cached) == nil {
			write(w, 200, cached)
			return nil
		}
	}
	data, err := a.loadAnalytics(r.Context())
	if err != nil {
		return err
	}
	cohort := data.cohort(filters, start, end)
	payload := a.recruitmentOverview(data, cohort, filters, start, end)
	a.Redis.Set(r.Context(), cacheKey, canonicalJSON(payload, false), 5*time.Minute)
	write(w, 200, payload)
	return nil
}
func (a *App) usageOverview(w http.ResponseWriter, r *http.Request) error {
	start, end, err := dateRange(r, "date_from", "date_to", 90, false)
	if err != nil {
		return err
	}
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "day"
	}
	if !contains([]string{"hour", "day", "week"}, granularity) {
		return bad("granularity 必须是 hour、day 或 week")
	}
	page := r.URL.Query().Get("page")
	if page != "" && !contains(pageKeys, page) {
		return bad("page 不是允许统计的页面键")
	}
	asOf := time.Now()
	where := "occurred_at>=$1 AND occurred_at<$2 AND occurred_at<=$3 AND ($4='' OR page_key=$4)"
	args := []any{start, end, asOf, page}
	metrics := "count(*) page_views,count(DISTINCT session_id) sessions,count(DISTINCT employee_no_snapshot) FILTER(WHERE employee_no_snapshot<>'') active_users"
	summary, err := one(r.Context(), a.Pool, "SELECT row_to_json(t) FROM (SELECT "+metrics+" FROM core_usagepageview WHERE "+where+") t", args...)
	if err != nil {
		return err
	}
	ranking, err := rows(r.Context(), a.Pool, "SELECT row_to_json(t) FROM (SELECT page_key,"+metrics+" FROM core_usagepageview WHERE "+where+" GROUP BY page_key ORDER BY page_views DESC,page_key) t", args...)
	if err != nil {
		return err
	}
	args = append(args, granularity)
	buckets, err := rows(r.Context(), a.Pool, "SELECT row_to_json(t) FROM (SELECT to_char(date_trunc($5,occurred_at AT TIME ZONE 'Asia/Shanghai'),'YYYY-MM-DD HH24:MI:SS') bucket,"+metrics+" FROM core_usagepageview WHERE "+where+" GROUP BY bucket ORDER BY bucket) t", args...)
	if err != nil {
		return err
	}
	byBucket := map[string]Object{}
	for _, b := range buckets {
		byBucket[str(b["bucket"])] = b
	}
	trend := []Object{}
	cursor := start
	step := 24 * time.Hour
	if granularity == "hour" {
		step = time.Hour
	}
	if granularity == "week" {
		step = 7 * 24 * time.Hour
		cursor = start.AddDate(0, 0, -(int(start.Weekday())+6)%7)
	}
	for cursor.Before(end) {
		key := cursor.Format("2006-01-02 15:04:05")
		bucket := byBucket[key]
		if bucket == nil {
			bucket = Object{"page_views": 0, "sessions": 0, "active_users": 0}
		}
		bucket["bucket"] = cursor.Format(time.RFC3339)
		trend = append(trend, bucket)
		cursor = cursor.Add(step)
	}
	var pageValue any
	if page != "" {
		pageValue = page
	}
	write(w, 200, Object{"data_as_of": asOf.In(shanghai).Format(time.RFC3339Nano), "filters": Object{"date_from": start.Format("2006-01-02"), "date_to": end.AddDate(0, 0, -1).Format("2006-01-02"), "granularity": granularity, "page": pageValue}, "summary": summary, "trend": trend, "page_ranking": ranking})
	return nil
}

type analyticsData struct {
	Candidates, Resumes, Workflows, Jobs, Departments, Tags map[int64]Object
	ResumeOrder                                             []Object
	WorkflowByCandidate, AttemptByResume                    map[int64]Object
	Decisions, Events, TagsByCandidate                      map[int64][]Object
}

func indexObjects(values []Object) map[int64]Object {
	out := map[int64]Object{}
	for _, v := range values {
		out[num(v["id"])] = v
	}
	return out
}
func (a *App) loadAnalytics(ctx context.Context) (*analyticsData, error) {
	d := &analyticsData{WorkflowByCandidate: map[int64]Object{}, AttemptByResume: map[int64]Object{}, Decisions: map[int64][]Object{}, Events: map[int64][]Object{}, TagsByCandidate: map[int64][]Object{}}
	// 同一只读事务建立看板快照，防止统计中途的转派使总数与分布不一致。
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		return nil, err
	}
	for table, dest := range map[string]*map[int64]Object{"core_candidate": &d.Candidates, "core_resume": &d.Resumes, "core_candidateworkflow": &d.Workflows, "core_job": &d.Jobs, "core_department": &d.Departments, "core_schooltag": &d.Tags} {
		values, err := a.all(ctx, tx, table)
		if err != nil {
			return nil, err
		}
		*dest = indexObjects(values)
		if table == "core_resume" {
			d.ResumeOrder = values
		}
	}
	sort.SliceStable(d.ResumeOrder, func(i, j int) bool {
		x, y := d.ResumeOrder[i], d.ResumeOrder[j]
		xr, yr := num(x["volunteer_rank"]), num(y["volunteer_rank"])
		if x["volunteer_rank"] == nil {
			xr = 1 << 30
		}
		if y["volunteer_rank"] == nil {
			yr = 1 << 30
		}
		if xr != yr {
			return xr < yr
		}
		if str(x["apply_date"]) != str(y["apply_date"]) {
			return str(x["apply_date"]) < str(y["apply_date"])
		}
		return num(x["id"]) < num(y["id"])
	})
	for _, v := range d.Workflows {
		d.WorkflowByCandidate[num(v["candidate_id"])] = v
	}
	attempts, err := rows(ctx, tx, "SELECT row_to_json(a) FROM core_assignmentattempt a WHERE status<>'cancelled' ORDER BY attempt_no,id")
	if err != nil {
		return nil, err
	}
	for _, v := range attempts {
		d.AttemptByResume[num(v["resume_id"])] = v
	}
	decisions, err := rows(ctx, tx, "SELECT row_to_json(d) FROM (SELECT id,resume_id,recommendation,error_code FROM core_agentdispatchdecision ORDER BY id) d")
	if err != nil {
		return nil, err
	}
	for _, v := range decisions {
		d.Decisions[num(v["resume_id"])] = append(d.Decisions[num(v["resume_id"])], v)
	}
	events, err := a.all(ctx, tx, "core_assignmenthandlingevent")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i]["occurred_at"] == events[j]["occurred_at"] {
			return num(events[i]["id"]) < num(events[j]["id"])
		}
		return parseTime(events[i]["occurred_at"]).Before(parseTime(events[j]["occurred_at"]))
	})
	for _, e := range events {
		d.Events[num(e["attempt_id"])] = append(d.Events[num(e["attempt_id"])], e)
	}
	links, err := rows(ctx, tx, "SELECT row_to_json(l) FROM core_candidate_school_tags l ORDER BY id")
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if t := d.Tags[num(l["schooltag_id"])]; t != nil {
			d.TagsByCandidate[num(l["candidate_id"])] = append(d.TagsByCandidate[num(l["candidate_id"])], t)
		}
	}
	for id, c := range d.Candidates {
		if len(d.TagsByCandidate[id]) == 0 {
			seen := map[int64]bool{}
			for _, degree := range []string{"highest", "first"} {
				tid := num(c[degree+"_degree_tag_id"])
				if t := d.Tags[tid]; t != nil && !seen[tid] {
					d.TagsByCandidate[id] = append(d.TagsByCandidate[id], t)
					seen[tid] = true
				}
			}
		}
	}
	return d, tx.Commit(ctx)
}
func (d *analyticsData) hierarchy(id any) (Object, Object, Object) {
	var primary, secondary, tertiary Object
	seen := map[int64]bool{}
	for dep := d.Departments[num(id)]; dep != nil && !seen[num(dep["id"])]; dep = d.Departments[num(dep["parent_id"])] {
		seen[num(dep["id"])] = true
		switch num(dep["level"]) {
		case 1:
			primary = dep
		case 2:
			secondary = dep
		case 3:
			tertiary = dep
		}
	}
	return primary, secondary, tertiary
}
func (a *App) analyticsFilters(r *http.Request) (Object, time.Time, time.Time, error) {
	start, end, err := dateRange(r, "date_from", "date_to", 366, false)
	if err != nil {
		return nil, start, end, err
	}
	filters := Object{"date_from": start.Format("2006-01-02"), "date_to": end.AddDate(0, 0, -1).Format("2006-01-02"), "entity": strings.TrimSpace(r.URL.Query().Get("entity")), "education": strings.TrimSpace(r.URL.Query().Get("education")), "source": strings.TrimSpace(r.URL.Query().Get("source"))}
	for _, key := range []string{"job_id", "primary_department_id", "department_id", "school_tag_id"} {
		filters[key] = nil
		if raw := r.URL.Query().Get(key); raw != "" {
			if num(raw) <= 0 {
				return nil, start, end, bad(key + " 必须是正整数")
			}
			filters[key] = num(raw)
		}
	}
	for key, table := range map[string]string{"education": "core_candidate", "source": "core_assignmentattempt"} {
		value := str(filters[key])
		field := key
		if key == "education" {
			field = "highest_education"
		}
		f, _ := fieldFor(a, table, field)
		valid := value == ""
		for _, choice := range f.Choices {
			if value == str(choice[0]) {
				valid = true
			}
		}
		if !valid {
			return nil, start, end, bad(key + " 不是有效代码")
		}
	}
	return filters, start, end, nil
}

type cohortRecord struct {
	Resume, Candidate, Effective, Attempt, Workflow Object
	Tags                                            []Object
}

func (d *analyticsData) cohort(filters Object, start, end time.Time) []cohortRecord {
	base := []Object{}
	fallback := map[int64]Object{}
	for _, r := range d.ResumeOrder {
		at := parseTime(r["imported_at"])
		if at.Before(start) || !at.Before(end) || str(filters["entity"]) != "" && r["entity"] != filters["entity"] {
			continue
		}
		id := num(r["candidate_id"])
		c := d.Candidates[id]
		if str(filters["education"]) != "" && c["highest_education"] != filters["education"] {
			continue
		}
		if filters["school_tag_id"] != nil {
			matched := false
			for _, t := range d.TagsByCandidate[id] {
				if num(t["id"]) == num(filters["school_tag_id"]) {
					matched = true
				}
			}
			if !matched {
				continue
			}
		}
		base = append(base, r)
		if fallback[id] == nil {
			fallback[id] = r
		}
	}
	out := []cohortRecord{}
	for _, r := range base {
		id := num(r["candidate_id"])
		w := d.WorkflowByCandidate[id]
		effective := d.Resumes[num(w["current_resume_id"])]
		if effective == nil {
			effective = fallback[id]
		}
		at := d.AttemptByResume[num(effective["id"])]
		primary, secondary, _ := d.hierarchy(at["current_department_id"])
		if filters["job_id"] != nil && num(effective["job_id"]) != num(filters["job_id"]) {
			continue
		}
		if filters["primary_department_id"] != nil && num(primary["id"]) != num(filters["primary_department_id"]) {
			continue
		}
		if filters["department_id"] != nil && num(secondary["id"]) != num(filters["department_id"]) {
			continue
		}
		if str(filters["source"]) != "" && at["source"] != filters["source"] {
			continue
		}
		out = append(out, cohortRecord{r, d.Candidates[id], effective, at, w, d.TagsByCandidate[id]})
	}
	return out
}

type distribution struct {
	Rows map[string]Object
	Seen map[string]map[int64]bool
}

func newDistribution() *distribution {
	return &distribution{map[string]Object{}, map[string]map[int64]bool{}}
}
func (d *distribution) add(key any, label string, candidate int64) {
	if key == nil || str(key) == "" {
		return
	}
	k := str(key) + "\x1f" + label
	if d.Rows[k] == nil {
		d.Rows[k] = Object{"key": key, "label": label}
		d.Seen[k] = map[int64]bool{}
	}
	d.Seen[k][candidate] = true
}
func (d *distribution) values(top int) []Object {
	values := []Object{}
	for k, v := range d.Rows {
		v["count"] = len(d.Seen[k])
		values = append(values, v)
	}
	sort.Slice(values, func(i, j int) bool {
		if num(values[i]["count"]) != num(values[j]["count"]) {
			return num(values[i]["count"]) > num(values[j]["count"])
		}
		return str(values[i]["label"]) < str(values[j]["label"])
	})
	if top > 0 && len(values) > top {
		values = values[:top]
	}
	return values
}
func displayRef(row Object, fallback string) (any, string) {
	label := str(row["name"])
	if label == "" {
		label = fallback
	}
	if row["id"] == nil {
		return "text:" + label, label
	}
	return row["id"], label
}
func (a *App) recruitmentOverview(d *analyticsData, cohort []cohortRecord, filters Object, start, end time.Time) Object {
	summary := Object{"resume_count": len(cohort), "candidate_count": 0, "classified_count": 0, "allocated_count": 0, "dispatched_count": 0, "feedback_count": 0, "passed_count": 0, "archived_count": 0}
	stages := map[string]map[int64]bool{}
	for _, key := range []string{"candidate", "classified", "allocated", "dispatched", "feedback", "passed", "archived"} {
		stages[key] = map[int64]bool{}
	}
	names := []string{"source_distribution", "ai_recommendation_distribution", "ai_error_distribution", "job_ranking", "primary_department_ranking", "department_ranking", "school_tag_ranking", "education_distribution", "archive_reason_distribution", "rejection_reason_distribution"}
	distributions := map[string]*distribution{}
	for _, n := range names {
		distributions[n] = newDistribution()
	}
	trend := map[string]Object{}
	trendSeen := map[string]bool{}
	for cursor := start; cursor.Before(end); cursor = cursor.AddDate(0, 0, 1) {
		date := cursor.Format("2006-01-02")
		trend[date] = Object{"date": date, "resumes": 0, "allocated": 0, "dispatched": 0, "feedback": 0, "passed": 0}
	}
	trendEvent := func(at any, kind string, candidate int64) {
		date := parseTime(at).In(shanghai).Format("2006-01-02")
		key := date + ":" + kind + ":" + str(candidate)
		if trend[date] != nil && !trendSeen[key] {
			trend[date][kind] = num(trend[date][kind]) + 1
			trendSeen[key] = true
		}
	}
	attempts := map[int64]Object{}
	averageValues := map[string][]float64{"to_allocation": {}, "to_dispatch": {}, "to_feedback": {}}
	for _, record := range cohort {
		r, c, at, w, effective := record.Resume, record.Candidate, record.Attempt, record.Workflow, record.Effective
		id := num(c["id"])
		date := parseTime(r["imported_at"]).In(shanghai).Format("2006-01-02")
		trend[date]["resumes"] = num(trend[date]["resumes"]) + 1
		if str(r["job_category"]) != "" && (len(record.Tags) > 0 || str(c["first_degree_platform"]) != "" || str(c["highest_degree_platform"]) != "") {
			stages["classified"][id] = true
		}
		if stages["candidate"][id] {
			continue
		}
		stages["candidate"][id] = true
		if at != nil {
			attempts[num(at["id"])] = at
			stages["allocated"][id] = true
			trendEvent(at["created_at"], "allocated", id)
			if contains([]string{"dispatched", "passed", "rejected"}, str(at["status"])) {
				stages["dispatched"][id] = true
			}
			if contains([]string{"passed", "rejected"}, str(at["status"])) {
				stages["feedback"][id] = true
			}
			if at["status"] == "passed" {
				stages["passed"][id] = true
			}
			source := str(at["source"])
			distributions["source_distribution"].add(source, a.choiceLabel("core_assignmentattempt", "source", source), id)
			primary, secondary, _ := d.hierarchy(at["current_department_id"])
			key, label := displayRef(primary, "未归属一级部门")
			distributions["primary_department_ranking"].add(key, label, id)
			key, label = displayRef(secondary, "未归属二级部门")
			distributions["department_ranking"].add(key, label, id)
			if at["status"] == "rejected" {
				reason := str(at["feedback_reason_code"])
				distributions["rejection_reason_distribution"].add(reason, a.choiceLabel("core_assignmentattempt", "feedback_reason_code", reason), id)
			}
			if value := rawHours(effective["imported_at"], at["created_at"]); value != nil {
				averageValues["to_allocation"] = append(averageValues["to_allocation"], *value)
			}
			first := map[string]bool{}
			for _, event := range d.Events[num(at["id"])] {
				kinds := []string{}
				switch str(event["event_type"]) {
				case "department_dispatched":
					kinds = []string{"dispatched"}
				case "feedback_passed":
					kinds = []string{"feedback", "passed"}
				case "feedback_rejected":
					kinds = []string{"feedback"}
				}
				for _, kind := range kinds {
					trendEvent(event["occurred_at"], kind, id)
					averageKey := map[string]string{"dispatched": "to_dispatch", "feedback": "to_feedback"}[kind]
					if averageKey != "" && !first[averageKey] {
						first[averageKey] = true
						if h := rawHours(effective["imported_at"], event["occurred_at"]); h != nil {
							averageValues[averageKey] = append(averageValues[averageKey], *h)
						}
					}
				}
			}
		}
		if w["status"] == "archived" {
			stages["archived"][id] = true
			reason := str(w["archive_reason"])
			distributions["archive_reason_distribution"].add(reason, a.choiceLabel("core_candidateworkflow", "archive_reason", reason), id)
		}
		education := str(c["highest_education"])
		distributions["education_distribution"].add(education, a.choiceLabel("core_candidate", "highest_education", education), id)
		job := d.Jobs[num(effective["job_id"])]
		label := str(job["public_name"])
		if label == "" {
			label = str(job["position_name"])
		}
		if label == "" {
			label = str(effective["position_name"])
		}
		if label == "" {
			label = "未分类"
		}
		key := job["id"]
		if key == nil {
			key = "text:" + label
		}
		distributions["job_ranking"].add(key, label, id)
		if len(record.Tags) == 0 {
			distributions["school_tag_ranking"].add("text:未填写", "未填写", id)
		}
		for _, t := range record.Tags {
			distributions["school_tag_ranking"].add(t["id"], str(t["name"]), id)
		}
		for _, decision := range d.Decisions[num(effective["id"])] {
			recommendation := str(decision["recommendation"])
			distributions["ai_recommendation_distribution"].add(recommendation, a.choiceLabel("core_agentdispatchdecision", "recommendation", recommendation), id)
			code := str(decision["error_code"])
			label := code
			// 旧错误仅做普通业务展示；不再触发专项配置或决策。
			if code == "ai_special_route_unavailable" {
				code = "ai_connection_error"
				label = "AI 连接异常"
			}
			distributions["ai_error_distribution"].add(code, label, id)
		}
	}
	for key, values := range stages {
		summary[key+"_count"] = len(values)
	}
	conversion := Object{}
	for _, key := range []string{"allocated", "dispatched", "feedback", "passed"} {
		rate := 0.0
		if num(summary["candidate_count"]) > 0 {
			rate = math.Round(float64(num(summary[key+"_count"]))*10000/float64(num(summary["candidate_count"]))) / 100
		}
		conversion[key+"_rate"] = rate
	}
	averages := Object{}
	for key, values := range averageValues {
		averages[key] = durationMetric(values)["avg"]
	}
	trendRows := []Object{}
	for cursor := start; cursor.Before(end); cursor = cursor.AddDate(0, 0, 1) {
		trendRows = append(trendRows, trend[cursor.Format("2006-01-02")])
	}
	payload := Object{"data_as_of": time.Now().In(shanghai).Format(time.RFC3339Nano), "filters": filters, "summary": summary, "conversion": conversion, "average_hours": averages, "trend": trendRows, "handling_speed": d.handlingSpeed(attempts), "filter_options": a.analyticsOptions(d), "methodology": Object{"cohort": "Resume.imported_at 落在所选日期范围内的投递记录", "candidate_scope": "候选人数及各阶段按 Candidate 去重", "job_scope": "岗位排行优先使用 CandidateWorkflow.current_resume", "department_scope": "部门筛选和排行使用候选人当前有效志愿的最新非取消分配尝试；部门收件箱仅到二级，一级部门按当前部门树回溯", "conversion_denominator": "所选 cohort 去重候选人数", "handling_speed": "自然时间小时；P90 使用最近秩；系统自动完成的部门转派不计转出部门人工处理时长"}}
	for name, dist := range distributions {
		top := 0
		if strings.HasSuffix(name, "ranking") || name == "rejection_reason_distribution" {
			top = 10
		}
		payload[name] = dist.values(top)
	}
	return payload
}
func rawHours(start, end any) *float64 {
	s, e := parseTime(start), parseTime(end)
	if s.IsZero() || e.IsZero() || e.Before(s) {
		return nil
	}
	v := e.Sub(s).Hours()
	return &v
}
func durationMetric(values []float64) Object {
	valid := []float64{}
	for _, v := range values {
		if v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			valid = append(valid, v)
		}
	}
	if len(valid) == 0 {
		return Object{"avg": nil, "median": nil, "p90": nil, "sample_count": 0}
	}
	sort.Float64s(valid)
	sum := 0.0
	for _, v := range valid {
		sum += v
	}
	median := valid[len(valid)/2]
	if len(valid)%2 == 0 {
		median = (valid[len(valid)/2-1] + median) / 2
	}
	round := func(v float64) float64 { return math.Round(v*100) / 100 }
	return Object{"avg": round(sum / float64(len(valid))), "median": round(median), "p90": round(valid[int(math.Ceil(float64(len(valid))*.9))-1]), "sample_count": len(valid)}
}
func (d *analyticsData) handlingSpeed(attempts map[int64]Object) Object {
	hr, department, total := []float64{}, []float64{}, []float64{}
	completed, pending := map[int64][]float64{}, map[int64][]float64{}
	now := time.Now()
	for _, at := range attempts {
		var first, entered any
		var active int64
		for _, e := range d.Events[num(at["id"])] {
			switch str(e["event_type"]) {
			case "department_dispatched":
				if first == nil {
					first = e["occurred_at"]
					if h := rawHours(at["created_at"], first); h != nil {
						hr = append(hr, *h)
					}
				}
				active = num(e["to_department_id"])
				entered = e["occurred_at"]
			case "department_transferred":
				if active != 0 && !truth(e["is_system_auto"]) {
					if h := rawHours(entered, e["occurred_at"]); h != nil {
						department = append(department, *h)
						completed[active] = append(completed[active], *h)
					}
				}
				active = num(e["to_department_id"])
				entered = e["occurred_at"]
			case "feedback_passed", "feedback_rejected":
				if h := rawHours(first, e["occurred_at"]); h != nil {
					total = append(total, *h)
				}
				if active != 0 {
					if h := rawHours(entered, e["occurred_at"]); h != nil {
						department = append(department, *h)
						completed[active] = append(completed[active], *h)
					}
				}
				active = 0
				entered = nil
			case "cancelled":
				active = 0
				entered = nil
			}
			if contains([]string{"feedback_passed", "feedback_rejected", "cancelled"}, str(e["event_type"])) {
				break
			}
		}
		if at["status"] == "dispatched" && active != 0 {
			if h := rawHours(entered, now); h != nil {
				pending[active] = append(pending[active], *h)
			}
		}
	}
	maxAge := func(values []float64) any {
		if len(values) == 0 {
			return nil
		}
		v := 0.0
		for _, n := range values {
			v = max(v, n)
		}
		return math.Round(v*100) / 100
	}
	departments := []Object{}
	seen := map[int64]bool{}
	for id := range completed {
		seen[id] = true
	}
	for id := range pending {
		seen[id] = true
	}
	allPending := []float64{}
	for id := range seen {
		dep := d.Departments[id]
		primary, _, _ := d.hierarchy(id)
		primaryName := str(primary["name"])
		if primaryName == "" {
			primaryName = "未归属一级部门"
		}
		departments = append(departments, Object{"department_id": id, "department_name": dep["name"], "primary_department_id": primary["id"], "primary_department_name": primaryName, "processing_hours": durationMetric(completed[id]), "pending_count": len(pending[id]), "max_pending_age_hours": maxAge(pending[id])})
		allPending = append(allPending, pending[id]...)
	}
	sort.Slice(departments, func(i, j int) bool {
		return str(departments[i]["department_name"]) < str(departments[j]["department_name"])
	})
	return Object{"overall": Object{"hr_dispatch_hours": durationMetric(hr), "department_processing_hours": durationMetric(department), "total_feedback_hours": durationMetric(total), "pending_count": len(allPending), "max_pending_age_hours": maxAge(allPending)}, "departments": departments}
}
func (a *App) analyticsOptions(d *analyticsData) Object {
	result := Object{}
	entities := map[string]bool{}
	for _, r := range d.Resumes {
		if str(r["entity"]) != "" {
			entities[str(r["entity"])] = true
		}
	}
	names := []string{}
	for name := range entities {
		names = append(names, name)
	}
	sort.Strings(names)
	result["entities"] = names
	for _, key := range []string{"jobs", "primary_departments", "departments", "school_tags", "educations", "sources"} {
		result[key] = []any{}
	}
	for _, j := range d.Jobs {
		if truth(j["is_active"]) {
			label := str(j["public_name"])
			if label == "" {
				label = str(j["position_name"])
			}
			result["jobs"] = append(list(result["jobs"]), Object{"value": j["id"], "label": label})
		}
	}
	for _, dep := range d.Departments {
		key := ""
		if num(dep["level"]) == 1 {
			key = "primary_departments"
		} else if num(dep["level"]) == 2 {
			key = "departments"
		}
		if key != "" {
			v := Object{"value": dep["id"], "label": dep["name"]}
			if key == "departments" {
				v["parent_id"] = dep["parent_id"]
			}
			result[key] = append(list(result[key]), v)
		}
	}
	for _, t := range d.Tags {
		if truth(t["is_active"]) {
			result["school_tags"] = append(list(result["school_tags"]), Object{"value": t["id"], "label": t["name"]})
		}
	}
	for key, field := range map[string][2]string{"educations": {"core_candidate", "highest_education"}, "sources": {"core_assignmentattempt", "source"}} {
		f, _ := fieldFor(a, field[0], field[1])
		for _, choice := range f.Choices {
			result[key] = append(list(result[key]), Object{"value": choice[0], "label": choice[1]})
		}
	}
	for _, key := range []string{"jobs", "primary_departments", "departments", "school_tags"} {
		values := list(result[key])
		sort.Slice(values, func(i, j int) bool { return str(obj(values[i])["label"]) < str(obj(values[j])["label"]) })
		result[key] = values
	}
	return result
}
