package platform

import (
	"context"
	"encoding/json"
	"resume-platform/internal/contract"
	pdftext "resume-platform/internal/pdftext"
	"strings"
)

// Historical facts require both preserved analysis evidence and a verified source version.
// This one-time migration may verify files; the allocation projection and Agent never do.
func (a *App) proveHistoricalQualification(ctx context.Context, db DB, m Object) bool {
	row, err := one(ctx, db, `SELECT jsonb_build_object('result',d.kernel_result,'frozen',i.kernel_snapshot,'text',t.payload,'file',r.resume_file) FROM core_agentdispatchdecision d JOIN core_processingrunscopeitem i ON i.run_id=$1 JOIN core_resume r ON r.id=$2 JOIN core_resumetextextraction t ON t.id=i.text_extraction_id WHERE d.id=$3 AND i.candidate_id=$4 LIMIT 1`, m["run_id"], m["resume_id"], m["decision_id"], m["candidate_id"])
	if err != nil {
		return false
	}
	result, frozen := clone(obj(row["result"])), obj(row["frozen"])
	delete(result, "deterministic")
	if contract.Validate("response", canonicalJSON(result, false)) != nil {
		return false
	}
	d := obj(frozen["preflight"])
	if d["standard_code"] != m["standard_code"] || d["pool_code"] != m["pool_code"] || num(obj(frozen["volunteer_ids"])[str(d["current_volunteer_ref"])]) != num(m["resume_id"]) {
		return false
	}
	var text pdftext.Text
	raw, _ := json.Marshal(row["text"])
	if json.Unmarshal(raw, &text) != nil || text.FileSHA256 != m["file_checksum"] {
		return false
	}
	request := brief(result, "protocol_version", "task_id", "idempotency_key", "workflow_revision", "pin")
	request["scope"] = Object{"tag_catalog": obj(frozen["snapshot"])["tag_catalog"]}
	if validateAnalysis(request, result, text, []string{str(m["standard_code"])}) != nil {
		return false
	}
	threshold := floatValue(obj(frozen["thresholds"])["dispatch"])
	matches := list(result["matches"])
	if threshold <= 0 || len(matches) != 1 || floatValue(obj(matches[0])["score"]) < threshold {
		return false
	}
	if obj(obj(m["assessment"])["requirement"])["content_hash"] != standardJob(obj(obj(frozen["snapshot"])["pool_policy"]), policyItem(obj(obj(frozen["snapshot"])["pool_policy"]), "standards", str(m["standard_code"])))["content_hash"] {
		return false
	}
	for _, value := range list(m["tags"]) {
		tag := obj(value)
		if tag["source"] == "manual" {
			var proven bool
			err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_pool_events WHERE member_id=$1 AND kind='tags_revised' AND actor_id IS NOT NULL AND payload->'tags' @> $2::jsonb)`, m["id"], string(canonicalJSON([]any{tag}, false))).Scan(&proven)
			if err != nil || !proven {
				return false
			}
		}
	}
	path, err := a.resumeFile(str(row["file"]))
	if err != nil {
		return false
	}
	checksum, _, err := fileDigest(ctx, path)
	return err == nil && checksum == m["file_checksum"]
}
func (a *App) backfillAllocationScope(ctx context.Context, db DB, scope Object) error {
	members, err := rows(ctx, db, `SELECT row_to_json(m) FROM platform_pool_memberships m WHERE pool_code=$1 AND assessment->'pool'->>'entity'=$2 AND status='pending_allocation' ORDER BY workflow_id,id FOR UPDATE`, scope["pool_code"], scope["entity"])
	if err != nil {
		return err
	}
	for _, m := range members {
		var exists bool
		if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_screening_qualifications WHERE member_id=$1)`, m["id"]).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if !a.proveHistoricalQualification(ctx, db, m) {
				if _, err = db.Exec(ctx, `UPDATE platform_pool_memberships SET status='needs_reanalysis',revision=revision+1,updated_at=now() WHERE id=$1`, m["id"]); err != nil {
					return err
				}
				if err = a.poolEvent(ctx, db, m, "qualification_migration_unproven", Object{"reason": "历史源文件或评估版本无法验证，需要重新筛选"}, nil); err != nil {
					return err
				}
				continue
			}
			if _, err = a.saveScreeningQualification(ctx, db, m, true); err != nil {
				return err
			}
		}
		if _, err = db.Exec(ctx, `INSERT INTO platform_allocation_work_items(member_id,qualification_id) SELECT $1,q.id FROM platform_screening_qualifications q WHERE q.member_id=$1 ORDER BY revision DESC LIMIT 1 ON CONFLICT(qualification_id) WHERE status IN ('pending','leased') DO NOTHING`, m["id"]); err != nil {
			return err
		}
	}
	return a.backfillAssignmentTargets(ctx, db, scope)
}
func (a *App) backfillAssignmentTargets(ctx context.Context, db DB, scope Object) error {
	// Only a saved allocation event plus matching reservation or decision can prove the historical demand.
	values, err := rows(ctx, db, `SELECT jsonb_build_object('attempt',to_jsonb(at),'member',to_jsonb(m),'event_job',ev.payload->>'job_id','reservation_job',cap.job_id,'decision_job',d.recommended_job_id) FROM core_assignmentattempt at JOIN platform_pool_memberships m ON m.decision_id=at.agent_decision_id JOIN core_agentdispatchdecision d ON d.id=m.decision_id LEFT JOIN core_processingrunjobcapacity cap ON cap.id=at.capacity_reservation_id LEFT JOIN LATERAL(SELECT payload FROM platform_pool_events WHERE member_id=m.id AND kind='allocated' AND payload->>'attempt_id'=at.id::text ORDER BY id DESC LIMIT 1) ev ON true WHERE m.pool_code=$1 AND m.assessment->'pool'->>'entity'=$2 AND NOT EXISTS(SELECT 1 FROM platform_assignment_targets WHERE attempt_id=at.id) ORDER BY at.id`, scope["pool_code"], scope["entity"])
	if err != nil {
		return err
	}
	for _, v := range values {
		at, m := obj(v["attempt"]), obj(v["member"])
		id := num(v["event_job"])
		proven := id > 0 && (id == num(v["reservation_job"]) || id == num(v["decision_job"]))
		if v["reservation_job"] != nil && id != num(v["reservation_job"]) {
			proven = false
		}
		var demand any
		kind := "unknown"
		name := ""
		if proven {
			j, e := a.get(ctx, db, "core_job", id)
			if e != nil {
				return e
			}
			demand = id
			kind = "demand"
			name = str(j["position_name"])
		}
		var dispatched any
		if contains([]string{"dispatched", "passed", "rejected"}, str(at["status"])) || at["dispatched_at"] != nil {
			dispatched = at["dispatched_at"]
			if dispatched == nil {
				dispatched = at["created_at"]
			}
		}
		if _, err = db.Exec(ctx, `INSERT INTO platform_assignment_targets(attempt_id,member_id,scope_id,demand_id,department_id,target_kind,demand_name,department_name,sequence,allocated_at,dispatched_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, at["id"], m["id"], scope["id"], demand, at["initial_department_id"], kind, name, at["initial_department_name_snapshot"], num(scope["next_sequence"]), at["created_at"], dispatched); err != nil {
			return err
		}
		scope["next_sequence"] = num(scope["next_sequence"]) + 1
	}
	_, err = db.Exec(ctx, `UPDATE platform_allocation_scopes SET next_sequence=GREATEST(next_sequence,$2) WHERE id=$1`, scope["id"], scope["next_sequence"])
	return err
}
func (a *App) recordLegacyTarget(ctx context.Context, db DB, member, attempt, job Object) error {
	scope, err := a.allocationScope(ctx, db, member, true)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO platform_assignment_targets(attempt_id,member_id,scope_id,demand_id,department_id,target_kind,demand_name,department_name,sequence) VALUES($1,$2,$3,$4,$5,'demand',$6,$7,$8) ON CONFLICT DO NOTHING`, attempt["id"], member["id"], scope["id"], job["id"], job["department_id"], job["position_name"], attempt["initial_department_name_snapshot"], scope["next_sequence"])
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE platform_allocation_scopes SET next_sequence=next_sequence+1,revision=revision+1 WHERE id=$1`, scope["id"])
	return err
}
func isAllocationPath(path string) bool { return strings.HasPrefix(path, "position-pools/allocation-") }
