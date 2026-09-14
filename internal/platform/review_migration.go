package platform

import "context"

// Legacy review rows keep their original decisions and events. Only current
// work is moved forward; unknown admission evidence requires a fresh assessment.
func (a *App) migrateRemovedReview(ctx context.Context, db DB) error {
	candidates, err := rows(ctx, db, `SELECT json_build_object('candidate_id',candidate_id) FROM core_candidateworkflow w WHERE EXISTS(SELECT 1 FROM platform_pool_memberships m WHERE m.workflow_id=w.id AND m.status='pending_review') OR EXISTS(SELECT 1 FROM core_assignmentattempt at WHERE at.workflow_id=w.id AND at.status='pending_review') ORDER BY candidate_id`)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		w, err := a.lockWorkflow(ctx, db, candidate["candidate_id"])
		if err != nil {
			return err
		}
		attempts, err := rows(ctx, db, "SELECT row_to_json(at) FROM core_assignmentattempt at WHERE workflow_id=$1 AND status='pending_review' ORDER BY id FOR UPDATE", w["id"])
		if err != nil {
			return err
		}
		recheck := false
		for _, at := range attempts {
			if err = a.releaseCapacity(ctx, db, at); err != nil {
				return err
			}
			if _, err = a.save(ctx, db, "core_assignmentattempt", at["id"], Object{"status": "cancelled", "review_required": false, "cancel_reason": "review_stage_removed", "cancelled_at": now()}); err != nil {
				return err
			}
			if err = a.event(ctx, db, at, "cancelled", at["current_department_id"], nil, "待复核阶段已取消，回到待处理重新判定", nil, nil, Object{"reason": "review_stage_removed"}); err != nil {
				return err
			}
			recheck = recheck || num(at["resume_id"]) == num(w["current_resume_id"])
		}
		members, err := rows(ctx, db, `SELECT to_jsonb(m)||jsonb_build_object('frozen_threshold',i.kernel_snapshot->'thresholds'->'dispatch','decision_error',d.error_code) FROM platform_pool_memberships m JOIN core_agentdispatchdecision d ON d.id=m.decision_id LEFT JOIN LATERAL(SELECT kernel_snapshot FROM core_processingrunscopeitem WHERE run_id=m.run_id AND candidate_id=m.candidate_id AND prepared_resume_id=m.resume_id ORDER BY id DESC LIMIT 1)i ON true WHERE m.workflow_id=$1 AND m.status='pending_review' ORDER BY m.id FOR UPDATE OF m`, w["id"])
		if err != nil {
			return err
		}
		for _, m := range members {
			current := num(m["resume_id"]) == num(w["current_resume_id"]) && !contains([]string{"passed", "talent_pool", "archived"}, str(w["status"]))
			status, reason := "closed", "历史志愿已失效，关闭待复核记录"
			if current {
				status, reason = removedReviewOutcome(m)
				recheck = false
			}
			if _, err = db.Exec(ctx, "UPDATE platform_pool_memberships SET status=$2,revision=revision+1,updated_at=now() WHERE id=$1", m["id"], status); err != nil {
				return err
			}
			if err = a.poolEvent(ctx, db, m, "review_stage_removed", Object{"previous_status": "pending_review", "status": status, "reason": reason, "score": obj(m["assessment"])["score"], "threshold": removedReviewThreshold(m)}, nil); err != nil {
				return err
			}
			if !current {
				var pending bool
				if err = db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM platform_applications WHERE resume_id=$1 AND status='pending_review')", m["resume_id"]).Scan(&pending); err != nil {
					return err
				}
				if pending {
					if err = a.applicationState(ctx, db, m["resume_id"], "cancelled", reason, Object{"reason_code": "review_stage_removed"}); err != nil {
						return err
					}
				}
				continue
			}
			state := status
			if status == "closed" {
				state = "ai_rejected"
			}
			if err = a.applicationState(ctx, db, m["resume_id"], state, reason, Object{"pool_membership_id": m["id"], "reason_code": "review_stage_removed"}); err != nil {
				return err
			}
			if status == "closed" {
				if err = a.talentPool(ctx, db, w); err != nil {
					return err
				}
			}
		}
		if recheck && !contains([]string{"passed", "talent_pool", "archived"}, str(w["status"])) {
			if err = a.applicationState(ctx, db, w["current_resume_id"], "blocked", "旧待复核分配需按当前投递标准重新判定", Object{"reason_code": "review_stage_removed"}); err != nil {
				return err
			}
			if err = a.waitForNextApplication(ctx, db, w); err != nil {
				return err
			}
		}
		if _, err = a.invalidateWorkflow(ctx, db, w); err != nil {
			return err
		}
	}
	return nil
}

func removedReviewThreshold(m Object) any {
	if threshold := obj(m["assessment"])["admission_threshold"]; threshold != nil {
		return threshold
	}
	return m["frozen_threshold"]
}

func removedReviewOutcome(m Object) (string, string) {
	score, validScore := obj(m["assessment"])["score"].(float64)
	threshold, validThreshold := removedReviewThreshold(m).(float64)
	if !validScore || !validThreshold || score < 0 || score > 1 || threshold < 0 || threshold > 1 || str(m["decision_error"]) != "" {
		return "needs_reanalysis", "待复核阶段已取消，历史判定信息不足，需要重新评估"
	}
	if score >= threshold {
		return "pending_allocation", "待复核阶段已取消，评估达标，直接入池等待分配"
	}
	return "closed", "待复核阶段已取消，评估未达到入池阈值，结束当前志愿"
}
