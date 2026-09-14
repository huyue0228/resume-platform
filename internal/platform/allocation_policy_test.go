package platform

import (
	"encoding/json"
	c "resume-platform/internal/contract"
	"testing"
)

func TestAllocationIndependentDesignCases(t *testing.T) {
	raw, _ := c.Bundle.ReadFile("bundle/allocation.cases.json")
	var cases []struct {
		ID       string
		Request  c.AllocationRequest
		Expected []struct {
			MemberID int64 `json:"member_id"`
			DemandID int64 `json:"demand_id"`
			Wait     string
		}
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			got := expectedAllocation(tc.Request.Snapshot)
			if len(got) != len(tc.Expected) {
				t.Fatal("coverage")
			}
			for i, want := range tc.Expected {
				if got[i].MemberID != want.MemberID || got[i].DemandID != want.DemandID || (want.Wait != "" && want.Wait != got[i].ReasonCode) {
					t.Fatalf("unexpected decision %+v", got[i])
				}
			}
			if allocationSnapshotHash(tc.Request.Snapshot) != tc.Request.SnapshotHash {
				t.Fatal("cross-language hash mismatch")
			}
		})
	}
}
func TestAllocationValidationRejectsPartialAndChangedPlans(t *testing.T) {
	raw, _ := c.Bundle.ReadFile("bundle/allocation.request.example.json")
	var req c.AllocationRequest
	_ = json.Unmarshal(raw, &req)
	raw, _ = c.Bundle.ReadFile("bundle/allocation.response.example.json")
	var res c.AllocationResponse
	_ = json.Unmarshal(raw, &res)
	if err := validateAllocationResult(req, res); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*c.AllocationResponse){func(r *c.AllocationResponse) { r.Decisions = nil }, func(r *c.AllocationResponse) { r.Decisions = append(r.Decisions, r.Decisions[0]) }, func(r *c.AllocationResponse) { r.Decisions[0].DemandID++ }, func(r *c.AllocationResponse) { r.Decisions[0].Order[1]++ }, func(r *c.AllocationResponse) { r.SnapshotHash = "invalid" }, func(r *c.AllocationResponse) { r.Pin.PinID = "wrong" }, func(r *c.AllocationResponse) { r.Decisions[0].AssertionRefs = []string{"outside"} }} {
		_ = json.Unmarshal(raw, &res)
		mutate(&res)
		if validateAllocationResult(req, res) == nil {
			t.Fatal("accepted invalid plan")
		}
	}
}

func TestAllocationConfidenceDoesNotRoundUpToAdmissionThreshold(t *testing.T) {
	raw, _ := c.Bundle.ReadFile("bundle/allocation.request.example.json")
	for _, tc := range []struct {
		confidence float64
		action     string
	}{{.799999, "wait"}, {.8, "assign"}} {
		var req c.AllocationRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		tags, err := allocationTagFacts(Object{"id": 1, "tags": []any{Object{"code": "backend", "status": "supported", "confidence": tc.confidence}}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		req.Snapshot.Members[0].Tags = tags
		if got := expectedAllocation(req.Snapshot)[0].Action; got != tc.action {
			t.Fatalf("confidence %f became %s", tc.confidence, got)
		}
	}
}
