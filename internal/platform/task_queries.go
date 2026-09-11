package platform

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var runStateGroups = map[string][]string{
	"active":    {"pending", "running", "waiting_conflict", "cancelling"},
	"attention": {"needs_attention", "partial_failed", "failed"},
	"finished":  {"success", "needs_attention", "partial_failed", "failed", "cancelled"},
}

// Filter and paginate in PostgreSQL before expanding stages and progress.
// Counts describe the entire matching scope, never just the visible page.
func (a *App) taskList(w http.ResponseWriter, r *http.Request, p *Principal, schedules bool) error {
	if !p.has("pipeline.view") {
		return &apiError{403, "无任务查看权限"}
	}
	q := r.URL.Query()
	where, args := []string{"TRUE"}, []any{}
	add := func(sql string, value any) {
		args = append(args, value)
		where = append(where, strings.ReplaceAll(sql, "?", "$"+strconv.Itoa(len(args))))
	}
	if search := strings.TrimSpace(q.Get("search")); search != "" {
		if len(search) > 300 {
			return bad("搜索内容过长")
		}
		name := "COALESCE(t.scope->>'task_name','')"
		if schedules {
			name = "t.name"
		}
		// strpos treats user input as literal text, including percent/underscore.
		add("(strpos(lower("+name+"),lower(?))>0 OR strpos(t.id::text,?)>0 OR strpos(lower(t.created_by_username_snapshot),lower(?))>0)", search)
	}
	if q.Get("mine") == "true" {
		add("t.created_by_id=?", num(p.User["id"]))
	}
	if user := q.Get("created_by"); user != "" {
		add("t.created_by_id::text=?", user)
	}
	if user := q.Get("created_by_username"); user != "" {
		add("t.created_by_username_snapshot=?", user)
	}
	if id := q.Get("id"); id != "" {
		add("t.id::text=?", id)
	}
	if ids := queryValues(r, "ids"); len(ids) > 0 {
		add("t.id::text=ANY(?::text[])", ids)
	}
	// Preserve existing run filters while moving the list to SQL pagination.
	if !schedules {
		for _, field := range []string{"step", "mode", "current_stage", "created_by_username_snapshot"} {
			if value := q.Get(field); value != "" {
				add("strpos(lower(t."+field+"),lower(?))>0", value)
			}
			if values := queryValues(r, field+"_in"); len(values) > 0 {
				add("t."+field+"=ANY(?::text[])", values)
			}
		}
	}
	var start, end time.Time
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	for _, key := range []string{"created_from", "created_to"} {
		if value := q.Get(key); value != "" {
			date, err := time.ParseInLocation("2006-01-02", value, zone)
			if err != nil {
				return bad("查询日期格式必须为 YYYY-MM-DD")
			}
			if key == "created_from" {
				start = date
				add("t.created_at>=?", date)
			} else {
				end = date
				add("t.created_at<?", date.AddDate(0, 0, 1))
			}
		}
	}
	if !start.IsZero() && !end.IsZero() && start.After(end) {
		return bad("开始日期不能晚于结束日期")
	}
	if !schedules {
		if source := q.Get("source"); source != "" {
			if !contains([]string{"manual", "schedule", "resume_import", "ai_retry"}, source) {
				return bad("未知触发来源")
			}
			add("COALESCE(NULLIF(t.scope->>'source',''),'manual')=?", source)
		}
		if id := q.Get("schedule_id"); id != "" {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil || n <= 0 {
				return bad("无效定时计划 ID")
			}
			add("t.scope->>'schedule_id'=?", strconv.FormatInt(n, 10))
		}
	} else if repeat := q.Get("repeat"); repeat != "" {
		if !contains([]string{"once", "daily", "weekly"}, repeat) {
			return bad("未知触发频率")
		}
		add("t.repeat=?", repeat)
	}
	table, selectSQL := "core_processingrun t", "row_to_json(t)"
	if schedules {
		table = "platform_processing_schedules t LEFT JOIN core_processingrun r ON r.id=t.last_run_id"
		selectSQL = "to_jsonb(t)||jsonb_build_object('last_run_status',r.status)"
	}
	var summary Object
	if !schedules && q.Get("include_summary") == "true" {
		var err error
		summary, err = one(r.Context(), a.Pool, `SELECT json_build_object('total',count(*),
 'active',count(*) FILTER (WHERE t.status IN ('pending','running','waiting_conflict','cancelling')),
 'attention',count(*) FILTER (WHERE t.status IN ('needs_attention','partial_failed','failed')),
 'finished',count(*) FILTER (WHERE t.status IN ('success','needs_attention','partial_failed','failed','cancelled')))
 FROM `+table+" WHERE "+strings.Join(where, " AND "), args...)
		if err != nil {
			return err
		}
	}
	state := q.Get("state")
	if !schedules && q.Get("active") == "true" {
		state = "active"
	}
	if state != "" {
		if schedules {
			if !contains([]string{"active", "paused", "completed", "cancelled", "failed"}, state) {
				return bad("未知计划状态")
			}
			add("t.status=?", state)
		} else {
			states, ok := runStateGroups[state]
			if !ok {
				return bad("未知执行状态分组")
			}
			add("t.status=ANY(?::text[])", states)
		}
	}
	if status := q.Get("status"); status != "" && !schedules {
		if !contains(append(append([]string{}, runStateGroups["active"]...), runStateGroups["finished"]...), status) {
			return bad("未知执行状态")
		}
		add("t.status=?", status)
	}
	if statuses := queryValues(r, "status_in"); len(statuses) > 0 && !schedules {
		add("t.status=ANY(?::text[])", statuses)
	}
	clause := " FROM " + table + " WHERE " + strings.Join(where, " AND ")
	var count int
	if err := a.Pool.QueryRow(r.Context(), "SELECT count(*)"+clause, args...).Scan(&count); err != nil {
		return err
	}
	page, size, err := pageBounds(r, count)
	if err != nil {
		return err
	}
	order := "t.created_at DESC,t.id DESC"
	if schedules {
		order = "(t.status='active') DESC,t.next_run_at ASC NULLS LAST,t.id DESC"
	}
	if ordering := q.Get("ordering"); ordering != "" {
		allowed := map[string]string{"-created_at": "t.created_at DESC,t.id DESC", "created_at": "t.created_at ASC,t.id ASC", "-id": "t.id DESC", "id": "t.id ASC"}
		var ok bool
		if order, ok = allowed[ordering]; !ok {
			return bad("不支持的任务排序")
		}
	}
	query := "SELECT " + selectSQL + clause + " ORDER BY " + order + fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	records, err := rows(r.Context(), a.Pool, query, append(args, size, (page-1)*size)...)
	if err != nil {
		return err
	}
	values := []Object{}
	for _, row := range records {
		if schedules {
			values = append(values, schedulePresentation(row, p))
		} else {
			value, err := a.serialize(r.Context(), "pipeline/runs", row, p, false)
			if err != nil {
				return err
			}
			values = append(values, value)
		}
	}
	result := pageResponse(r, values, count, page, size)
	if summary != nil {
		result["summary"] = summary
	}
	write(w, 200, result)
	return nil
}

func schedulePresentation(value Object, p *Principal) Object {
	scope := obj(value["scope"])
	if ids, exists := scope["candidate_ids"]; exists {
		value["scope_label"] = "固定选中 " + strconv.Itoa(len(list(ids))) + " 名候选人"
	} else {
		labels := []string{}
		for _, status := range list(scope["system_statuses"]) {
			labels = append(labels, systemLabels[str(status)])
		}
		value["scope_label"] = "触发时匹配：" + strings.Join(labels, "、")
		if len(obj(scope["candidate_filters"])) > 0 {
			value["scope_label"] = str(value["scope_label"]) + "（含创建时的筛选条件）"
		}
	}
	canManage := p.has("pipeline.run") && (p.isAdministrator() || num(value["created_by_id"]) == num(p.User["id"]))
	value["can_cancel"] = canManage && contains([]string{"active", "paused"}, str(value["status"]))
	value["can_pause"] = canManage && value["status"] == "active"
	value["can_resume"] = canManage && value["status"] == "paused" && p.has("resume.view")
	delete(value, "scope")
	return value
}
