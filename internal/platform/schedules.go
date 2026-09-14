package platform

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func validateSavedCandidateFilters(filters Object) error {
	query := url.Values{}
	for key, value := range filters {
		if strings.TrimSpace(str(value)) == "" || value == nil {
			continue
		}
		if !contains([]string{"name", "search", "phone", "highest_major", "highest_major_in", "highest_degree", "current_apply_id", "current_entity_in", "current_position_name_in", "job_department_name_in", "current_primary_department_id", "current_department_id", "current_job_category_in", "school_tag_in", "school_tag", "current_apply_date_from", "current_apply_date_to", "imported_after", "imported_before", "allocation_source", "reason_code", "reason_type", "feedback_reason_code", "workflow_status", "processing_run_id", "processing_result", "ordering"}, key) && !strings.HasPrefix(key, "analytics_") {
			return bad("不支持保存筛选条件：" + key)
		}
		switch v := value.(type) {
		case string:
			query.Set(key, v)
		case float64:
			query.Set(key, str(v))
		case []any:
			for _, item := range v {
				if _, ok := item.(string); !ok {
					return bad("筛选项必须是字符串数组")
				}
			}
			query.Set(key, strings.Join(stringValues(v), ","))
		default:
			return bad("筛选项类型无效")
		}
	}
	r, _ := http.NewRequest("GET", "http://localhost/api/candidates/?"+query.Encode(), nil)
	return validateCandidateFilters(r, &Principal{Permissions: map[string]bool{"resume.view": true}})
}

