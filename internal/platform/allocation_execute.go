package platform

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	c "resume-platform/internal/contract"
	"strings"
	"time"
)

func (a *App) recordAllocationPlan(ctx context.Context, task Object, request c.AllocationRequest, result c.AllocationResponse, status, code string) error {
	_, err := a.Pool.Exec(ctx, `INSERT INTO platform_allocation_plans(task_id,generation,snapshot_hash,result,status,validation_code) VALUES($1,$2,$3,$4::jsonb,$5,$6) ON CONFLICT(task_id,generation) DO NOTHING`, task["id"], task["generation"], request.SnapshotHash, string(canonicalJSON(result, false)), status, code)
	return err
}
func (a *App) commitAllocation(ctx context.Context, scope, task Object, request c.AllocationRequest, result c.AllocationResponse) error {
	if err := validateAllocationResult(request, result); err != nil {
		return err
	}
	if err := a.recordAllocationPlan(ctx, task, request, result, "proposed", ""); err != nil {
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
	at, err := time.Parse(time.RFC3339, request.Snapshot.SnapshotAt)
	if err != nil || time.Since(at) > 60*time.Second {
		return taskError("allocation_snapshot_stale", "分配快照已过期")
	}
	current, err := a.buildAllocationSnapshot(ctx, tx, scope, task, at, false)
	if err != nil {
		return err
	}
	if allocationSnapshotHash(current) != request.SnapshotHash {
		return taskError("allocation_snapshot_stale", "分配期间资格、需求或供给已变化")
	}
	plan, err := one(ctx, tx, `SELECT row_to_json(p) FROM platform_allocation_plans p WHERE task_id=$1 AND generation=$2 FOR UPDATE`, task["id"], task["generation"])
	if err != nil {
		return err
	}
	assigned, waiting := 0, 0
	var maxSequence int64
	for _, decision := range result.Decisions {
		if decision.Action == "assign" {
			assigned++
		} else {
			waiting++
		}
		if task["mode"] == "simulate" {
			continue
		}
		if decision.Action == "wait" {
			if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items SET status='waiting',reason_code=$3 WHERE member_id=$1 AND task_id=$2 AND status='leased'`, decision.MemberID, task["id"], decision.ReasonCode); err != nil {
				return err
			}
			continue
		}
		member, err := one(ctx, tx, `SELECT row_to_json(m) FROM platform_pool_memberships m WHERE id=$1 FOR UPDATE`, decision.MemberID)
		if err != nil {
			return err
		}
		workflow, err := a.lockWorkflow(ctx, tx, member["candidate_id"])
		if err != nil {
			return err
		}
		resume, err := a.get(ctx, tx, "core_resume", member["resume_id"])
		if err != nil {
			return err
		}
		job, err := a.get(ctx, tx, "core_job", decision.DemandID)
		if err != nil {
			return err
		}
		department, err := a.get(ctx, tx, "core_department", job["department_id"])
		if err != nil {
			return err
		}
		reason := "归属内部用人需求；命中标签：" + strings.Join(decision.MatchedTags, "、")
		attempt, err := a.createAttempt(ctx, tx, workflow, resume, department, Object{"source": "ai", "match_mode": "ai", "agent_decision_id": member["decision_id"], "confidence_score": obj(member["assessment"])["score"], "match_reason": reason, "review_required": false}, nil)
		if err != nil {
			return err
		}
		qualificationID := strings.TrimPrefix(decision.QualificationRef, "qualification-")
		if _, err = tx.Exec(ctx, `INSERT INTO platform_assignment_targets(attempt_id,member_id,qualification_id,plan_id,scope_id,demand_id,department_id,target_kind,demand_name,department_name,sequence,allocated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'demand',$8,$9,$10,clock_timestamp())`, attempt["id"], member["id"], qualificationID, plan["id"], scope["id"], job["id"], department["id"], job["position_name"], department["name"], decision.Sequence); err != nil {
			return err
		}
		if _, err = a.save(ctx, tx, "core_resume", resume["id"], Object{"job_id": job["id"], "job_category": job["category"], "category_mode": "ai", "category_reason": reason}); err != nil {
			return err
		}
		if _, err = a.save(ctx, tx, "core_agentdispatchdecision", member["decision_id"], Object{"recommendation": "dispatch", "recommended_job_id": job["id"], "recommended_department_id": department["id"]}); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE platform_pool_memberships SET status='allocated',revision=revision+1,updated_at=now() WHERE id=$1`, member["id"]); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items SET status='assigned',reason_code='allocated' WHERE member_id=$1 AND status IN ('pending','leased','waiting')`, member["id"]); err != nil {
			return err
		}
		if err = a.poolEvent(ctx, tx, member, "allocated", Object{"job_id": job["id"], "attempt_id": attempt["id"], "plan_id": plan["id"], "matched_tags": decision.MatchedTags, "order": decision.Order, "reason": reason, "allocation_version": "v1"}, nil); err != nil {
			return err
		}
		maxSequence = max(maxSequence, decision.Sequence)
	}
	planStatus := "committed"
	if task["mode"] == "simulate" {
		planStatus = "simulated"
		if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items SET status='pending',task_id=NULL WHERE task_id=$1 AND status='leased'`, task["id"]); err != nil {
			return err
		}
	}

	if maxSequence > 0 {
		if _, err = tx.Exec(ctx, `UPDATE platform_allocation_scopes SET next_sequence=GREATEST(next_sequence,$2),revision=revision+1 WHERE id=$1`, scope["id"], maxSequence+1); err != nil {
			return err
		}
	}
	revision, err := a.allocationRevision(ctx, tx, scope)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_work_items SET observed_revision=$2 WHERE task_id=$1 AND status='waiting'`, task["id"], revision); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_plans SET status=$2 WHERE id=$1`, plan["id"], planStatus); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE platform_allocation_tasks SET status='completed',progress=$2::jsonb,updated_at=now() WHERE id=$1`, task["id"], string(canonicalJSON(Object{"selected": len(result.Decisions), "assigned": assigned, "waiting": waiting, "stage": planStatus, "simulated": task["mode"] == "simulate"}, false))); err != nil {
		return err
	}
	if err = a.releaseAllocationLease(ctx, tx, scope, task); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func allocationPlanObject(result c.AllocationResponse) Object {
	raw, _ := json.Marshal(result)
	var value Object
	_ = json.Unmarshal(raw, &value)
	return value
}
