package platform

import (
	"context"
	"time"
)

var applicationLabels = map[string]string{
	"pending": "未处理", "assessing": "AI 评估中", "blocked": "待处理异常",
	"pending_review": "历史复核", "pending_allocation": "入池待分配", "needs_reanalysis": "待重新评估",
	"pending_dispatch": "待下发", "department_review": "待部门反馈", "cancelled": "已取消分配",
	"ai_rejected": "AI 评估不通过", "department_rejected": "部门不通过", "review_rejected": "人工复核不通过", "passed": "部门通过",
}

func closedApplication(status string) bool {
	return contains([]string{"ai_rejected", "department_rejected", "review_rejected", "passed"}, status)
}

func (a *App) migrateApplications(ctx context.Context, db DB) error {
	_, err := db.Exec(ctx, `
CREATE TABLE IF NOT EXISTS platform_applications(
 resume_id bigint PRIMARY KEY REFERENCES core_resume(id) ON DELETE CASCADE,
 candidate_id bigint NOT NULL REFERENCES core_candidate(id) ON DELETE CASCADE,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','assessing','blocked','pending_review','pending_allocation','needs_reanalysis','pending_dispatch','department_review','cancelled','ai_rejected','department_rejected','review_rejected','passed')),
 reason text NOT NULL DEFAULT '',updated_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz);
CREATE INDEX IF NOT EXISTS platform_applications_candidate ON platform_applications(candidate_id,status,resume_id);
CREATE TABLE IF NOT EXISTS platform_application_events(
 id bigserial PRIMARY KEY,resume_id bigint NOT NULL REFERENCES platform_applications(resume_id) ON DELETE CASCADE,
 from_status text NOT NULL,to_status text NOT NULL,reason text NOT NULL,payload jsonb NOT NULL DEFAULT '{}',created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS platform_application_events_resume ON platform_application_events(resume_id,id);
WITH history AS (
 SELECT r.id,r.candidate_id,
 CASE WHEN at.status='passed' THEN 'passed' WHEN at.status='rejected' THEN 'department_rejected'
 WHEN at.status='dispatched' THEN 'department_review' WHEN at.status IN ('pending_review','pending_dispatch') THEN at.status
 WHEN m.status='rejected' THEN 'review_rejected' WHEN m.status IN ('pending_review','pending_allocation','needs_reanalysis') THEN m.status
 WHEN d.recommendation='archive' AND d.error_code='' THEN 'ai_rejected'
 WHEN d.error_code<>'' THEN 'blocked' ELSE 'pending' END status,
 COALESCE(NULLIF(at.feedback_note,''),NULLIF(at.feedback_reason_label_snapshot,''),d.reason,'') reason
 FROM core_resume r
 LEFT JOIN LATERAL (SELECT * FROM core_assignmentattempt WHERE resume_id=r.id AND status<>'cancelled' ORDER BY id DESC LIMIT 1) at ON true
 LEFT JOIN LATERAL (SELECT * FROM platform_pool_memberships WHERE resume_id=r.id ORDER BY id DESC LIMIT 1) m ON true
 LEFT JOIN LATERAL (SELECT * FROM core_agentdispatchdecision WHERE resume_id=r.id ORDER BY id DESC LIMIT 1) d ON true
 WHERE NOT EXISTS(SELECT 1 FROM platform_applications a WHERE a.resume_id=r.id)
), inserted AS (
 INSERT INTO platform_applications(resume_id,candidate_id,status,reason,completed_at)
 SELECT id,candidate_id,status,reason,CASE WHEN status IN ('ai_rejected','department_rejected','review_rejected','passed') THEN now() END FROM history ON CONFLICT(resume_id) DO NOTHING RETURNING *
)
INSERT INTO platform_application_events(resume_id,from_status,to_status,reason,payload)
 SELECT resume_id,'',status,reason,'{"source":"migration"}' FROM inserted;
UPDATE core_candidateworkflow w SET status='talent_pool',archive_detail='全部志愿均未通过，进入人才库等待后续筛选策略'
 WHERE status='archived' AND archive_reason='all_rejected';
`)
	return err
}

