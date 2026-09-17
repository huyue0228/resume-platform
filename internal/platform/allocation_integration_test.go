package platform

import (
	"context"
	"encoding/json"
	"os"
	c "resume-platform/internal/contract"
	"strings"
	"testing"
	"time"
)

func newAllocationFixture(t *testing.T) *pipelineFixture {
	t.Helper()
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	f.independentAllocation = true
	ctx := context.Background()
	if err := f.a.syncAllocationConfig(ctx, f.a.Pool); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Pool.Exec(ctx, `UPDATE platform_allocation_scopes SET allocation_mode='execute_v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.save(ctx, f.a.Pool, "core_job", f.job["id"], Object{"headcount": 0, "is_public": false}); err != nil {
		t.Fatal(err)
	}
	f.executeJob(t, f.submit(t), ctx)
	m := poolMemberForTest(t, f)
	if m["status"] != "pending_allocation" {
		t.Fatalf("screening did not enter independent queue: %v", m["status"])
	}
	return f
}
func allocationLocalKernel(t *testing.T, f *pipelineFixture) {
	t.Helper()
	url := os.Getenv("TEST_ALLOCATION_KERNEL_URL")
	if url == "" {
		t.Skip("TEST_ALLOCATION_KERNEL_URL must point to an independently built Kernel")
	}
	f.a.Config.KernelURL = url
}
func runAllocation(t *testing.T, f *pipelineFixture) (Object, Object, error) {
	t.Helper()
	scope, task, err := f.a.claimAllocation(context.Background())
	if err != nil {
		t.Fatal("claim", err)
	}
	err = f.a.processAllocation(context.Background(), scope, task)
	if err != nil {
		f.a.finishAllocationFailure(context.Background(), scope, task, err)
	}
	return scope, task, err
}
func TestAllocationRealKernelNonpublicHCZero(t *testing.T) {
	f := newAllocationFixture(t)
	allocationLocalKernel(t, f)
	ctx := context.Background()
	scope, task, err := runAllocation(t, f)
	if err != nil {
		t.Fatal("allocation", err)
	}
	m := poolMemberForTest(t, f)
	if m["status"] != "allocated" || f.analyses.Load() != 1 {
		t.Fatal("allocation repeated screening or failed")
	}
	var targets int
	if err = f.a.Pool.QueryRow(ctx, `SELECT count(*) FROM platform_assignment_targets WHERE scope_id=$1 AND demand_id=$2`, scope["id"], f.job["id"]).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != 1 {
		t.Fatalf("HC-zero demand did not receive candidate: %d", targets)
	}
	detail := responseObject(t, apiRequest(t, f.a, f.p, "GET", "/api/position-pools/allocation-tasks/"+str(task["id"])+"/", nil), 200)
	if detail["status"] != "completed" || num(obj(detail["progress"])["assigned"]) != 1 {
		t.Fatal("task progress incorrect", detail)
	}
	raw := string(canonicalJSON(detail, false))
	for _, forbidden := range []string{"resume_text", "source_text", "quote", "highest_education"} {
		if strings.Contains(raw, forbidden) {
			t.Fatal("private screening fields leaked", forbidden)
		}
	}
	if err = f.a.processAllocation(ctx, scope, task); err == nil {
		t.Fatal("completed worker replay should lose lease")
	}
	if err = f.a.Pool.QueryRow(ctx, `SELECT count(*) FROM platform_assignment_targets WHERE scope_id=$1`, scope["id"]).Scan(&targets); err != nil || targets != 1 {
		t.Fatal("duplicate replay")
	}
	supplies, err := f.a.allocationSupply(ctx, f.a.Pool, scope["id"], time.Now().UTC().Add(time.Second))
	if err != nil || num(supplies[0]["recent_supply_count"]) != 1 {
		t.Fatal("pending dispatch not counted", err)
	}
}
func TestAllocationWaitAndFailurePreserveQualification(t *testing.T) {
	f := newAllocationFixture(t)
	allocationLocalKernel(t, f)
	ctx := context.Background()
	if _, err := f.a.Pool.Exec(ctx, `UPDATE platform_demand_settings SET reception_state='paused'`); err != nil {
		t.Fatal(err)
	}
	_, task, err := runAllocation(t, f)
	if err != nil {
		t.Fatal(err)
	}
	m := poolMemberForTest(t, f)
	if m["status"] != "pending_allocation" {
		t.Fatal("wait revoked qualification")
	}
	var reason string
	if err = f.a.Pool.QueryRow(ctx, `SELECT reason_code FROM platform_allocation_work_items WHERE task_id=$1`, task["id"]).Scan(&reason); err != nil || reason != "no_receiving_demand" {
		t.Fatal(reason, err)
	}
	if _, _, err = f.a.claimAllocation(ctx); !noPoolRecord(err) {
		t.Fatal("waiting loop should sleep", err)
	}
	if _, err = f.a.Pool.Exec(ctx, `UPDATE platform_demand_settings SET reception_state='receiving'`); err != nil {
		t.Fatal(err)
	}
	f.a.Config.KernelToken = ""
	scope, failed, err := runAllocation(t, f)
	if err == nil {
		t.Fatal("unavailable Kernel accepted")
	}
	row, e := one(ctx, f.a.Pool, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE id=$1`, failed["id"])
	if e != nil || row["status"] != "failed" {
		t.Fatal("failure not persisted", e)
	}
	if poolMemberForTest(t, f)["status"] != "pending_allocation" || f.analyses.Load() != 1 {
		t.Fatal("failure changed screening")
	}
	_ = scope
}
func TestAllocationStaleSnapshotAndCancellationWriteNothing(t *testing.T) {
	f := newAllocationFixture(t)
	ctx := context.Background()
	scope, task, err := f.a.claimAllocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.a.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.a.buildAllocationSnapshot(ctx, tx, scope, task, time.Now().UTC().Truncate(time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := c.Bundle.ReadFile("bundle/allocation.request.example.json")
	var req c.AllocationRequest
	_ = json.Unmarshal(raw, &req)
	req.Snapshot = snapshot
	req.SnapshotHash = allocationSnapshotHash(snapshot)
	res := c.AllocationResponse{ProtocolVersion: allocationProtocol, ResultSchemaVersion: allocationResult, TaskID: req.TaskID, IdempotencyKey: req.IdempotencyKey, Pin: req.Pin, SnapshotHash: req.SnapshotHash, TerminalState: "DONE", Decisions: expectedAllocation(snapshot), SafeTrace: c.AllocationTrace{ToolNames: []string{}}}
	if _, err = f.a.Pool.Exec(ctx, `UPDATE platform_demand_settings SET reception_state='paused'`); err != nil {
		t.Fatal(err)
	}
	err = f.a.commitAllocation(ctx, scope, task, req, res)
	code, _ := failureInfo(err)
	if code != "allocation_snapshot_stale" {
		t.Fatal("stale accepted", err)
	}
	f.a.finishAllocationFailure(ctx, scope, task, err)
	var count int
	_ = f.a.Pool.QueryRow(ctx, `SELECT count(*) FROM core_assignmentattempt`).Scan(&count)
	if count != 0 {
		t.Fatal("stale plan partially committed")
	}
	taskRow, e := one(ctx, f.a.Pool, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE id=$1`, task["id"])
	if e != nil || num(taskRow["generation"]) != 2 || taskRow["status"] != "pending" {
		t.Fatal("stale generation not retried", taskRow, e)
	}
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/position-pools/allocation-tasks/"+str(task["id"])+"/cancel/", Object{}), 202)
	if err = f.a.commitAllocation(ctx, scope, task, req, res); err == nil {
		t.Fatal("cancelled plan committed")
	}
}

func TestAllocationAPIIdempotencyAndPermission(t *testing.T) {
	f := newAllocationFixture(t)
	ctx := context.Background()
	m := poolMemberForTest(t, f)
	scope, err := f.a.allocationScope(ctx, f.a.Pool, m, false)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/position-pools/allocation-tasks/"
	body := Object{"scope_id": scope["id"], "mode": "simulate", "member_ids": []any{m["id"]}, "idempotency_key": "api-fixture-unique"}
	oneTask := responseObject(t, apiRequest(t, f.a, f.p, "POST", path, body), 202)
	twoTask := responseObject(t, apiRequest(t, f.a, f.p, "POST", path, body), 202)
	if oneTask["task_id"] != twoTask["task_id"] {
		t.Fatal("duplicate idempotent task")
	}
	body["mode"] = "execute"
	responseObject(t, apiRequest(t, f.a, f.p, "POST", path, body), 409)
	user := mustSave(t, f.a, "accounts_user", Object{"username": "allocation-scope-user", "password": "!unusable", "is_active": true, "date_joined": now()})
	p, err := f.a.userPrincipal(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	responseObject(t, apiRequest(t, f.a, p, "GET", path+str(oneTask["task_id"])+"/", nil), 403)
	responseObject(t, apiRequest(t, f.a, p, "PATCH", "/api/jobs/"+str(f.job["id"])+"/reception/", Object{"expected_revision": 1, "reception_state": "paused", "reason": "test"}), 403)
	responseObject(t, apiRequest(t, f.a, f.p, "POST", path+str(oneTask["task_id"])+"/cancel/", Object{}), 202)
	if _, _, err = f.a.claimAllocation(ctx); !noPoolRecord(err) {
		t.Fatal("cancelled pending work was automatically executed", err)
	}
}
func TestAllocationLeaseRecoveryAndBoundedRetry(t *testing.T) {
	f := newAllocationFixture(t)
	ctx := context.Background()
	scope, old, err := f.a.claimAllocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.a.claimAllocation(ctx); !noPoolRecord(err) {
		t.Fatal("same scope leased twice", err)
	}
	if _, err = f.a.Pool.Exec(ctx, `UPDATE platform_allocation_scopes SET lease_until=now()-interval '1 second' WHERE id=$1`, scope["id"]); err != nil {
		t.Fatal(err)
	}
	_, fresh, err := f.a.claimAllocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if num(fresh["generation"]) != 2 || fresh["worker_token"] == old["worker_token"] {
		t.Fatal("lease was not fenced")
	}
	tx, err := f.a.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.a.lockAllocationLease(ctx, tx, scope, old)
	_ = tx.Rollback(ctx)
	if err == nil {
		t.Fatal("old worker retained write access")
	}
	if _, err = f.a.Pool.Exec(ctx, `UPDATE platform_allocation_tasks SET generation=3 WHERE id=$1`, fresh["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Pool.Exec(ctx, `UPDATE platform_allocation_scopes SET lease_until=now()-interval '1 second' WHERE id=$1`, scope["id"]); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.a.claimAllocation(ctx)
	if !noPoolRecord(err) {
		t.Fatal("retry cap ignored", err)
	}
	task, err := one(ctx, f.a.Pool, `SELECT row_to_json(t) FROM platform_allocation_tasks t WHERE id=$1`, fresh["id"])
	if err != nil || task["status"] != "failed" {
		t.Fatal("exhausted retry not durable", task, err)
	}
}
func TestAllocationSupplyCancellationAndRejection(t *testing.T) {
	f := newAllocationFixture(t)
	allocationLocalKernel(t, f)
	ctx := context.Background()
	scope, _, err := runAllocation(t, f)
	if err != nil {
		t.Fatal(err)
	}
	at, err := one(ctx, f.a.Pool, `SELECT row_to_json(a) FROM core_assignmentattempt a ORDER BY id DESC LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.save(ctx, f.a.Pool, "core_assignmentattempt", at["id"], Object{"status": "cancelled"}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.a.allocationSupply(ctx, f.a.Pool, scope["id"], time.Now().Add(time.Second))
	if err != nil || len(rows) != 0 {
		t.Fatal("pre-dispatch cancellation retained supply", err)
	}
	if _, err = f.a.save(ctx, f.a.Pool, "core_assignmentattempt", at["id"], Object{"status": "dispatched"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.save(ctx, f.a.Pool, "core_assignmentattempt", at["id"], Object{"status": "rejected"}); err != nil {
		t.Fatal(err)
	}
	rows, err = f.a.allocationSupply(ctx, f.a.Pool, scope["id"], time.Now().Add(time.Second))
	if err != nil || len(rows) != 1 || num(rows[0]["recent_supply_count"]) != 1 {
		t.Fatal("post-dispatch rejection erased historical recommendation", err)
	}
}

func TestAllocationPauseFencesRunningAndResumesWithoutScreening(t *testing.T) {
	f := newAllocationFixture(t)
	allocationLocalKernel(t, f)
	ctx := context.Background()
	scope, task, err := f.a.claimAllocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/position-pools/allocation-scopes/" + str(scope["id"]) + "/"
	paused := responseObject(t, apiRequest(t, f.a, f.p, "PATCH", path, Object{"paused": true, "expected_revision": scope["revision"], "reason": "暂停检查"}), 200)
	if !truth(paused["paused"]) {
		t.Fatal("pause not saved")
	}
	if err = f.a.processAllocation(ctx, scope, task); err == nil {
		t.Fatal("old worker executed after pause")
	}
	if _, _, err = f.a.claimAllocation(ctx); !noPoolRecord(err) {
		t.Fatal("paused scope leased", err)
	}
	if poolMemberForTest(t, f)["status"] != "pending_allocation" {
		t.Fatal("pause revoked qualification")
	}
	responseObject(t, apiRequest(t, f.a, f.p, "PATCH", path, Object{"paused": false, "expected_revision": paused["revision"], "reason": "恢复"}), 200)
	if _, _, err = runAllocation(t, f); err != nil {
		t.Fatal(err)
	}
	if poolMemberForTest(t, f)["status"] != "allocated" || f.analyses.Load() != 1 {
		t.Fatal("resume repeated screening")
	}
}

func TestAllocationManualSimulationAndFreshRetry(t *testing.T) {
	f := newAllocationFixture(t)
	allocationLocalKernel(t, f)
	ctx := context.Background()
	m := poolMemberForTest(t, f)
	scope, err := f.a.allocationScope(ctx, f.a.Pool, m, false)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/position-pools/allocation-tasks/"
	scopePath := "/api/position-pools/allocation-scopes/" + str(scope["id"]) + "/"
	paused := responseObject(t, apiRequest(t, f.a, f.p, "PATCH", scopePath, Object{"paused": true, "expected_revision": scope["revision"], "reason": "暂停执行后试算"}), 200)
	simulated := responseObject(t, apiRequest(t, f.a, f.p, "POST", path, Object{"scope_id": scope["id"], "mode": "simulate", "member_ids": []any{m["id"]}, "idempotency_key": "only-simulate"}), 202)
	if _, _, err = runAllocation(t, f); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err = f.a.Pool.QueryRow(ctx, `SELECT count(*) FROM core_assignmentattempt`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal("simulation wrote business assignment", err)
	}
	detail := responseObject(t, apiRequest(t, f.a, f.p, "GET", path+str(simulated["task_id"])+"/", nil), 200)
	if len(list(detail["screening_runs"])) != 1 {
		t.Fatal("missing screening provenance")
	}
	if _, _, err = f.a.claimAllocation(ctx); !noPoolRecord(err) {
		t.Fatal("simulation resumed paused automatic allocation", err)
	}
	responseObject(t, apiRequest(t, f.a, f.p, "PATCH", scopePath, Object{"paused": false, "expected_revision": paused["revision"], "reason": "试算后恢复"}), 200)
	f.a.Config.KernelToken = ""
	_, failed, err := runAllocation(t, f)
	if err == nil {
		t.Fatal("expected service failure")
	}
	f.a.Config.KernelToken = "test-kernel-token"
	responseObject(t, apiRequest(t, f.a, f.p, "POST", path+str(failed["id"])+"/retry/", Object{"idempotency_key": "fresh-after-failure"}), 202)
	if _, _, err = runAllocation(t, f); err != nil {
		t.Fatal(err)
	}
	if f.analyses.Load() != 1 || poolMemberForTest(t, f)["status"] != "allocated" {
		t.Fatal("retry lost screening result")
	}
}

func TestAllocationAcrossBatchesCountsLatestSupply(t *testing.T) {
	f := newAllocationFixture(t)
	ctx := context.Background()
	screeningURL := f.a.Config.KernelURL
	allocationLocalKernel(t, f)
	scope, _, err := runAllocation(t, f)
	if err != nil {
		t.Fatal(err)
	}
	f.a.Config.KernelURL = screeningURL
	candidate := mustSave(t, f.a, "core_candidate", Object{"name": "第二批候选人", "phone": "13800138123", "identity_hash": identity("second-batch", "13800138123"), "highest_education": "bachelor"})
	mustSave(t, f.a, "core_resume", Object{"candidate_id": candidate["id"], "apply_id": "second-batch", "resume_file": f.resume["resume_file"], "entity": "YLS", "position_name": f.job["public_name"]})
	f.executeJob(t, f.submit(t, candidate["id"]), ctx)
	allocationLocalKernel(t, f)
	scope, task, err := f.a.claimAllocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.a.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.a.buildAllocationSnapshot(ctx, tx, scope, task, time.Now().UTC().Truncate(time.Microsecond), false)
	_ = tx.Rollback(ctx)
	if err != nil || len(snapshot.Demands) != 1 || snapshot.Demands[0].RecentSupplyCount != 1 {
		t.Fatalf("cross-batch supply lost: %+v %v", snapshot.Demands, err)
	}
	if err = f.a.processAllocation(ctx, scope, task); err != nil {
		t.Fatal(err)
	}
	var total int
	if err = f.a.Pool.QueryRow(ctx, `SELECT count(*) FROM platform_assignment_targets WHERE scope_id=$1 AND demand_id=$2`, scope["id"], f.job["id"]).Scan(&total); err != nil || total != 2 {
		t.Fatal("HC zero did not receive multiple candidates", total, err)
	}
	if f.analyses.Load() != 2 {
		t.Fatal("allocation analyzed source resumes")
	}
}

func TestAllocationConcurrentClaimsAndDuplicateTasks(t *testing.T) {
	f := newAllocationFixture(t)
	allocationLocalKernel(t, f)
	ctx := context.Background()
	m := poolMemberForTest(t, f)
	scope, err := f.a.allocationScope(ctx, f.a.Pool, m, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"concurrent-first", "concurrent-duplicate"} {
		responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/position-pools/allocation-tasks/", Object{"scope_id": scope["id"], "mode": "execute", "member_ids": []any{m["id"]}, "idempotency_key": key}), 202)
	}
	type claim struct {
		s, t Object
		err  error
	}
	results := make(chan claim, 8)
	start := make(chan struct{})
	for range 8 {
		go func() { <-start; s, task, e := f.a.claimAllocation(ctx); results <- claim{s, task, e} }()
	}
	close(start)
	claimed := 0
	var owner claim
	for range 8 {
		v := <-results
		if v.err == nil {
			owner = v
			claimed++
		} else if !noPoolRecord(v.err) {
			t.Fatal(v.err)
		}
	}
	if claimed != 1 {
		t.Fatalf("one scope has %d workers", claimed)
	}
	if err = f.a.processAllocation(ctx, owner.s, owner.t); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runAllocation(t, f); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err = f.a.Pool.QueryRow(ctx, `SELECT count(*) FROM core_assignmentattempt`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("duplicate task created assignment", attempts, err)
	}
	if f.analyses.Load() != 1 {
		t.Fatal("duplicate task screened resume")
	}
}
