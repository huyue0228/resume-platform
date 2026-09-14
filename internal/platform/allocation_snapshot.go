package platform

import (
	"context"
	"encoding/json"
	"math"
	c "resume-platform/internal/contract"
	"sort"
	"time"
)

func (a *App) allocationScope(ctx context.Context, db DB, member Object, lock bool) (Object, error) {
	q := `SELECT row_to_json(s) FROM platform_allocation_scopes s WHERE entity=$1 AND pool_code=$2`
	if lock {
		q += " FOR UPDATE"
	}
	return one(ctx, db, q, obj(obj(member["assessment"])["pool"])["entity"], member["pool_code"])
}
func (a *App) allocationRevision(ctx context.Context, db DB, scope Object) (int64, error) {
	var revision int64
	err := db.QueryRow(ctx, `SELECT s.revision+COALESCE((SELECT max(id) FROM platform_allocation_changes WHERE scope_id=s.id),0) FROM platform_allocation_scopes s WHERE s.id=$1`, scope["id"]).Scan(&revision)
	return revision, err
}
func (a *App) saveScreeningQualification(ctx context.Context, db DB, member Object, screened bool) (Object, error) {
	source, err := one(ctx, db, `SELECT row_to_json(s) FROM platform_resume_sources s WHERE resume_id=$1 FOR UPDATE`, member["resume_id"])
	if err != nil {
		return nil, err
	}
	if screened {
		source, err = one(ctx, db, `UPDATE platform_resume_sources s SET file_checksum=$2,verified=true WHERE resume_id=$1 RETURNING row_to_json(s)`, member["resume_id"], member["file_checksum"])
		if err != nil {
			return nil, err
		}
	}
	if !truth(source["verified"]) || source["file_checksum"] != member["file_checksum"] {
		return nil, taskError("qualification_stale", "源文件版本未验证，需要重新筛选")
	}
	tags, err := allocationTagFacts(member, num(member["revision"]))
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(tags)
	q, err := one(ctx, db, `INSERT INTO platform_screening_qualifications(member_id,revision,decision_id,source_revision,source_checksum,standard_hash,tags_hash,tags) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb) ON CONFLICT(member_id,revision) DO NOTHING RETURNING row_to_json(platform_screening_qualifications)`, member["id"], member["revision"], member["decision_id"], source["revision"], source["file_checksum"], obj(obj(member["assessment"])["requirement"])["content_hash"], allocationTagHash(tags), string(raw))
	if noPoolRecord(err) {
		q, err = one(ctx, db, `SELECT row_to_json(q) FROM platform_screening_qualifications q WHERE member_id=$1 AND revision=$2`, member["id"], member["revision"])
	}
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(ctx, `UPDATE platform_allocation_work_items SET status='cancelled',reason_code='qualification_replaced' WHERE member_id=$1 AND qualification_id<>$2 AND status IN ('pending','leased','waiting')`, member["id"], q["id"]); err != nil {
		return nil, err
	}
	_, err = db.Exec(ctx, `INSERT INTO platform_allocation_work_items(member_id,qualification_id) VALUES($1,$2) ON CONFLICT(qualification_id) WHERE status IN ('pending','leased') DO NOTHING`, member["id"], q["id"])
	return q, err
}
func (a *App) allocationMemberRows(ctx context.Context, db DB, ids []int64) ([]Object, error) {
	return rows(ctx, db, `SELECT jsonb_build_object('member',to_jsonb(m),'workflow',to_jsonb(w),'resume',to_jsonb(r),'candidate',to_jsonb(c),'source',to_jsonb(s),'qualification',(SELECT to_jsonb(q) FROM platform_screening_qualifications q WHERE q.member_id=m.id ORDER BY q.revision DESC LIMIT 1),'already_assigned',EXISTS(SELECT 1 FROM core_assignmentattempt at WHERE at.workflow_id=w.id AND at.status IN ('pending_review','pending_dispatch','dispatched','passed'))) FROM platform_pool_memberships m JOIN core_candidateworkflow w ON w.id=m.workflow_id JOIN core_resume r ON r.id=m.resume_id JOIN core_candidate c ON c.id=m.candidate_id JOIN platform_resume_sources s ON s.resume_id=r.id WHERE m.id=ANY($1) ORDER BY m.workflow_id,m.id FOR SHARE OF c,w,m,r,s`, ids)
}
func (a *App) allocationQualificationValid(policy Object, row Object) bool {
	m, w, r, source, q := obj(row["member"]), obj(row["workflow"]), obj(row["resume"]), obj(row["source"]), obj(row["qualification"])
	standard := policyItem(policy, "standards", str(m["standard_code"]))
	pool := policyItem(policy, "pools", str(m["pool_code"]))
	current := resolveApplicationStandard(Object{"pool_policy": policy}, r, Object{})
	facts, err := allocationTagFacts(m, num(q["revision"]))
	if err != nil || allocationTagHash(facts) != q["tags_hash"] {
		return false
	}
	return len(q) > 0 && m["status"] == "pending_allocation" && num(w["current_resume_id"]) == num(m["resume_id"]) && !truth(row["already_assigned"]) && !contains([]string{"passed", "archived", "waiting_next", "talent_pool"}, str(w["status"])) && activePolicyItem(standard) && activePolicyItem(pool) && current["standard_code"] == m["standard_code"] && current["pool_code"] == m["pool_code"] && q["standard_hash"] == standardJob(policy, standard)["content_hash"] && num(q["source_revision"]) == num(source["revision"]) && q["source_checksum"] == source["file_checksum"] && truth(source["verified"]) && fingerprint(brief(obj(row["candidate"]), "highest_major", "highest_education")) == fingerprint(obj(obj(m["assessment"])["candidate"]))
}
func (a *App) buildAllocationSnapshot(ctx context.Context, db DB, scope, task Object, at time.Time, clean bool) (c.AllocationSnapshot, error) {
	var snapshot c.AllocationSnapshot
	config, err := one(ctx, db, `SELECT row_to_json(p) FROM platform_pool_policy p WHERE singleton FOR SHARE`)
	if err != nil {
		return snapshot, err
	}
	policy := obj(config["policy"])
	ids := []int64{}
	for _, id := range list(task["member_ids"]) {
		ids = append(ids, num(id))
	}
	memberRows, err := a.allocationMemberRows(ctx, db, ids)
	if err != nil {
		return snapshot, err
	}
	snapshot = c.AllocationSnapshot{SnapshotID: allocationRef("snapshot", str(task["id"])+"-"+str(task["generation"])), SnapshotAt: at.UTC().Format("2006-01-02T15:04:05.000000Z"), ScopeRef: allocationRef("scope", scope["id"]), EntityRef: allocationRef("entity", fingerprint(scope["entity"])), PoolRef: str(scope["pool_code"]), PolicyRevision: num(config["version"]), Epoch: num(scope["epoch"]), NextSequence: num(scope["next_sequence"]), WindowSeconds: 604800, Members: []c.AllocationMember{}, Demands: []c.AllocationDemand{}}
	jobs, err := rows(ctx, db, `SELECT to_jsonb(j)||jsonb_build_object('reception_state',ds.reception_state,'demand_revision',ds.revision) FROM core_job j JOIN core_department d ON d.id=j.department_id JOIN platform_demand_settings ds ON ds.demand_id=j.id WHERE lower(btrim(j.entity))=lower(btrim($1)) AND j.is_active AND d.level IN (1,2) ORDER BY j.id FOR SHARE OF j,d,ds`, scope["entity"])
	if err != nil {
		return snapshot, err
	}
	jobMap := map[int64]Object{}
	for _, j := range jobs {
		jobMap[num(j["id"])] = j
	}
	supplies, err := a.allocationSupply(ctx, db, scope["id"], at)
	if err != nil {
		return snapshot, err
	}
	supplyMap := map[int64]Object{}
	for _, s := range supplies {
		supplyMap[num(s["demand_id"])] = s
	}
	for _, v := range list(policy["rules"]) {
		rule := obj(v)
		j := jobMap[num(rule["job_id"])]
		if !activePolicyItem(rule) || rule["pool_code"] != scope["pool_code"] || j == nil {
			continue
		}
		s := supplyMap[num(j["id"])]
		snapshot.Demands = append(snapshot.Demands, c.AllocationDemand{DemandID: num(j["id"]), DepartmentRef: allocationRef("department", j["department_id"]), Revision: num(j["demand_revision"]), ReceptionState: str(j["reception_state"]), RequiredTags: stringValues(rule["required_tags"]), PreferredTags: stringValues(rule["preferred_tags"]), Priority: num(rule["priority"]), RecentSupplyCount: num(s["recent_supply_count"]), LastAllocationSequence: num(s["last_allocation_sequence"])})
	}
	if len(snapshot.Demands) > 200 {
		return snapshot, taskError("allocation_scope_too_large", "职位池有效需求超过 200 条，请调整配置")
	}
	countedRows, err := rows(ctx, db, `SELECT DISTINCT jsonb_build_object('candidate_id',w.candidate_id,'demand_id',t.demand_id) FROM platform_assignment_targets t JOIN core_assignmentattempt a ON a.id=t.attempt_id JOIN core_candidateworkflow w ON w.id=a.workflow_id WHERE t.scope_id=$1 AND t.target_kind='demand' AND t.allocated_at>=$2::timestamptz-interval '7 days' AND t.allocated_at<$2 AND (a.status IN ('pending_review','pending_dispatch','dispatched','passed') OR t.dispatched_at IS NOT NULL) AND w.candidate_id IN (SELECT candidate_id FROM platform_pool_memberships WHERE id=ANY($3))`, scope["id"], at, ids)
	if err != nil {
		return snapshot, err
	}
	countedByCandidate := map[int64]map[int64]bool{}
	for _, v := range countedRows {
		candidate := num(v["candidate_id"])
		if countedByCandidate[candidate] == nil {
			countedByCandidate[candidate] = map[int64]bool{}
		}
		countedByCandidate[candidate][num(v["demand_id"])] = true
	}
	for _, row := range memberRows {
		m, q, w := obj(row["member"]), obj(row["qualification"]), obj(row["workflow"])
		if m["pool_code"] != scope["pool_code"] || obj(obj(m["assessment"])["pool"])["entity"] != scope["entity"] {
			return snapshot, bad("候选人不属于本次分配范围")
		}
		if !a.allocationQualificationValid(policy, row) {
			if clean {
				status, code := "needs_reanalysis", "qualification_stale"
				if m["status"] != "pending_allocation" || truth(row["already_assigned"]) || num(w["current_resume_id"]) != num(m["resume_id"]) {
					status, code = str(m["status"]), "candidate_already_assigned"
				}
				if status == "needs_reanalysis" {
					if _, err = db.Exec(ctx, `UPDATE platform_pool_memberships SET status='needs_reanalysis',revision=revision+1,updated_at=now() WHERE id=$1`, m["id"]); err != nil {
						return snapshot, err
					}
				}
				if _, err = db.Exec(ctx, `UPDATE platform_allocation_work_items SET status='cancelled',reason_code=$2 WHERE member_id=$1 AND status IN ('pending','leased','waiting')`, m["id"], code); err != nil {
					return snapshot, err
				}
			}
			continue
		}
		tags := []c.AllocationTag{}
		raw, _ := json.Marshal(q["tags"])
		if err = json.Unmarshal(raw, &tags); err != nil {
			return snapshot, err
		}
		allowed := []int64{}
		counted := []int64{}
		for _, d := range snapshot.Demands {
			allowed = append(allowed, d.DemandID)
			if countedByCandidate[num(m["candidate_id"])][d.DemandID] {
				counted = append(counted, d.DemandID)
			}
		}
		created, err := time.Parse(time.RFC3339Nano, str(m["created_at"]))
		if err != nil {
			return snapshot, err
		}
		snapshot.Members = append(snapshot.Members, c.AllocationMember{MemberID: num(m["id"]), CandidateRef: allocationRef("candidate", m["candidate_id"]), ApplicationRef: allocationRef("application", m["resume_id"]), QualificationRef: allocationRef("qualification", q["id"]), QualificationRevision: num(q["revision"]), MemberRevision: num(m["revision"]), WorkflowRevision: num(w["revision"]), SourceRevision: num(q["source_revision"]), StandardRef: str(m["standard_code"]), StandardHash: str(q["standard_hash"]), TagsHash: str(q["tags_hash"]), Admitted: true, CreatedAt: created.UTC().Format("2006-01-02T15:04:05.000000Z"), Tags: tags, AllowedDemandIDs: allowed, CountedDemandIDs: counted})
	}
	snapshot.ScopeRevision, err = a.allocationRevision(ctx, db, scope)
	return snapshot, err
}
func (a *App) allocationSupply(ctx context.Context, db DB, scopeID any, at time.Time) ([]Object, error) {
	return rows(ctx, db, `SELECT jsonb_build_object('demand_id',t.demand_id,'recent_supply_count',count(DISTINCT w.candidate_id) FILTER(WHERE t.allocated_at>=$2::timestamptz-interval '7 days' AND t.allocated_at<$2),'last_allocation_sequence',COALESCE(max(t.sequence),0),'last_allocated_at',max(t.allocated_at),'pending_dispatch',count(*) FILTER(WHERE a.status='pending_dispatch'),'awaiting_feedback',count(*) FILTER(WHERE a.status='dispatched')) FROM platform_assignment_targets t JOIN core_assignmentattempt a ON a.id=t.attempt_id JOIN core_candidateworkflow w ON w.id=a.workflow_id WHERE t.scope_id=$1 AND t.target_kind='demand' AND (a.status IN ('pending_review','pending_dispatch','dispatched','passed') OR t.dispatched_at IS NOT NULL) GROUP BY t.demand_id`, scopeID, at)
}

func allocationTagFacts(member Object, revision int64) ([]c.AllocationTag, error) {
	tags := []c.AllocationTag{}
	for _, value := range list(member["tags"]) {
		t := obj(value)
		src := "model"
		if t["source"] == "manual" {
			if str(t["note"]) == "" {
				return nil, bad("人工标签缺少审计依据")
			}
			src = "manual"
		}
		tags = append(tags, c.AllocationTag{Code: str(t["code"]), Status: str(t["status"]), ConfidenceBPS: int64(math.Floor(floatValue(t["confidence"]) * 10000)), Source: src, AssertionRef: allocationRef("assertion", str(member["id"])+"-"+str(revision)+"-"+str(t["code"])), Verified: true})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Code < tags[j].Code })

	return tags, nil
}
