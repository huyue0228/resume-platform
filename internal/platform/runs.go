package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var stageLabels = map[string]string{"queued": "等待后台处理", "initialize": "检查处理服务", "preparing": "准备候选人材料", "step1": "整理简历与志愿", "step2": "核验学历与院校", "step3": "匹配可选岗位", "step4": "提取文字、分析与保存结果", "finalize": "汇总处理结果"}
var stageDescriptions = map[string]string{"queued": "任务已提交，等待后台开始处理。", "initialize": "检查模型连接和分析服务，准备本次处理配置。", "preparing": "读取所选简历，准备候选人材料和岗位信息。", "step1": "检查重复投递，整理候选人的志愿顺序。", "step2": "依据院校和学历要求核验候选人资格。", "step3": "确定当前志愿对应的可选岗位。", "step4": "逐份提取全文、校验文本、分析岗位匹配并保存结果；材料异常可单份重试。", "finalize": "核对完成、需处理和失败数量，生成任务汇总。"}

func activeRun(s string) bool {
	return contains([]string{"pending", "running", "waiting_conflict", "cancelling"}, s)
}
func terminalItem(s string) bool {
	return contains([]string{"success", "needs_attention", "failed", "skipped_manual_change", "cancelled"}, s)
}
func stageSteps(step string) []string {
	switch step {
	case "all", "resume_process":
		return []string{"step1", "step2", "step3", "step4"}
	case "step2":
		return []string{"step2", "step3", "step4"}
	case "step3":
		return []string{"step3", "step4"}
	default:
		return []string{step}
	}
}
func positiveIDs(value any) ([]int64, error) {
	values, ok := value.([]any)
	if !ok || len(values) == 0 {
		return nil, bad("必须提供非空的正整数 ID 数组")
	}
	ids := []int64{}
	seen := map[int64]bool{}
	for _, v := range values {
		n, ok := v.(float64)
		if !ok || n < 1 || n != float64(int64(n)) {
			return nil, bad("ID 必须是正整数")
		}
		if !seen[int64(n)] {
			ids = append(ids, int64(n))
			seen[int64(n)] = true
		}
	}
	return ids, nil
}
func (a *App) submitRun(w http.ResponseWriter, r *http.Request, p *Principal) error {
	if r.Method != "POST" {
		return &apiError{405, "请求方法不允许"}
	}
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	if _, ok := body["mode"]; ok {
		return bad("处理内核由系统固定，不接受 mode 或 modes")
	}
	if _, ok := body["modes"]; ok {
		return bad("处理内核由系统固定，不接受 mode 或 modes")
	}
	scope := Object{}
	if body["scope"] != nil {
		var ok bool
		scope, ok = body["scope"].(map[string]any)
		if !ok {
			return bad("处理范围 scope 必须是对象")
		}
	}
	for _, key := range []string{"source", "retry_decision_id", "retry_resume_id", "expected_workflow_revision", "trigger", "schedule_id", "scheduled_for", "task_name"} {
		if _, ok := scope[key]; ok {
			return bad("续办与 AI 重试只能从对应业务入口发起")
		}
	}
	step := str(body["step"])
	if step == "" {
		step = "all"
	}
	if !contains([]string{"all", "resume_process", "step1", "step2", "step3", "step4"}, step) {
		return bad("未知处理步骤")
	}
	if v, ok := scope["force_reprocess"]; ok {
		if !truth(v) || step != "step2" || scope["system_statuses"] != nil || scope["candidate_filters"] != nil {
			return bad("force_reprocess 仅支持 step2 与明确的 candidate_ids")
		}
		if _, err = positiveIDs(scope["candidate_ids"]); err != nil {
			return err
		}
	}
	if name, exists := body["name"]; exists {
		value, ok := name.(string)
		if !ok || utf8.RuneCountInString(value) > 100 {
			return bad("任务名称最多 100 个字符")
		}
		if value = strings.TrimSpace(value); value != "" {
			scope["task_name"] = value
		}
	}
	ids, err := a.resolveRunCandidates(r, p, scope)
	if err != nil {
		return err
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	run, err := a.createRun(r.Context(), tx, step, scope, ids, p)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	a.wakeQueue(r.Context())
	value, err := a.serialize(r.Context(), "pipeline/runs", run, p, true)
	if err != nil {
		return err
	}
	write(w, 202, Object{"processing_runs": []any{value}})
	return nil
}
func (a *App) resolveRunCandidates(r *http.Request, p *Principal, scope Object) ([]int64, error) {
	var err error
	ids := []int64{}
	if v, ok := scope["candidate_ids"]; ok {
		ids, err = positiveIDs(v)
		if err != nil {
			return nil, err
		}
	} else {
		query := url.Values{}
		if v, ok := scope["candidate_filters"]; ok {
			filters, ok := v.(map[string]any)
			if !ok {
				return nil, bad("candidate_filters 必须是对象")
			}
			for k, v := range filters {
				if vs, ok := v.([]any); ok {
					query.Set(k, strings.Join(stringValues(vs), ","))
				} else {
					query.Set(k, str(v))
				}
			}
		}
		if v, ok := scope["system_statuses"]; ok {
			statuses, ok := v.([]any)
			if !ok || len(statuses) == 0 {
				return nil, bad("system_statuses 必须是非空数组")
			}
			for _, s := range statuses {
				if _, ok := systemLabels[str(s)]; !ok {
					return nil, bad("未知系统状态")
				}
			}
			query.Set("system_statuses", strings.Join(stringValues(v), ","))
		}
		filterReq := r.Clone(r.Context())
		u := *r.URL
		filterReq.URL = &u
		u.RawQuery = query.Encode()
		var candidates []Object
		if len(query) == 0 && p.has("resume.view") {
			candidates, err = rows(r.Context(), a.Pool, "SELECT json_build_object('id',id) FROM core_candidate ORDER BY id")
		} else {
			candidates, err = a.filtered(r.Context(), "candidates", filterReq, p)
		}
		if err != nil {
			return nil, err
		}
		for _, c := range candidates {
			ids = append(ids, num(c["id"]))
		}
	}
	return ids, nil
}
func (a *App) createRun(ctx context.Context, db DB, step string, scope Object, ids []int64, p *Principal) (Object, error) {
	scope = clone(scope)
	delete(scope, "candidate_ids")
	summary := Object{"candidate_count": len(ids)}
	for _, key := range []string{"system_statuses", "source", "task_name", "schedule_id"} {
		if v, ok := scope[key]; ok {
			summary[key] = v
		}
	}
	if expected, ok := scope["expected_workflow_revision"]; ok {
		if len(ids) != 1 {
			return nil, bad("续办必须指定一个候选人")
		}
		workflow, err := one(ctx, db, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1 FOR UPDATE", ids[0])
		if err != nil {
			return nil, err
		}
		if num(workflow["revision"]) != num(expected) {
			return nil, &apiError{409, "续办前流程已被修改"}
		}
		existing, err := one(ctx, db, "SELECT row_to_json(r) FROM core_processingrun r JOIN core_processingrunscopeitem i ON i.run_id=r.id WHERE i.candidate_id=$1 AND r.scope->>'trigger'='feedback_rejected' AND r.scope->>'retry_resume_id'=$2 AND r.scope->>'expected_workflow_revision'=$3 ORDER BY r.id LIMIT 1", ids[0], str(scope["retry_resume_id"]), str(expected))
		if err == nil {
			return existing, nil
		}
		var e *apiError
		if !errors.As(err, &e) || e.Status != 404 {
			return nil, err
		}
	}
	values := Object{"step": step, "mode": "ai", "scope": scope, "scope_summary": summary, "total_count": len(ids), "params": Object{"runtime_initialization": "pending", "snapshot_preparation": "pending"}, "protocol_version": protocolVersion, "current_stage": "queued", "message": "任务已提交，等待后台开始处理"}
	if p != nil {
		values["created_by_id"] = p.User["id"]
		values["created_by_username_snapshot"] = p.User["username"]
	}
	run, err := a.save(ctx, db, "core_processingrun", nil, values)
	if err != nil {
		return nil, err
	}
	// 在一条语句中冻结候选人范围和当前流程修订，不在提交请求内展开全文或关联对象。
	tag, err := db.Exec(ctx, `INSERT INTO core_processingrunscopeitem(processing_node,kernel_snapshot,kernel_result,run_id,candidate_id,workflow_revision_at_submit,status,skip_reason,result_type,reason_code,result_message,attempt_count,error_code,error_message,created_at)
 SELECT 'pending','{}'::jsonb,'{}'::jsonb,$1,c.id,w.revision,'pending','','','','',0,'','',now()
 FROM core_candidate c LEFT JOIN core_candidateworkflow w ON w.candidate_id=c.id WHERE c.id=ANY($2::bigint[]) ORDER BY c.id`, run["id"], ids)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != int64(len(ids)) {
		return nil, bad("选择的候选人已不存在")
	}
	stages := []string{"queued", "initialize"}
	if step != "step1" {
		stages = append(stages, "preparing")
	}
	stages = append(stages, stageSteps(step)...)
	stages = append(stages, "finalize")
	for i, step := range stages {
		count := len(ids)
		if contains([]string{"queued", "initialize", "finalize"}, step) {
			count = 1
		}
		values := Object{"run_id": run["id"], "sequence": i + 1, "step": step, "label": stageLabels[step], "total_count": count}
		if step == "queued" {
			values["status"] = "running"
			values["started_at"] = time.Now().UTC()
		}
		if _, err = a.save(ctx, db, "core_processingrunstage", nil, values); err != nil {
			return nil, err
		}
	}
	_, err = db.Exec(ctx, "INSERT INTO platform_go_jobs(run_id) VALUES($1)", run["id"])
	return run, err
}
func (a *App) wakeQueue(ctx context.Context) {
	if err := a.Redis.LPush(ctx, "resume:go:queue", "wake").Err(); err != nil {
		a.Log.Warn("queue notification unavailable; durable database queue will be polled")
	} else {
		a.Redis.LTrim(ctx, "resume:go:queue", 0, 31)
	}
}
func (a *App) Workers(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.maintenance(ctx) }()
	go func() { defer wg.Done(); a.scheduler(ctx) }()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.worker(ctx) }()
	}
	wg.Wait()
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (a *App) worker(ctx context.Context) {
	for ctx.Err() == nil {
		workerToken := token(16)
		job, err := one(ctx, a.Pool, `UPDATE platform_go_jobs AS j SET status='running',worker_token=$1,lease_until=now()+interval '20 seconds',attempts=attempts+1 WHERE id=(SELECT id FROM platform_go_jobs WHERE status='pending' OR (status='running' AND lease_until<now()) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING row_to_json(j)`, workerToken)
		if err != nil {
			var e *apiError
			if !errors.As(err, &e) {
				a.Log.Error("task queue unavailable", "error_type", fmt.Sprintf("%T", err))
			}
			waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			a.Redis.BLPop(waitCtx, time.Second, "resume:go:queue")
			cancel()
			pause(ctx, 100*time.Millisecond)
			continue
		}
		a.executeJob(ctx, job, workerToken)
	}
}
func (a *App) executeJob(parent context.Context, job Object, workerToken string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				tag, err := a.Pool.Exec(ctx, "UPDATE platform_go_jobs SET lease_until=now()+interval '20 seconds' WHERE id=$1 AND worker_token=$2 AND status='running'", job["id"], workerToken)
				if err != nil || tag.RowsAffected() != 1 {
					cancel()
					return
				}
				var cancelled bool
				err = a.Pool.QueryRow(ctx, "SELECT cancel_requested_at IS NOT NULL FROM core_processingrun WHERE id=$1", job["run_id"]).Scan(&cancelled)
				if err != nil || cancelled {
					cancel()
					return
				}
				if _, err = a.Pool.Exec(ctx, "UPDATE core_processingrun SET last_heartbeat_at=now() WHERE id=$1", job["run_id"]); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := a.executeRun(ctx, job["run_id"])
	close(stop)
	cancel()
	<-done
	// 进程退出或租约丢失时留给下一次领取恢复，禁止把未完成任务误记为成功。
	if parent.Err() != nil {
		return
	}
	finishCtx, finishCancel := context.WithTimeout(parent, 10*time.Second)
	defer finishCancel()
	var own bool
	if a.Pool.QueryRow(finishCtx, "SELECT worker_token=$2 AND lease_until>now() FROM platform_go_jobs WHERE id=$1", job["id"], workerToken).Scan(&own) != nil || !own {
		return
	}
	run, getErr := a.get(finishCtx, a.Pool, "core_processingrun", job["run_id"])
	if getErr != nil {
		return
	}
	if run["cancel_requested_at"] != nil {
		err = taskError("agent_cancelled", "任务已取消")
	}
	if errors.Is(err, context.Canceled) && run["cancel_requested_at"] == nil {
		return
	}
	if err != nil {
		if e := a.stopRun(finishCtx, run, err); e != nil {
			a.Log.Error("task failure could not be persisted")
			return
		}
	}
	if _, err = a.Pool.Exec(finishCtx, "UPDATE platform_go_jobs SET status='done',lease_until=NULL WHERE id=$1 AND worker_token=$2", job["id"], workerToken); err != nil {
		a.Log.Error("task queue completion failed")
	}
}
func (a *App) beginStage(ctx context.Context, runID any, step string) error {
	tag, err := a.Pool.Exec(ctx, "UPDATE core_processingrun SET status='running',current_stage=$2,message=$3,last_heartbeat_at=now(),started_at=COALESCE(started_at,now()) WHERE id=$1 AND cancel_requested_at IS NULL", runID, step, stageDescriptions[step])
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return taskError("agent_cancelled", "任务已取消")
	}
	_, err = a.Pool.Exec(ctx, "UPDATE core_processingrunstage SET status='running',started_at=COALESCE(started_at,now()),message=$3,error='' WHERE run_id=$1 AND step=$2 AND status<>'success'", runID, step, stageDescriptions[step])
	return err
}
func (a *App) finishStage(ctx context.Context, runID any, step string) error {
	_, err := a.Pool.Exec(ctx, "UPDATE core_processingrunstage SET status=CASE WHEN step='step4' AND failed_count>0 THEN 'partial_failed' WHEN step='step4' AND needs_attention_count>0 THEN 'needs_attention' ELSE 'success' END,started_at=COALESCE(started_at,now()),finished_at=now(),processed_count=CASE WHEN step='step4' THEN processed_count ELSE total_count END,success_count=CASE WHEN step='step4' THEN success_count ELSE total_count END WHERE run_id=$1 AND step=$2", runID, step)
	return err
}
func (a *App) executeRun(ctx context.Context, id any) error {
	run, err := a.get(ctx, a.Pool, "core_processingrun", id)
	if err != nil {
		return err
	}
	if !activeRun(str(run["status"])) {
		return nil
	}
	if run["cancel_requested_at"] != nil {
		return taskError("agent_cancelled", "任务已取消")
	}
	if err = a.finishStage(ctx, id, "queued"); err != nil {
		return err
	}
	pin := Object{}
	for _, key := range []string{"kernel_build", "protocol_version", "toolset_version", "result_schema_version", "policy_version", "instruction_version", "model_config_revision", "pin_id"} {
		if key == "instruction_version" {
			pin[key] = run["prompt_version"]
		} else {
			pin[key] = run[key]
		}
	}
	params := obj(run["params"])
	if params["runtime_initialization"] == "pending" {
		if err = a.beginStage(ctx, id, "initialize"); err != nil {
			return err
		}
		c := Object{}
		if contains(stageSteps(str(run["step"])), "step4") {
			pin, c, err = a.runtimePin(ctx)
			if err != nil {
				return err
			}
		}
		coefficient := max(int64(1), min(int64(100), num(a.configValue(ctx, "job_hc_coefficient", 1))))
		limit := max(int64(1), min(int64(20), num(a.configValue(ctx, "ai_concurrency_limit", 8))))
		tx, err := a.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		params["runtime_initialization"] = "ready"
		values := brief(pin, "kernel_build", "protocol_version", "toolset_version", "result_schema_version", "policy_version", "model_config_revision", "pin_id")
		for k, v := range values {
			if v == nil {
				delete(values, k)
			}
		}
		values["params"] = params
		values["model_name"] = str(c["model_name"])
		values["prompt_version"] = str(pin["instruction_version"])
		values["decision_version"] = policyVersion
		values["job_hc_coefficient_snapshot"] = coefficient
		values["ai_concurrency_limit"] = limit
		values["ai_effective_concurrency"] = limit
		run, err = a.save(ctx, tx, "core_processingrun", id, values)
		if err != nil {
			return err
		}
		jobs, err := a.all(ctx, tx, "core_job")
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if _, err = a.save(ctx, tx, "core_processingrunjobcapacity", nil, Object{"run_id": id, "job_id": job["id"], "headcount_snapshot": job["headcount"], "coefficient_snapshot": coefficient, "capacity": num(job["headcount"]) * coefficient}); err != nil {
				return err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	if err = a.finishStage(ctx, id, "initialize"); err != nil {
		return err
	}
	if params["snapshot_preparation"] != "ready" {
		if str(run["step"]) != "step1" {
			if err = a.beginStage(ctx, id, "preparing"); err != nil {
				return err
			}
		}
		if err = a.prepareRunSnapshots(ctx, run, pin); err != nil {
			return err
		}
		params["snapshot_preparation"] = "ready"
		if _, err = a.save(ctx, a.Pool, "core_processingrun", id, Object{"params": params}); err != nil {
			return err
		}
		if str(run["step"]) != "step1" {
			if err = a.finishStage(ctx, id, "preparing"); err != nil {
				return err
			}
		}
	}
	for _, step := range stageSteps(str(run["step"])) {
		stage, err := one(ctx, a.Pool, "SELECT row_to_json(s) FROM core_processingrunstage s WHERE run_id=$1 AND step=$2", id, step)
		if err != nil {
			return err
		}
		if stage["status"] == "success" {
			continue
		}
		if err = a.beginStage(ctx, id, step); err != nil {
			return err
		}
		if step == "step4" {
			err = a.analyzeItems(ctx, run)
		} else {
			err = a.prepareItems(ctx, run, step)
		}
		if err != nil {
			return err
		}
		if err = a.finishStage(ctx, id, step); err != nil {
			return err
		}
	}
	if err = a.beginStage(ctx, id, "finalize"); err != nil {
		return err
	}
	if err = a.summarizeRun(ctx, id, true); err != nil {
		return err
	}
	return a.finishStage(ctx, id, "finalize")
}
func (a *App) stopRun(ctx context.Context, run Object, cause error) error {
	code, message := failureInfo(cause)
	status := "failed"
	if code == "agent_cancelled" {
		status = "cancelled"
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	values := Object{"status": status, "finished_at": time.Now().UTC(), "message": message, "error": message}
	if status == "cancelled" {
		values["cancelled_at"] = time.Now().UTC()
		values["error"] = ""
	}
	if _, err = a.save(ctx, tx, "core_processingrun", run["id"], values); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE core_processingrunstage SET status=$2::text,message=$3::text,error=CASE WHEN $2::text='failed' THEN $3::text ELSE '' END,finished_at=now() WHERE run_id=$1 AND status IN ('running','waiting_conflict')", run["id"], status, message); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE core_processingrunstage SET status='skipped',message='任务已停止，此节点未执行',finished_at=now() WHERE run_id=$1 AND status='pending'", run["id"]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE core_processingrunscopeitem SET status=$2::text,result_type=$2::text,processing_node=$2::text,error_code=$3::text,error_message=$4::text,result_message=$4::text,reason_code=$3::text,finished_at=now() WHERE run_id=$1 AND status IN ('pending','processing','waiting_conflict')", run["id"], status, code, message); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE core_candidateworkflow SET active_processing_scope_item_id=NULL,active_processing_token=NULL,active_processing_expires_at=NULL WHERE active_processing_scope_item_id IN (SELECT id FROM core_processingrunscopeitem WHERE run_id=$1)", run["id"]); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return a.summarizeRun(ctx, run["id"], false)
}
func (a *App) summarizeRun(ctx context.Context, id any, finish bool) error {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	run, err := one(ctx, tx, "SELECT row_to_json(r) FROM core_processingrun r WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return err
	}
	if finish && run["cancel_requested_at"] != nil {
		return taskError("agent_cancelled", "任务已取消")
	}
	counts, err := one(ctx, tx, `SELECT row_to_json(c) FROM (SELECT count(*) FILTER(WHERE finished_at IS NOT NULL) processed_count,count(*) FILTER(WHERE result_type='completed') completed_count,count(*) FILTER(WHERE status='success') success_count,count(*) FILTER(WHERE result_type='needs_attention') needs_attention_count,count(*) FILTER(WHERE result_type='failed') failed_count,count(*) FILTER(WHERE status='skipped_manual_change') skipped_count,count(*) FILTER(WHERE status='cancelled') cancelled_count FROM core_processingrunscopeitem WHERE run_id=$1) c`, id)
	if err != nil {
		return err
	}
	recs, err := rows(ctx, tx, "SELECT row_to_json(c) FROM (SELECT recommendation,count(*) n FROM core_agentdispatchdecision WHERE processing_run_id=$1 AND recommendation IS NOT NULL GROUP BY recommendation) c", id)
	if err != nil {
		return err
	}
	for _, key := range []string{"review", "dispatch", "archive"} {
		counts[key+"_count"] = 0
	}
	for _, rec := range recs {
		counts[str(rec["recommendation"])+"_count"] = rec["n"]
	}
	if finish {
		counts["status"] = "success"
		if num(counts["failed_count"]) > 0 {
			counts["status"] = "partial_failed"
		} else if num(counts["needs_attention_count"]) > 0 {
			counts["status"] = "needs_attention"
		}
		counts["current_stage"] = ""
		counts["last_heartbeat_at"] = time.Now().UTC()
		counts["finished_at"] = time.Now().UTC()
		counts["message"] = fmt.Sprintf("处理完成：完成 %d，需处理 %d，失败 %d，跳过 %d", num(counts["completed_count"]), num(counts["needs_attention_count"]), num(counts["failed_count"]), num(counts["skipped_count"]))
	}
	_, err = a.save(ctx, tx, "core_processingrun", id, counts)
	if err != nil {
		return err
	}
	delete(counts, "status")
	delete(counts, "finished_at")
	delete(counts, "message")
	delete(counts, "current_stage")
	delete(counts, "last_heartbeat_at")
	stage, err := one(ctx, tx, "SELECT row_to_json(s) FROM core_processingrunstage s WHERE run_id=$1 AND step='step4'", id)
	if err == nil {
		_, err = a.save(ctx, tx, "core_processingrunstage", stage["id"], counts)
	} else {
		var e *apiError
		if errors.As(err, &e) && e.Status == 404 {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (a *App) cancelRun(ctx context.Context, id any, p *Principal) (Object, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	run, err := one(ctx, tx, "SELECT row_to_json(r) FROM core_processingrun r WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return nil, err
	}
	if !activeRun(str(run["status"])) {
		return nil, &apiError{409, "任务已结束，不能取消"}
	}
	if run["cancel_requested_at"] == nil {
		run, err = a.save(ctx, tx, "core_processingrun", id, Object{"status": "cancelling", "cancel_requested_at": time.Now().UTC(), "cancelled_by_id": p.User["id"], "cancelled_by_username_snapshot": p.User["username"], "message": "正在停止提取与分析，取消后续写入"})
		if err != nil {
			return nil, err
		}
	}
	return run, tx.Commit(ctx)
}
func (a *App) acquireModelSlot(ctx context.Context) (func(), error) {
	limit := max(int64(1), min(int64(20), num(a.configValue(ctx, "ai_concurrency_limit", 8))))
	owner := token(16)
	key := "resume:go:model-slots"
	for ctx.Err() == nil {
		now := time.Now().UnixMilli()
		result, err := a.Redis.Eval(ctx, `redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',ARGV[1]); if redis.call('ZCARD',KEYS[1]) < tonumber(ARGV[2]) then redis.call('ZADD',KEYS[1],ARGV[3],ARGV[4]); redis.call('PEXPIRE',KEYS[1],700000); return 1 end; return 0`, []string{key}, now, limit, now+650000, owner).Int()
		if err != nil {
			return nil, taskError("processing_queue_unavailable", "任务队列暂不可用，请重试")
		}
		if result == 1 {
			return func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				a.Redis.ZRem(cleanup, key, owner)
			}, nil
		}
		if !pause(ctx, 200*time.Millisecond) {
			break
		}
	}
	return nil, ctx.Err()
}
func runIDString(id any) string { return strconv.FormatInt(num(id), 10) }
