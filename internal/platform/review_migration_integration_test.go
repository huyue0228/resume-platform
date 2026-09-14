package platform

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestRemovedReviewMigratesFrozenAdmissionAndPreservesHistory(t *testing.T) {
	for _, tc := range []struct {
		name, member, application, group string
		score                            any
		remaining, unknown, stale        bool
	}{
		{name: "passing", score: .8, member: "pending_allocation", application: "pending_allocation", group: "pending_allocation"},
		{name: "rejected_with_next", score: .7, remaining: true, member: "closed", application: "ai_rejected", group: "raw"},
		{name: "exhausted", score: .7, member: "closed", application: "ai_rejected", group: "talent_pool"},
		{name: "unknown_threshold", score: .8, unknown: true, member: "needs_reanalysis", application: "needs_reanalysis", group: "pending_allocation"},
		{name: "unknown_score", member: "needs_reanalysis", application: "needs_reanalysis", group: "pending_allocation"},
		{name: "stale", score: .8, stale: true, member: "closed", application: "cancelled", group: "archived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
			// Qualify without a department match, then restore a legacy review row.
			f.resultHook = func(result Object) { obj(result["profile"])["tags"] = []any{} }
			run := f.submit(t)
			f.executeJob(t, run, ctx)
			m := poolMemberForTest(t, f)
			assessment := obj(m["assessment"])
			delete(assessment, "admission_threshold")
			assessment["score"] = tc.score
			if _, err := f.a.Pool.Exec(ctx, "UPDATE platform_pool_memberships SET status='pending_review',assessment=$2::jsonb WHERE id=$1", m["id"], string(canonicalJSON(assessment, false))); err != nil {
				t.Fatal(err)
			}
			if _, err := f.a.Pool.Exec(ctx, "UPDATE platform_applications SET status='pending_review' WHERE resume_id=$1", f.resume["id"]); err != nil {
				t.Fatal(err)
			}
			if _, err := f.a.save(ctx, f.a.Pool, "core_agentdispatchdecision", m["decision_id"], Object{"recommendation": "review", "reason": "原始人工复核判定"}); err != nil {
				t.Fatal(err)
			}
			before, err := f.a.get(ctx, f.a.Pool, "core_agentdispatchdecision", m["decision_id"])
			if err != nil {
				t.Fatal(err)
			}
			if err = f.a.setConfig(ctx, f.a.Pool, "ai_dispatch_threshold", .95); err != nil {
				t.Fatal(err)
			}
			if tc.unknown {
				if _, err = f.a.Pool.Exec(ctx, "UPDATE core_processingrunscopeitem SET kernel_snapshot=kernel_snapshot-'thresholds' WHERE run_id=$1", run["id"]); err != nil {
					t.Fatal(err)
				}
			}
			if tc.remaining {
				nextApplication(t, f)
			}
			if tc.stale {
				if _, err = f.a.save(ctx, f.a.Pool, "core_candidateworkflow", m["workflow_id"], Object{"status": "archived", "current_resume_id": nil}); err != nil {
					t.Fatal(err)
				}
			}
			for n := 0; n < 2; n++ {
				if err = f.a.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			updated := poolMemberForTest(t, f)
			if updated["status"] != tc.member || num(updated["revision"]) != num(m["revision"])+1 {
				t.Fatalf("migration not idempotent: %v", updated)
			}
			assertApplicationState(t, f, f.resume, tc.application)
			view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
			if err != nil || view["system_status"] != tc.group {
				t.Fatalf("wrong candidate group: %v %v", view["system_status"], err)
			}
			after, err := f.a.get(ctx, f.a.Pool, "core_agentdispatchdecision", m["decision_id"])
			if err != nil || string(canonicalJSON(before, false)) != string(canonicalJSON(after, false)) {
				t.Fatalf("original decision changed: %v", err)
			}
			var events, attempts, runs int
			if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM platform_pool_events WHERE member_id=$1 AND kind='review_stage_removed'", m["id"]).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_assignmentattempt WHERE workflow_id=$1", m["workflow_id"]).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_processingrunscopeitem WHERE candidate_id=$1", f.candidate["id"]).Scan(&runs); err != nil {
				t.Fatal(err)
			}
			if events != 1 || attempts != 0 || runs != 1 || f.analyses.Load() != 1 {
				t.Fatalf("migration repeated work or dispatched: %d %d %d", events, attempts, runs)
			}
			if tc.group == "raw" || tc.group == "pending_allocation" {
				ids, err := f.a.resolveRunCandidates(httptest.NewRequest("POST", "/api/pipeline/run/", nil), f.p, Object{"system_statuses": []any{"pending_review"}})
				if err != nil || len(ids) != 1 || ids[0] != num(f.candidate["id"]) {
					t.Fatalf("legacy scheduled filter lost candidate: %v %v", ids, err)
				}
			}
		})
	}
}

func TestRemovedReviewCancelsLegacyAttemptAndReleasesCapacity(t *testing.T) {
	ctx := context.Background()
	f := newPipelineFixtureWithApp(t, isolatedInboxApp(t, false))
	f.executeJob(t, f.submit(t), ctx)
	m := poolMemberForTest(t, f)
	at, err := one(ctx, f.a.Pool, "SELECT row_to_json(at) FROM core_assignmentattempt at WHERE workflow_id=$1", m["workflow_id"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.save(ctx, f.a.Pool, "core_assignmentattempt", at["id"], Object{"status": "pending_review", "review_required": true}); err != nil {
		t.Fatal(err)
	}
	// The legacy attempt is not waiting on a pool review.
	if _, err = f.a.Pool.Exec(ctx, "UPDATE platform_pool_memberships SET status='closed' WHERE id=$1", m["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.Pool.Exec(ctx, "UPDATE platform_applications SET status='pending_review' WHERE resume_id=$1", f.resume["id"]); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err = f.a.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	at, err = f.a.get(ctx, f.a.Pool, "core_assignmentattempt", at["id"])
	if err != nil || at["status"] != "cancelled" || at["capacity_released_at"] == nil || truth(at["review_required"]) {
		t.Fatalf("legacy review still active: %v %v", at, err)
	}
	assertApplicationState(t, f, f.resume, "blocked")
	var used, events int
	if err = f.a.Pool.QueryRow(ctx, "SELECT used_count FROM core_processingrunjobcapacity WHERE id=$1", at["capacity_reservation_id"]).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err = f.a.Pool.QueryRow(ctx, "SELECT count(*) FROM core_assignmenthandlingevent WHERE attempt_id=$1 AND event_type='cancelled'", at["id"]).Scan(&events); err != nil {
		t.Fatal(err)
	}
	view, err := f.a.serialize(ctx, "candidates", f.candidate, f.p, true)
	if err != nil || view["system_status"] != "raw" || used != 0 || events != 1 {
		t.Fatalf("review cancellation not idempotent: status=%v used=%d events=%d err=%v", view["system_status"], used, events, err)
	}
	responseObject(t, apiRequest(t, f.a, f.p, "POST", "/api/workflow-attempts/"+str(at["id"])+"/confirm-review/", Object{}), 410)
}