// Schedules belong to the platform. Claiming a trigger, creating its run and
// advancing the clock commit together, so a restart cannot duplicate a trigger.
func (a *App) migrateSchedules(ctx context.Context, db DB) error {
	_, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS platform_processing_schedules (
 id bigserial PRIMARY KEY, name text NOT NULL, scope jsonb NOT NULL,
 repeat text NOT NULL CHECK (repeat IN ('once','daily','weekly')),
 run_at timestamptz NOT NULL, next_run_at timestamptz,
 status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','completed','cancelled','failed')),
 created_by_id bigint REFERENCES accounts_user(id) ON DELETE SET NULL,
 created_by_username_snapshot text NOT NULL,
 last_run_id bigint REFERENCES core_processingrun(id) ON DELETE SET NULL,
 last_triggered_at timestamptz, last_message text NOT NULL DEFAULT '',
 trigger_count integer NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());
 CREATE INDEX IF NOT EXISTS platform_processing_schedules_due ON platform_processing_schedules(next_run_at,id) WHERE status='active';
 ALTER TABLE platform_processing_schedules DROP CONSTRAINT IF EXISTS platform_processing_schedules_status_check;
 ALTER TABLE platform_processing_schedules ADD CONSTRAINT platform_processing_schedules_status_check CHECK (status IN ('active','paused','completed','cancelled','failed'));
 CREATE INDEX IF NOT EXISTS platform_runs_created_query ON core_processingrun(created_at DESC,id DESC);
 CREATE INDEX IF NOT EXISTS platform_runs_schedule_query ON core_processingrun((scope->>'schedule_id'),created_at DESC);`)
	return err
}

func validateSchedule(body Object, now time.Time) (Object, time.Time, error) {
	for key := range body {
		if !contains([]string{"name", "scope", "repeat", "run_at"}, key) {
			return nil, time.Time{}, bad("不支持的定时任务字段：" + key)
		}
	}
	name, ok := body["name"].(string)
	if !ok || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 100 {
		return nil, time.Time{}, bad("任务名称须为 1 至 100 个字符")
	}
	when, err := time.Parse(time.RFC3339, str(body["run_at"]))
	if err != nil || !when.After(now) || when.Year() > 9999 {
		return nil, time.Time{}, bad("触发时间必须是带时区的未来时间")
	}
	if !contains([]string{"once", "daily", "weekly"}, str(body["repeat"])) {
		return nil, time.Time{}, bad("重复方式须为 once、daily 或 weekly")
	}
	scope, ok := body["scope"].(map[string]any)
	if !ok {
		return nil, time.Time{}, bad("请选择处理范围")
	}
	scope = clone(scope)
	for key := range scope {
		if !contains([]string{"candidate_ids", "system_statuses", "candidate_filters", "force_reprocess"}, key) {
			return nil, time.Time{}, bad("不支持的定时处理范围字段：" + key)
		}
	}
	if value, exists := scope["candidate_filters"]; exists {
		filters, ok := value.(map[string]any)
		if !ok {
			return nil, time.Time{}, bad("candidate_filters 必须是对象")
		}
		if err := validateSavedCandidateFilters(filters); err != nil {
			return nil, time.Time{}, err
		}
	}
	_, selected := scope["candidate_ids"]
	_, statuses := scope["system_statuses"]
	if selected == statuses || selected && scope["candidate_filters"] != nil {
		return nil, time.Time{}, bad("请选择候选人或简历状态，两种范围互斥")
	}
	if selected {
		ids, err := positiveIDs(scope["candidate_ids"])
		if err != nil {
			return nil, time.Time{}, err
		}
		scope["candidate_ids"] = ids
	} else {
		values, ok := scope["system_statuses"].([]any)
		if !ok || len(values) == 0 {
			return nil, time.Time{}, bad("请选择至少一种简历状态")
		}
		for _, value := range values {
			if _, ok := systemLabels[str(value)]; !ok && str(value) != "pending_review" {
				return nil, time.Time{}, bad("未知系统状态")
			}
		}
	}
	if value, exists := scope["force_reprocess"]; exists && (!selected || !truth(value)) {
		return nil, time.Time{}, bad("强制重新处理仅支持明确选中的候选人")
	}
	return clone(scope), when.UTC(), nil
}

// Beijing time has a fixed UTC+8 offset. Missed intervals coalesce into one
// trigger; the following trigger remains anchored to the original wall clock.
func nextScheduleTime(previous time.Time, repeat string, now time.Time) *time.Time {
	if repeat == "once" {
		return nil
	}
	days := 1
	if repeat == "weekly" {
		days = 7
	}
	beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
	previous, now = previous.In(beijing), now.In(beijing)
	// Calendar arithmetic avoids time.Duration overflow for distant dates.
	intervals := (now.Unix() - previous.Unix()) / int64(days*86400)
	if intervals < 0 {
		intervals = 0
	}
	next := previous.AddDate(0, 0, int(intervals+1)*days).UTC()
	return &next
}

func (a *App) schedulesAPI(w http.ResponseWriter, r *http.Request, path string, p *Principal) error {
	if path == "pipeline/schedules" && r.Method == "GET" {
		if !p.has("pipeline.view") {
			return &apiError{403, "无任务查看权限"}
		}
		return a.taskList(w, r, p, true)
	}
	if !p.has("pipeline.run") {
		return &apiError{403, "无处理权限"}
	}
	if path == "pipeline/schedules" {
		if r.Method != "POST" {
			return &apiError{405, "请求方法不允许"}
		}
		if !p.has("resume.view") {
			return &apiError{403, "创建定时任务需要简历库查看权限"}
		}
		body, err := readBody(w, r)
		if err != nil {
			return err
		}
		scope, when, err := validateSchedule(body, time.Now())
		if err != nil {
			return err
		}
		if selected, exists := scope["candidate_ids"]; exists {
			ids, _ := positiveIDs(selected)
			var count int
			if err = a.Pool.QueryRow(r.Context(), "SELECT count(*) FROM core_candidate WHERE id=ANY($1::bigint[])", ids).Scan(&count); err != nil {
				return err
			}
			if count != len(ids) {
				return bad("选择的候选人已不存在")
			}
		}
		value, err := one(r.Context(), a.Pool, `INSERT INTO platform_processing_schedules AS s
 (name,scope,repeat,run_at,next_run_at,created_by_id,created_by_username_snapshot)
 VALUES($1,$2::jsonb,$3,$4,$4,$5,$6) RETURNING row_to_json(s)`, strings.TrimSpace(str(body["name"])), string(canonicalJSON(scope, false)), body["repeat"], when, p.User["id"], p.User["username"])
		if err != nil {
			return err
		}
		write(w, 201, value)
		return nil
	}
	parts := strings.Split(path, "/")
	if len(parts) != 4 || !contains([]string{"cancel", "pause", "resume"}, parts[3]) {
		return &apiError{404, "未找到"}
	}
	if r.Method != "POST" {
		return &apiError{405, "请求方法不允许"}
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id < 1 {
		return bad("无效任务 ID")
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	schedule, err := one(r.Context(), tx, "SELECT row_to_json(s) FROM platform_processing_schedules s WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return err
	}
	if !p.isAdministrator() && num(schedule["created_by_id"]) != num(p.User["id"]) {
		return &apiError{403, "只能取消自己创建的定时任务"}
	}
	action := parts[3]
	current := str(schedule["status"])
	var value Object
	switch action {
	case "cancel":
		if !contains([]string{"active", "paused", "cancelled"}, current) {
			return &apiError{409, "定时计划已结束"}
		}
		value, err = one(r.Context(), tx, "UPDATE platform_processing_schedules s SET status='cancelled',next_run_at=NULL,updated_at=now() WHERE id=$1 RETURNING row_to_json(s)", id)
	case "pause":
		if !contains([]string{"active", "paused"}, current) {
			return &apiError{409, "只能暂停启用中的计划"}
		}
		value, err = one(r.Context(), tx, "UPDATE platform_processing_schedules s SET status='paused',updated_at=now() WHERE id=$1 RETURNING row_to_json(s)", id)
	case "resume":
		if current != "paused" {
			return &apiError{409, "只能恢复已暂停的计划"}
		}
		if !p.has("resume.view") {
			return &apiError{403, "无简历库查看权限"}
		}
		next, parseErr := time.Parse(time.RFC3339Nano, str(schedule["next_run_at"]))
		if parseErr != nil {
			return parseErr
		}
		if !next.After(time.Now()) {
			if schedule["repeat"] == "once" {
				next = time.Now().UTC()
			} else {
				next = *nextScheduleTime(next, str(schedule["repeat"]), time.Now())
			}
		}
		value, err = one(r.Context(), tx, "UPDATE platform_processing_schedules s SET status='active',next_run_at=$2,updated_at=now() WHERE id=$1 RETURNING row_to_json(s)", id, next)
	}

	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	write(w, 200, value)
	return nil
}

func (a *App) scheduler(ctx context.Context) {
	for ctx.Err() == nil {
		for i := 0; i < 20 && ctx.Err() == nil; i++ {
			triggerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			claimed, err := a.triggerSchedule(triggerCtx, time.Now().UTC())
			cancel()
			if err != nil {
				a.Log.Error("scheduled task dispatch failed; will retry")
			}
			if err != nil || !claimed {
				break
			}
		}
		if !pause(ctx, 5*time.Second) {
			return
		}
	}
}

func (a *App) triggerSchedule(ctx context.Context, now time.Time) (bool, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	s, err := one(ctx, tx, `SELECT row_to_json(s) FROM platform_processing_schedules s
 WHERE status='active' AND next_run_at<=$1 ORDER BY next_run_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, now)
	if err != nil {
		var public *apiError
		if errors.As(err, &public) && public.Status == 404 {
			return false, nil
		}
		return false, err
	}
	due, err := time.Parse(time.RFC3339Nano, str(s["next_run_at"]))
	if err != nil {
		return false, err
	}
	next := nextScheduleTime(due, str(s["repeat"]), now)
	status, message := "active", ""
	if next == nil {
		status = "completed"
	}
	// Authorization is reconstructed at execution time; no stored token or
	// permission snapshot can keep a revoked or disabled account running jobs.
	user, err := one(ctx, tx, "SELECT row_to_json(u) FROM accounts_user u WHERE id=$1 AND is_active", s["created_by_id"])
	var p *Principal
	if err != nil {
		var public *apiError
		if !errors.As(err, &public) || public.Status != 404 {
			return false, err
		}
	} else if p, err = a.userPrincipal(ctx, user); err != nil {
		return false, err
	}
	if p == nil || !p.has("pipeline.run") || !p.has("resume.view") {
		status, next, message = "failed", nil, "创建人已停用或失去简历处理权限，请重新创建任务"
	}
	var runID any = s["last_run_id"]
	created := false
	if message == "" && runID != nil {
		previous, err := a.get(ctx, tx, "core_processingrun", runID)
		if err != nil {
			return false, err
		}
		if activeRun(str(previous["status"])) {
			message = "上一轮仍在处理，本轮已跳过"
		}
	}
	if message == "" {
		scope := clone(obj(s["scope"]))
		r, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost/api/candidates/", nil)
		ids, err := a.resolveRunCandidates(r, p, scope)
		if err != nil {
			return false, err
		}
		if len(ids) == 0 {
			message = "本轮没有符合范围的候选人，已跳过"
		} else {
			var count int
			if err = tx.QueryRow(ctx, "SELECT count(*) FROM core_candidate WHERE id=ANY($1::bigint[])", ids).Scan(&count); err != nil {
				return false, err
			}
			if count != len(ids) {
				status, next, message = "failed", nil, "固定范围中的候选人已被删除，请重新创建任务"
			} else {
				scope["task_name"] = s["name"]
				scope["source"], scope["schedule_id"], scope["scheduled_for"] = "schedule", s["id"], due.Format(time.RFC3339Nano)
				run, err := a.createRun(ctx, tx, "step2", scope, ids, p)
				if err != nil {
					return false, err
				}
				runID, created = run["id"], true
				message = "已提交处理任务 #" + str(runID)
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE platform_processing_schedules SET status=$2,next_run_at=$3,
 last_run_id=$4,last_triggered_at=$5,last_message=$6,trigger_count=trigger_count+1,updated_at=$5 WHERE id=$1`, s["id"], status, next, runID, now, message)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if created {
		a.wakeQueue(ctx)
	}
	return true, nil
}
