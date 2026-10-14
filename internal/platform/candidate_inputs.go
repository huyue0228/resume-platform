package platform

import "context"

// These source fields determine admission, volunteer ordering and model input.
// Derived school tags and display fields must not invalidate the worker itself.
func candidateAssessmentInputs(candidate Object) Object {
	inputs := Object{}
	for _, key := range []string{"highest_major", "highest_education", "household_province", "first_degree_school", "highest_degree_school"} {
		inputs[key] = str(candidate[key])
	}
	return inputs
}

// Call inside the edit/import transaction, after locking allocation scopes.
// Existing department assignments and terminal applications retain their history.
func (a *App) invalidateCandidateInputs(ctx context.Context, db DB, before, after Object, actor *Principal) error {
	if before["id"] == nil || fingerprint(candidateAssessmentInputs(before)) == fingerprint(candidateAssessmentInputs(after)) {
		return nil
	}
	w, err := one(ctx, db, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1 FOR UPDATE", after["id"])
	if noPoolRecord(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = a.invalidateWorkflow(ctx, db, w); err != nil {
		return err
	}
	const reason = "候选人准入或评估资料已变化，请重新处理"
	applications, err := rows(ctx, db, "SELECT row_to_json(a) FROM platform_applications a WHERE candidate_id=$1 AND status='assessing' ORDER BY resume_id", after["id"])
	if err != nil {
		return err
	}
	for _, application := range applications {
		if err = a.applicationState(ctx, db, application["resume_id"], "blocked", reason, Object{"source": "candidate_changed"}); err != nil {
			return err
		}
	}
	members, err := rows(ctx, db, "UPDATE platform_pool_memberships SET status='needs_reanalysis',revision=revision+1,updated_at=now() WHERE candidate_id=$1 AND status='pending_allocation' RETURNING row_to_json(platform_pool_memberships)", after["id"])
	if err != nil {
		return err
	}
	for _, member := range members {
		if _, err = db.Exec(ctx, "UPDATE platform_allocation_work_items SET status='cancelled',reason_code='qualification_stale' WHERE member_id=$1 AND status IN ('pending','leased','waiting')", member["id"]); err != nil {
			return err
		}
		if err = a.applicationState(ctx, db, member["resume_id"], "needs_reanalysis", reason, Object{"member_id": member["id"]}); err != nil {
			return err
		}
		if err = a.poolEvent(ctx, db, member, "candidate_changed", Object{"message": reason}, actor); err != nil {
			return err
		}
	}
	return nil
}