// Call while holding the candidate workflow lock. Terminal applications are immutable;
// a future talent-pool strategy must create a new evaluation round explicitly.
func (a *App) applicationState(ctx context.Context, db DB, resumeID any, status, reason string, payload Object) error {
	if _, err := db.Exec(ctx, `INSERT INTO platform_applications(resume_id,candidate_id) SELECT id,candidate_id FROM core_resume WHERE id=$1 ON CONFLICT DO NOTHING`, resumeID); err != nil {
		return err
	}
	prior, err := one(ctx, db, "SELECT row_to_json(a) FROM platform_applications a WHERE resume_id=$1 FOR UPDATE", resumeID)
	if err != nil {
		return err
	}
	if prior["status"] == status && prior["reason"] == reason {
		return nil
	}
	if closedApplication(str(prior["status"])) {
		return &apiError{409, "该志愿已结束，不能覆盖历史结果"}
	}
	var completed any
	if closedApplication(status) {
		completed = time.Now().UTC()
	}
	if _, err = db.Exec(ctx, "UPDATE platform_applications SET status=$2,reason=$3,completed_at=$4,updated_at=now() WHERE resume_id=$1", resumeID, status, reason, completed); err != nil {
		return err
	}
	if payload == nil {
		payload = Object{}
	}
	_, err = db.Exec(ctx, "INSERT INTO platform_application_events(resume_id,from_status,to_status,reason,payload) VALUES($1,$2,$3,$4,$5::jsonb)", resumeID, prior["status"], status, reason, string(canonicalJSON(payload, false)))
	return err
}

func (a *App) requireOpenApplication(ctx context.Context, db DB, resumeID any) error {
	var closed bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_applications WHERE resume_id=$1 AND status IN ('ai_rejected','department_rejected','review_rejected','passed'))`, resumeID).Scan(&closed); err != nil {
		return err
	}
	if closed {
		return &apiError{409, "该志愿已结束，不能重新处理或分配；历史结果已保留"}
	}
	return nil
}

func (a *App) talentPool(ctx context.Context, db DB, w Object) error {
	// An import may add a wish after this run froze its snapshot. Let the next
	// scheduled batch pick it up instead of declaring it rejected unseen.
	var remaining bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core_resume r LEFT JOIN platform_applications a ON a.resume_id=r.id WHERE r.candidate_id=$1 AND (a.status IS NULL OR a.status NOT IN ('ai_rejected','department_rejected','review_rejected','passed')))`, w["candidate_id"]).Scan(&remaining); err != nil {
		return err
	}
	if remaining {
		return a.waitForNextApplication(ctx, db, w)
	}
	if err := a.closePoolMemberships(ctx, db, w["id"], nil, "all_rejected"); err != nil {
		return err
	}
	_, err := a.save(ctx, db, "core_candidateworkflow", w["id"], Object{"status": "talent_pool", "archive_reason": "all_rejected", "archive_detail": "全部志愿均未通过，进入人才库等待后续筛选策略", "block_reason": "", "block_detail": "", "completed_at": time.Now().UTC()})
	return err
}

