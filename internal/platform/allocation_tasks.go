package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	c "resume-platform/internal/contract"
	"time"
)

func (a *App) createAllocationTask(ctx context.Context, db DB, scope Object, ids []int64, mode, key string, p *Principal, automatic bool, version string) (Object, error) {
	if truth(scope["paused"]) {
		return nil, &apiError{409, "当前范围已暂停分配"}
	}
	if len(ids) == 0 || len(ids) > 100 || !contains([]string{"simulate", "execute"}, mode) {
		return nil, bad("分配任务须包含 1 到 100 名候选人")
	}
	if key == "" {
		key = token(16)
	}
	if len(key) > 128 {
		return nil, bad("幂等键过长")
	}
	hash := fingerprint(Object{"scope_id": scope["id"], "member_ids": ids, "mode": mode, "version": version})
	var actor any
	if p != nil {
		actor = p.User["id"]
	}
	existing, err := one(ctx, db, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE idempotency_key=$1`, key)
	if err == nil {
		if existing["input_hash"] != hash || num(existing["created_by"]) != num(actor) {
			return nil, &apiError{409, "幂等键已用于不同请求"}
		}
		return existing, nil
	}
	if !noPoolRecord(err) {
		return nil, err
	}
	task, err := one(ctx, db, `INSERT INTO platform_allocation_tasks(scope_id,mode,idempotency_key,input_hash,member_ids,created_by,automatic,execution_version) VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,$8) ON CONFLICT(idempotency_key) DO NOTHING RETURNING row_to_json(platform_allocation_tasks)`, scope["id"], mode, key, hash, string(canonicalJSON(ids, false)), actor, automatic, version)
	if noPoolRecord(err) {
		existing, e := one(ctx, db, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE idempotency_key=$1`, key)
		if e != nil {
			return nil, e
		}
		if existing["input_hash"] != hash || num(existing["created_by"]) != num(actor) {
			return nil, &apiError{409, "幂等键已用于不同请求"}
		}
		return existing, nil
	}
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err = db.Exec(ctx, `INSERT INTO platform_allocation_work_items(member_id,qualification_id,task_id) SELECT $1,q.id,$2 FROM platform_screening_qualifications q WHERE q.member_id=$1 ORDER BY revision DESC LIMIT 1 ON CONFLICT(qualification_id) WHERE status IN ('pending','leased') DO NOTHING`, id, task["id"]); err != nil {
			return nil, err
		}
	}
	if _, err = db.Exec(ctx, `UPDATE platform_allocation_work_items SET task_id=$2 WHERE member_id=ANY($1) AND status='pending' AND task_id IS NULL`, ids, task["id"]); err != nil {
		return nil, err
	}
	return task, nil
}
func (a *App) enqueuePoolAllocation(ctx context.Context, db DB, member Object, p *Principal) (Object, error) {
	scope, err := a.allocationScope(ctx, db, member, true)
	if err != nil {
		return nil, err
	}
	mode, version := "execute", "v1"
	if scope["allocation_mode"] == "legacy" {
		version = "legacy"
	}
	if scope["allocation_mode"] == "simulate" {
		mode = "simulate"
	}
	return a.createAllocationTask(ctx, db, scope, []int64{num(member["id"])}, mode, "", p, false, version)
}
func (a *App) lockCandidateAllocationScopes(ctx context.Context, db DB, candidateID any) error {
	var ready bool
	if err := db.QueryRow(ctx, `SELECT to_regclass('platform_allocation_scopes') IS NOT NULL`).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return nil
	}
	_, err := rows(ctx, db, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE EXISTS(SELECT 1 FROM platform_pool_memberships m WHERE m.candidate_id=$1 AND m.status IN ('pending_allocation','allocated','needs_reanalysis') AND m.pool_code=s.pool_code AND m.assessment->'pool'->>'entity'=s.entity) ORDER BY s.id FOR UPDATE`, candidateID)
	return err
}
func (a *App) claimAllocation(ctx context.Context) (Object, Object, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	scope, err := one(ctx, tx, `
SELECT row_to_json(s) FROM platform_allocation_scopes s
WHERE NOT s.paused AND (s.lease_until IS NULL OR s.lease_until < now())
  AND (
    EXISTS (SELECT 1 FROM platform_allocation_tasks t WHERE t.scope_id=s.id AND t.status IN ('pending','running'))
    OR (s.allocation_mode <> 'legacy' AND EXISTS (
      SELECT 1 FROM platform_allocation_work_items i
      JOIN platform_pool_memberships m ON m.id=i.member_id
      WHERE m.pool_code=s.pool_code AND m.assessment->'pool'->>'entity'=s.entity
        AND m.status='pending_allocation'
        AND i.id=(SELECT max(last.id) FROM platform_allocation_work_items last WHERE last.qualification_id=i.qualification_id)
        AND NOT EXISTS(SELECT 1 FROM platform_screening_qualifications newer WHERE newer.member_id=m.id AND newer.id>i.qualification_id)
        AND (i.status='pending' OR (i.status='waiting' AND i.observed_revision <
          s.revision+COALESCE((SELECT max(id) FROM platform_allocation_changes WHERE scope_id=s.id),0)))
    ))
  )
ORDER BY s.id FOR UPDATE SKIP LOCKED LIMIT 1`)
	if err != nil {
		return nil, nil, err
	}
	// Expired workers cannot commit; recovery uses a fresh generation and snapshot.
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_tasks SET status=CASE WHEN generation>=3 THEN 'failed' ELSE 'pending' END,error_code='allocation_lease_expired',generation=generation+1,worker_token='',lease_until=NULL,snapshot=NULL WHERE scope_id=$1 AND status='running'`, scope["id"]); err != nil {
		return nil, nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items i SET status=CASE WHEN t.status='failed' THEN 'failed' ELSE 'pending' END FROM platform_allocation_tasks t WHERE i.task_id=t.id AND i.status='leased' AND t.scope_id=$1 AND t.status IN ('pending','failed')`, scope["id"]); err != nil {
		return nil, nil, err
	}
	task, err := one(ctx, tx, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE scope_id=$1 AND status='pending' ORDER BY id LIMIT 1 FOR UPDATE`, scope["id"])
	if noPoolRecord(err) {
		// Wake waiting qualifications only when inputs changed. New active work supersedes old waits.
		revision, e := a.allocationRevision(ctx, tx, scope)
		if e != nil {
			return nil, nil, e
		}
		_, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items i SET status='pending',task_id=NULL WHERE i.status='waiting' AND i.observed_revision<$1 AND EXISTS(SELECT 1 FROM platform_pool_memberships m WHERE m.id=i.member_id AND m.pool_code=$2 AND m.assessment->'pool'->>'entity'=$3 AND m.status='pending_allocation') AND NOT EXISTS(SELECT 1 FROM platform_allocation_work_items other WHERE other.qualification_id=i.qualification_id AND other.status IN ('pending','leased')) AND i.id=(SELECT max(last.id) FROM platform_allocation_work_items last WHERE last.qualification_id=i.qualification_id)`, revision, scope["pool_code"], scope["entity"])
		if err != nil {
			return nil, nil, err
		}
		items, e := rows(ctx, tx, `SELECT jsonb_build_object('id',m.id) FROM platform_pool_memberships m WHERE m.pool_code=$1 AND m.assessment->'pool'->>'entity'=$2 AND m.status='pending_allocation' AND EXISTS(SELECT 1 FROM platform_allocation_work_items i WHERE i.member_id=m.id AND i.status='pending') ORDER BY m.created_at,m.id LIMIT 100`, scope["pool_code"], scope["entity"])
		if e != nil {
			return nil, nil, e
		}
		ids := []int64{}
		for _, i := range items {
			ids = append(ids, num(i["id"]))
		}
		if len(ids) == 0 {
			if err = tx.Commit(ctx); err != nil {
				return nil, nil, err
			}
			return nil, nil, &apiError{404, "无待分配成员"}
		}
		mode := "execute"
		if scope["allocation_mode"] == "simulate" {
			mode = "simulate"
		}
		task, err = a.createAllocationTask(ctx, tx, scope, ids, mode, "", nil, true, "v1")
	}
	if err != nil {
		return nil, nil, err
	}
	fence := token(16)
	scope, err = one(ctx, tx, `UPDATE platform_allocation_scopes s SET lease_token=$2,lease_until=now()+interval '90 seconds' WHERE id=$1 RETURNING row_to_json(s)`, scope["id"], fence)
	if err != nil {
		return nil, nil, err
	}
	task, err = one(ctx, tx, `UPDATE platform_allocation_tasks t SET status='running',worker_token=$2,lease_until=now()+interval '90 seconds',updated_at=now(),error_code='' WHERE id=$1 RETURNING row_to_json(t)`, task["id"], fence)
	if err != nil {
		return nil, nil, err
	}
	ids := []int64{}
	for _, id := range list(task["member_ids"]) {
		ids = append(ids, num(id))
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items SET status='leased',task_id=$2 WHERE member_id=ANY($1) AND status='pending'`, ids, task["id"]); err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return scope, task, nil
}
func (a *App) allocationWorker(ctx context.Context) {
	for ctx.Err() == nil {
		scope, task, err := a.claimAllocation(ctx)
		if err == nil {
			err = a.processAllocation(ctx, scope, task)
			if err != nil {
				a.finishAllocationFailure(ctx, scope, task, err)
			}
			continue
		}
		if !noPoolRecord(err) {
			a.Log.Error("allocation claim failed", "error_type", fmt.Sprintf("%T", err))
		}
		if a.Redis != nil {
			wait, cancel := context.WithTimeout(ctx, 3*time.Second)
			a.Redis.BLPop(wait, 2*time.Second, "resume:allocation:wakeup").Result()
			cancel()
		} else if !pause(ctx, 2*time.Second) {
			return
		}
	}
}
func (a *App) wakeAllocation(ctx context.Context) {
	if a.Redis != nil {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		a.Redis.LPush(ctx, "resume:allocation:wakeup", "1").Err()
	}
}
func (a *App) processAllocation(ctx context.Context, scope, task Object) error {
	if task["execution_version"] == "legacy" {
		return a.executeLegacyAllocationTask(ctx, scope, task)
	}
	pin := c.AllocationPin{}
	var err error
	if len(obj(task["pin"])) > 0 {
		raw, _ := json.Marshal(task["pin"])
		err = json.Unmarshal(raw, &pin)
	} else {
		pin, err = a.allocationPin(ctx)
	}
	if err != nil {
		return err
	}
	tx, err := a.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	scope, err = a.lockAllocationLease(ctx, tx, scope, task)
	if err != nil {
		return err
	}
	if err = a.authorizeAllocationTask(ctx, tx, scope, task); err != nil {
		return err
	}
	snapshot, err := a.buildAllocationSnapshot(ctx, tx, scope, task, time.Now().UTC().Truncate(time.Microsecond), true)
	if err != nil {
		return err
	}
	if len(snapshot.Members) == 0 {
		_, err = tx.Exec(ctx, `UPDATE platform_allocation_tasks SET status='completed',progress='{"assigned":0,"waiting":0,"ineligible":true}',updated_at=now() WHERE id=$1`, task["id"])
		if err != nil {
			return err
		}
		if err = a.releaseAllocationLease(ctx, tx, scope, task); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	request := c.AllocationRequest{ProtocolVersion: allocationProtocol, TaskKind: "pool.candidate_allocation", ExecutionMode: "deterministic", TaskID: allocationRef("allocation", str(task["kernel_namespace"])+"-"+str(task["generation"])), IdempotencyKey: allocationRef("allocation-key", str(task["kernel_namespace"])+"-"+str(task["generation"])), Pin: pin, Snapshot: snapshot, SnapshotHash: allocationSnapshotHash(snapshot), Budget: c.AllocationBudget{MaxDurationSeconds: 30, MaxToolCalls: 16}}
	raw, _ := json.Marshal(request)
	if len(raw) > 2<<20 || c.Validate("allocation.request", raw) != nil {
		return taskError("allocation_snapshot_invalid", "分配快照无效或超出上限")
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_tasks SET snapshot=$2::jsonb,pin=$3::jsonb,progress=$4::jsonb WHERE id=$1`, task["id"], string(raw), string(canonicalJSON(pin, false)), string(canonicalJSON(Object{"selected": len(snapshot.Members), "stage": "computing"}, false))); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	result, err := a.callAllocation(ctx, request)
	if err != nil {
		if result.TaskID != "" {
			code, _ := failureInfo(err)
			_ = a.recordAllocationPlan(ctx, task, request, result, "invalid", code)
		}
		return err
	}
	return a.commitAllocation(ctx, scope, task, request, result)
}
func (a *App) lockAllocationLease(ctx context.Context, db DB, scope, task Object) (Object, error) {
	current, err := one(ctx, db, `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE id=$1 AND NOT paused AND lease_token=$2 AND lease_until>now() FOR UPDATE`, scope["id"], task["worker_token"])
	if err != nil {
		return nil, taskError("allocation_lease_lost", "分配租约失效")
	}
	var running bool
	err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_allocation_tasks WHERE id=$1 AND generation=$2 AND status='running' AND worker_token=$3)`, task["id"], task["generation"], task["worker_token"]).Scan(&running)
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, taskError("allocation_lease_lost", "任务已取消或由新代次接管")
	}
	return current, nil
}
func (a *App) releaseAllocationLease(ctx context.Context, db DB, scope, task Object) error {
	_, err := db.Exec(ctx, `UPDATE platform_allocation_scopes SET lease_token='',lease_until=NULL WHERE id=$1 AND lease_token=$2`, scope["id"], task["worker_token"])
	return err
}
func (a *App) finishAllocationFailure(ctx context.Context, scope, task Object, cause error) {
	code, _ := failureInfo(cause)
	var pgerr interface{ SQLState() string }
	if errors.As(cause, &pgerr) && (pgerr.SQLState() == "40001" || pgerr.SQLState() == "40P01") {
		code = "allocation_snapshot_stale"
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	scope, err = a.lockAllocationLease(ctx, tx, scope, task)
	if err != nil {
		return
	}
	state := "failed"
	generation := num(task["generation"])
	if code == "allocation_snapshot_stale" && generation < 3 {
		state = "pending"
		generation++
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_tasks SET status=$2,error_code=$3,generation=$4,worker_token='',lease_until=NULL,snapshot=NULL,updated_at=now() WHERE id=$1`, task["id"], state, code, generation); err != nil {
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_plans SET status=CASE WHEN $3='allocation_snapshot_stale' THEN 'stale' ELSE 'invalid' END,validation_code=$3 WHERE task_id=$1 AND generation=$2 AND status='proposed'`, task["id"], task["generation"], code); err != nil {
		return
	}
	workState := "failed"
	if state == "pending" {
		workState = "pending"
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items SET status=$2,reason_code=$3 WHERE task_id=$1 AND status='leased'`, task["id"], workState, code); err != nil {
		return
	}
	if err = a.releaseAllocationLease(ctx, tx, scope, task); err != nil {
		return
	}
	_ = tx.Commit(ctx)
}

// Multi-scope imports/configuration acquire all scopes in ID order before business rows.
func (a *App) lockAllAllocationScopes(ctx context.Context, db DB) error {
	_, err := rows(ctx, db, `SELECT row_to_json(s) FROM platform_allocation_scopes s ORDER BY id FOR UPDATE`)
	return err
}
