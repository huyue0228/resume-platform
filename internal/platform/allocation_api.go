package platform

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A task exposes the entire pool's decision set. Department grants must cover every demand.
func (a *App) canAccessAllocationScope(ctx context.Context, db DB, p *Principal, scope Object, writeAccess bool) bool {
	if p == nil || !p.has("resume.view") || writeAccess && !p.has("attempt.dispatch") {
		return false
	}
	if p.has("attempt.view_all") {
		return true
	}
	config, err := a.poolPolicy(ctx, db)
	if err != nil {
		return false
	}
	policy := obj(config["policy"])
	found := false
	for _, v := range list(policy["rules"]) {
		rule := obj(v)
		if rule["pool_code"] != scope["pool_code"] || !activePolicyItem(rule) {
			continue
		}
		j, err := a.get(ctx, db, "core_job", rule["job_id"])
		if err != nil {
			return false
		}
		d, err := a.get(ctx, db, "core_department", j["department_id"])
		if err != nil {
			return false
		}
		covered := false
		for _, g := range p.departmentGrants() {
			if isDepartmentHR(g.Contact["contact_level"]) && p.grantHas(g, "attempt.view_department") && (!writeAccess || p.grantHas(g, "attempt.dispatch")) && a.grantCovers(ctx, db, g, d) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
		found = true
	}
	return found
}
func (a *App) authorizeAllocationTask(ctx context.Context, db DB, scope, task Object) error {
	if task["created_by"] == nil {
		return nil
	}
	user, err := a.get(ctx, db, "accounts_user", task["created_by"])
	if err != nil || !truth(user["is_active"]) {
		return &apiError{403, "任务创建者已停用"}
	}
	p, err := a.userPrincipal(ctx, user)
	if err != nil {
		return err
	}
	if !a.canAccessAllocationScope(ctx, db, p, scope, true) {
		return &apiError{403, "任务创建者已无当前分配范围权限"}
	}
	return nil
}
func (a *App) allocationAPI(w http.ResponseWriter, r *http.Request, path string, p *Principal) error {
	ctx := r.Context()
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return &apiError{404, "未找到"}
	}
	if parts[1] == "allocation-scopes" {
		return a.allocationScopesAPI(w, r, parts, p)
	}
	if parts[1] == "allocation-plans" {
		if len(parts) != 3 || r.Method != "GET" {
			return &apiError{405, "请求方法不允许"}
		}
		plan, err := one(ctx, a.Pool, `SELECT row_to_json(p) FROM platform_allocation_plans p WHERE id=$1`, parts[2])
		if err != nil {
			return err
		}
		task, err := one(ctx, a.Pool, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE id=$1`, plan["task_id"])
		if err != nil {
			return err
		}
		scope, err := one(ctx, a.Pool, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE id=$1`, task["scope_id"])
		if err != nil {
			return err
		}
		if !a.canAccessAllocationScope(ctx, a.Pool, p, scope, false) {
			return &apiError{403, "无方案查看权限"}
		}
		write(w, 200, plan)
		return nil
	}
	if parts[1] != "allocation-tasks" {
		return &apiError{404, "未找到"}
	}
	if len(parts) == 2 && r.Method == "GET" {
		scopes, err := rows(ctx, a.Pool, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE ($1::bigint=0 OR s.id=$1) ORDER BY id`, num(r.URL.Query().Get("scope_id")))
		if err != nil {
			return err
		}
		allowed := []int64{}
		for _, scope := range scopes {
			if a.canAccessAllocationScope(ctx, a.Pool, p, scope, false) {
				allowed = append(allowed, num(scope["id"]))
			}
		}
		var count int
		if err = a.Pool.QueryRow(ctx, `SELECT count(*) FROM platform_allocation_tasks WHERE scope_id=ANY($1)`, allowed).Scan(&count); err != nil {
			return err
		}
		page, size, err := pageBounds(r, count)
		if err != nil {
			return err
		}
		tasks, err := rows(ctx, a.Pool, `SELECT to_jsonb(t)-'snapshot'-'pin'-'worker_token'-'lease_until'-'kernel_namespace' FROM platform_allocation_tasks t WHERE scope_id=ANY($1) ORDER BY id DESC LIMIT $2 OFFSET $3`, allowed, size, (page-1)*size)
		if err != nil {
			return err
		}
		writePage(w, r, tasks, count, page, size)
		return nil
	}
	if len(parts) == 2 && r.Method == "POST" {
		body, err := readBody(w, r)
		if err != nil {
			return err
		}
		tx, err := a.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		scope, err := one(ctx, tx, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE id=$1 FOR UPDATE`, body["scope_id"])
		if err != nil {
			return err
		}
		if !a.canAccessAllocationScope(ctx, tx, p, scope, true) {
			return &apiError{403, "无当前范围分配权限"}
		}
		mode := str(body["mode"])
		if mode == "execute" && scope["allocation_mode"] != "execute_v1" {
			return &apiError{409, "请先在职位池启用新分配执行模式"}
		}
		ids := []int64{}
		seen := map[int64]bool{}
		for _, v := range list(body["member_ids"]) {
			id := num(v)
			if id <= 0 || seen[id] {
				return bad("成员 ID 无效或重复")
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			members, e := rows(ctx, tx, `SELECT jsonb_build_object('id',id) FROM platform_pool_memberships WHERE pool_code=$1 AND assessment->'pool'->>'entity'=$2 AND status='pending_allocation' ORDER BY created_at,id LIMIT 100`, scope["pool_code"], scope["entity"])
			if e != nil {
				return e
			}
			for _, m := range members {
				ids = append(ids, num(m["id"]))
			}
		}
		for _, id := range ids {
			var valid bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_pool_memberships WHERE id=$1 AND pool_code=$2 AND assessment->'pool'->>'entity'=$3 AND status='pending_allocation')`, id, scope["pool_code"], scope["entity"]).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return bad("成员不属于当前范围或不在待分配状态")
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		task, err := a.createAllocationTask(ctx, tx, scope, ids, mode, str(body["idempotency_key"]), p, false, "v1")
		if err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		a.wakeAllocation(ctx)
		write(w, 202, Object{"task_id": task["id"], "code": "allocation_queued", "detail": "分配任务已提交"})
		return nil
	}
	if len(parts) < 3 {
		return &apiError{405, "请求方法不允许"}
	}
	task, err := one(ctx, a.Pool, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE id=$1`, parts[2])
	if err != nil {
		return err
	}
	scope, err := one(ctx, a.Pool, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE id=$1`, task["scope_id"])
	if err != nil {
		return err
	}
	if !a.canAccessAllocationScope(ctx, a.Pool, p, scope, r.Method != "GET") {
		return &apiError{403, "无分配任务权限"}
	}
	if len(parts) == 3 && r.Method == "GET" {
		plans, err := rows(ctx, a.Pool, `SELECT row_to_json(p) FROM platform_allocation_plans p WHERE task_id=$1 ORDER BY generation`, task["id"])
		if err != nil {
			return err
		}
		items, err := rows(ctx, a.Pool, `SELECT row_to_json(i) FROM platform_allocation_work_items i WHERE task_id=$1 ORDER BY id`, task["id"])
		if err != nil {
			return err
		}
		screening, err := rows(ctx, a.Pool, `SELECT DISTINCT jsonb_build_object('run_id',m.run_id) FROM platform_pool_memberships m JOIN platform_allocation_tasks t ON t.id=$1 WHERE t.member_ids @> to_jsonb(m.id)`, task["id"])
		if err != nil {
			return err
		}
		task["screening_runs"] = screening
		delete(task, "kernel_namespace")
		delete(task, "snapshot")
		delete(task, "worker_token")
		delete(task, "lease_until")
		task["plans"], task["items"] = plans, items
		write(w, 200, task)
		return nil
	}
	if len(parts) != 4 || r.Method != "POST" || !contains([]string{"retry", "cancel"}, parts[3]) {
		return &apiError{405, "请求方法不允许"}
	}
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	scope, err = one(ctx, tx, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE id=$1 FOR UPDATE`, scope["id"])
	if err != nil {
		return err
	}
	task, err = one(ctx, tx, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE id=$1 FOR UPDATE`, task["id"])
	if err != nil {
		return err
	}
	if parts[3] == "cancel" {
		if task["status"] == "completed" {
			return &apiError{409, "已完成任务不能通过取消撤销业务归属"}
		}
		if num(task["created_by"]) != num(p.User["id"]) && !p.isAdministrator() {
			return &apiError{403, "仅创建者或管理员可取消任务"}
		}
		if _, err = tx.Exec(ctx, `UPDATE platform_allocation_tasks SET status='cancelled',updated_at=now() WHERE id=$1`, task["id"]); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE platform_allocation_plans SET status='cancelled' WHERE task_id=$1 AND status='proposed'`, task["id"]); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items SET status='cancelled',reason_code='task_cancelled' WHERE task_id=$1 AND status IN ('pending','leased')`, task["id"]); err != nil {
			return err
		}
		if err = a.releaseAllocationLease(ctx, tx, scope, task); err != nil {
			return err
		}
	} else {
		if !contains([]string{"failed", "cancelled", "completed"}, str(task["status"])) {
			return &apiError{409, "任务仍在运行"}
		}
		ids := []int64{}
		for _, id := range list(task["member_ids"]) {
			ids = append(ids, num(id))
		}
		key := str(body["idempotency_key"])
		if key == "" {
			return bad("重试须提供幂等键")
		}
		task, err = a.createAllocationTask(ctx, tx, scope, ids, str(task["mode"]), key, p, false, str(task["execution_version"]))
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	a.wakeAllocation(ctx)
	write(w, 202, Object{"task_id": task["id"], "detail": "操作已记录"})
	return nil
}
func (a *App) allocationScopesAPI(w http.ResponseWriter, r *http.Request, parts []string, p *Principal) error {
	ctx := r.Context()
	if len(parts) == 2 && r.Method == "GET" {
		scopes, err := rows(ctx, a.Pool, `SELECT to_jsonb(s)-'lease_token'-'lease_until' FROM platform_allocation_scopes s ORDER BY s.id`)
		if err != nil {
			return err
		}
		visible := []Object{}
		for _, s := range scopes {
			if a.canAccessAllocationScope(ctx, a.Pool, p, s, false) {
				visible = append(visible, s)
			}
		}
		write(w, 200, Object{"results": visible})
		return nil
	}
	if len(parts) < 3 {
		return &apiError{405, "请求方法不允许"}
	}
	scope, err := one(ctx, a.Pool, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE id=$1`, parts[2])
	if err != nil {
		return err
	}
	if !a.canAccessAllocationScope(ctx, a.Pool, p, scope, false) {
		return &apiError{403, "无需求供给查看权限"}
	}
	if len(parts) == 4 && parts[3] == "supply" && r.Method == "GET" {
		supply, err := a.allocationSupply(ctx, a.Pool, scope["id"], time.Now().UTC())
		if err != nil {
			return err
		}
		indexed := map[int64]Object{}
		for _, s := range supply {
			indexed[num(s["demand_id"])] = s
		}
		config, err := a.poolPolicy(ctx, a.Pool)
		if err != nil {
			return err
		}
		results := []Object{}
		for _, v := range list(obj(config["policy"])["rules"]) {
			rule := obj(v)
			if rule["pool_code"] != scope["pool_code"] {
				continue
			}
			j, err := one(ctx, a.Pool, `SELECT jsonb_build_object('demand_id',j.id,'position_name',j.position_name,'public_name',j.public_name,'is_public',j.is_public,'headcount',j.headcount,'is_active',j.is_active,'department_name',d.name,'reception_state',ds.reception_state,'revision',ds.revision) FROM core_job j JOIN core_department d ON d.id=j.department_id JOIN platform_demand_settings ds ON ds.demand_id=j.id WHERE j.id=$1`, rule["job_id"])
			if err != nil {
				return err
			}
			j["mapping_active"] = activePolicyItem(rule)
			j["recent_supply_count"], j["pending_dispatch"], j["awaiting_feedback"] = 0, 0, 0
			for k, v := range indexed[num(j["demand_id"])] {
				j[k] = v
			}

			results = append(results, j)
		}
		unknown, err := rows(ctx, a.Pool, `SELECT jsonb_build_object('attempt_id',attempt_id,'member_id',member_id,'department_name',department_name,'allocated_at',allocated_at) FROM platform_assignment_targets WHERE scope_id=$1 AND target_kind='unknown' ORDER BY attempt_id`, scope["id"])
		if err != nil {
			return err
		}
		write(w, 200, Object{"results": results, "window_days": 7, "unknown_targets": unknown})
		return nil
	}
	if len(parts) != 3 || r.Method != "PATCH" {
		return &apiError{405, "请求方法不允许"}
	}
	if !p.has("settings.manage_config") {
		return &apiError{403, "无分配模式配置权限"}
	}
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	mode := str(body["allocation_mode"])
	paused, hasPause := body["paused"]
	if hasPause {
		if _, ok := paused.(bool); !ok {
			return bad("暂停状态必须是布尔值")
		}
	}
	if mode == "" && hasPause {
		mode = str(scope["allocation_mode"])
	}
	if !contains([]string{"legacy", "simulate", "execute_v1"}, mode) || strings.TrimSpace(str(body["reason"])) == "" {
		return bad("请选择分配模式并填写原因")
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	scope, err = one(ctx, tx, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE id=$1 FOR UPDATE`, scope["id"])
	if err != nil {
		return err
	}
	if num(body["expected_revision"]) != num(scope["revision"]) {
		return &apiError{409, "范围版本已变化，请刷新"}
	}
	if scope["lease_until"] != nil && !(hasPause && truth(paused)) {
		at, _ := time.Parse(time.RFC3339Nano, str(scope["lease_until"]))
		if at.After(time.Now()) {
			return &apiError{409, "当前分配任务尚未结束，请稍后切换"}
		}
	}
	if mode == "execute_v1" && scope["allocation_mode"] != mode {
		if err = a.backfillAllocationScope(ctx, tx, scope); err != nil {
			return err
		}
	}
	previous := scope["allocation_mode"]
	if !hasPause {
		paused = scope["paused"]
	}
	if mode != previous && scope["allocation_mode"] == "execute_v1" && mode == "legacy" && !truth(scope["paused"]) {
		return &apiError{409, "回退前请先暂停新分配"}
	}
	scope, err = one(ctx, tx, `UPDATE platform_allocation_scopes s SET allocation_mode=$2,paused=$3,epoch=epoch+1,revision=revision+1,lease_token='',lease_until=NULL WHERE id=$1 RETURNING row_to_json(s)`, scope["id"], mode, paused)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_tasks SET status='cancelled',error_code='allocation_mode_changed' WHERE scope_id=$1 AND status IN ('pending','running')`, scope["id"]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_plans SET status='cancelled',validation_code='allocation_scope_changed' WHERE status='proposed' AND task_id IN (SELECT id FROM platform_allocation_tasks WHERE scope_id=$1 AND status='cancelled');`, scope["id"]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items SET status='pending',task_id=NULL,reason_code='' WHERE status IN ('pending','leased') AND task_id IN (SELECT id FROM platform_allocation_tasks WHERE scope_id=$1 AND status='cancelled')`, scope["id"]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform_allocation_audit(scope_id,actor_id,kind,payload) VALUES($1,$2,'mode_changed',$3::jsonb)`, scope["id"], p.User["id"], string(canonicalJSON(Object{"previous": previous, "mode": mode, "paused": paused, "reason": body["reason"]}, false))); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	delete(scope, "lease_token")
	delete(scope, "lease_until")
	a.wakeAllocation(ctx)
	write(w, 200, scope)
	return nil
}
func (a *App) demandReceptionAPI(w http.ResponseWriter, r *http.Request, path string, p *Principal) error {
	if r.Method != "PATCH" {
		return &apiError{405, "请求方法不允许"}
	}
	if !p.has("job.manage") {
		return &apiError{403, "无岗位维护权限"}
	}
	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return bad("岗位 ID 无效")
	}
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	state := str(body["reception_state"])
	if !contains([]string{"receiving", "paused", "closed"}, state) || strings.TrimSpace(str(body["reason"])) == "" {
		return bad("请选择接收状态并填写原因")
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	job, err := a.get(r.Context(), tx, "core_job", id)
	if err != nil {
		return err
	}
	department, err := a.get(r.Context(), tx, "core_department", job["department_id"])
	if err != nil {
		return err
	}
	covered := p.isAdministrator() || p.GlobalPermissions["job.manage"]
	for _, grant := range p.departmentGrants() {
		if p.grantHas(grant, "job.manage") && a.grantCovers(r.Context(), tx, grant, department) {
			covered = true
		}
	}
	if !covered {
		return &apiError{403, "无当前需求所属部门的维护权限"}
	}
	if err = a.syncAllocationConfig(r.Context(), tx); err != nil {
		return err
	}
	value, err := one(r.Context(), tx, `UPDATE platform_demand_settings d SET reception_state=$2,revision=revision+1,updated_at=now(),updated_by=$3,reason=$4 WHERE demand_id=$1 AND revision=$5 RETURNING row_to_json(d)`, id, state, p.User["id"], body["reason"], body["expected_revision"])
	if noPoolRecord(err) {
		return &apiError{409, "需求版本已变化，请刷新"}
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO platform_allocation_audit(demand_id,actor_id,kind,payload) VALUES($1,$2,'reception_changed',$3::jsonb)`, id, p.User["id"], string(canonicalJSON(value, false))); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	a.wakeAllocation(r.Context())
	write(w, 200, value)
	return nil
}

func (a *App) canReadPoolMember(ctx context.Context, p *Principal, m Object) bool {
	if !p.has("resume.view") {
		return false
	}
	if p.has("attempt.view_all") {
		return true
	}
	if m["status"] == "allocated" {
		at, err := one(ctx, a.Pool, `SELECT row_to_json(a) FROM core_assignmentattempt a WHERE agent_decision_id=$1 AND status IN ('pending_review','pending_dispatch','dispatched','passed') ORDER BY id DESC LIMIT 1`, m["decision_id"])
		return err == nil && a.visibleAttempt(ctx, p, at)
	}
	scope, err := a.allocationScope(ctx, a.Pool, m, false)
	return err == nil && a.canAccessAllocationScope(ctx, a.Pool, p, scope, false)
}