func (a *App) waitAfterDepartmentRejection(ctx context.Context, db DB, w Object) error {
	if err := a.closePoolMemberships(ctx, db, w["id"], nil, "department_rejected"); err != nil {
		return err
	}
	var remaining bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core_resume r LEFT JOIN platform_applications a ON a.resume_id=r.id WHERE r.candidate_id=$1 AND (a.status IS NULL OR a.status NOT IN ('ai_rejected','department_rejected','review_rejected','passed')))`, w["candidate_id"]).Scan(&remaining); err != nil {
		return err
	}
	if !remaining {
		return a.talentPool(ctx, db, w)
	}
	return a.waitForNextApplication(ctx, db, w)
}

func (a *App) waitForNextApplication(ctx context.Context, db DB, w Object) error {
	_, err := a.save(ctx, db, "core_candidateworkflow", w["id"], Object{"status": "waiting_next", "current_resume_id": nil, "current_rank": nil, "archive_reason": "", "archive_detail": "", "block_reason": "", "block_detail": "", "completed_at": nil})
	return err
}

// State-based batches continue only work owned by automation. Department feedback owns its phase even if a scheduled scope still contains them.
func (a *App) retainApplicationWork(ctx context.Context, db DB, run, w Object) (bool, string, string, error) {
	if contains([]string{"passed", "talent_pool"}, str(w["status"])) || (w["status"] == "archived" && !forceReprocess(run)) {
		return true, "terminal_workflow", "流程已结束，已保留现有结果", nil
	}
	var active bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core_assignmentattempt WHERE workflow_id=$1 AND status IN ('pending_review','pending_dispatch','dispatched','passed'))`, w["id"]).Scan(&active); err != nil {
		return false, "", "", err
	}
	if active {
		return true, "awaiting_department", "候选人已有分配或部门处理记录，保留当前流程", nil
	}
	member, err := one(ctx, db, `SELECT row_to_json(m) FROM platform_pool_memberships m WHERE workflow_id=$1 AND status='pending_allocation' ORDER BY id DESC LIMIT 1`, w["id"])
	if err != nil && !noPoolRecord(err) {
		return false, "", "", err
	}
	if member != nil && !forceReprocess(run) {
		code, message := "allocation_queued", "筛选资格已保留，等待独立分配"
		_, err = a.invalidateWorkflow(ctx, db, w)
		return true, code, message, err
	}
	return false, "", "", nil
}

// Persist the next assessment on the same candidate scope item. This transaction
// also commits the rejected decision; crash recovery resumes the next task ID.
func (a *App) continueAfterAIRejection(ctx context.Context, db DB, w, item, frozen Object) error {
	next := clone(frozen)
	snapshot := obj(next["snapshot"])
	prior := obj(next["preflight"])
	for _, v := range list(snapshot["volunteers"]) {
		if obj(v)["ref"] == prior["current_volunteer_ref"] {
			obj(v)["rejected"] = true
		}
	}
	revision := num(w["revision"]) + 1
	next["continuation_revision"] = revision
	obj(snapshot["workflow"])["revision"] = revision
	obj(snapshot["workflow"])["retry_volunteer_ref"] = ""
	next["preflight"] = prepareSnapshot(snapshot)
	d := obj(next["preflight"])
	if str(d["current_volunteer_ref"]) == "" {
		if err := a.talentPool(ctx, db, w); err != nil {
			return err
		}
		current, err := a.get(ctx, db, "core_candidateworkflow", w["id"])
		if err != nil {
			return err
		}
		if current["status"] == "waiting_next" {
			return a.itemOutcome(ctx, db, item, "success", "waiting_next", "本轮志愿已结束，新增志愿等待下一轮处理")
		}
		return a.itemOutcome(ctx, db, item, "success", "talent_pool", "全部志愿均未通过，已进入人才库")
	}
	configureAssessmentSnapshot(next)
	next["task_id"] = token(16)
	resumeID := obj(next["volunteer_ids"])[str(d["current_volunteer_ref"])]
	resume, err := a.get(ctx, db, "core_resume", resumeID)
	if err != nil {
		return err
	}
	if err = a.touchWorkflow(ctx, db, w, resume); err != nil {
		return err
	}
	_, err = a.save(ctx, db, "core_processingrunscopeitem", item["id"], Object{
		"kernel_snapshot": next, "kernel_result": Object{}, "prepared_resume_id": resumeID, "text_extraction_id": nil,
		"workflow_revision_at_prepare": revision,
		"status":                       "pending", "processing_node": "pending", "reason_code": "ai_next_volunteer", "result_message": "当前志愿 AI 评估不通过，继续评估下一志愿",
	})
	return err
}
