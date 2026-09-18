package platform

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestPoolFilterUsesCurrentMembershipInsteadOfDispatchStatus(t *testing.T) {
	a := unitApp(t)
	p := &Principal{Permissions: map[string]bool{"resume.view": true}}
	for _, tc := range []struct {
		name, query, member string
		want                bool
	}{
		{"waiting", "admitted", "pending_allocation", true},
		{"allocated", "admitted", "allocated", true},
		{"reassessment", "admitted", "needs_reanalysis", true},
		{"manual allocation is not admission", "admitted", "", false},
		{"closed is not active", "admitted", "closed", false},
		{"not admitted", "none", "", true},
		{"closed", "none", "closed", true},
		{"specific status", "pending_allocation", "allocated", false},
		{"multiple statuses", "allocated,pending_allocation", "allocated", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/candidates/?pool_status="+tc.query, nil)
			if err := validateCandidateFilters(r, p); err != nil {
				t.Fatal(err)
			}
			value := Object{"system_status": "pending_dispatch", "pool_membership": Object{"status": tc.member}}
			got, handled, err := a.candidateFilter(context.Background(), r, p, Object{}, value, "pool_status", tc.query)
			if err != nil || !handled || got != tc.want {
				t.Fatalf("scope mismatch: got %v want %v (%v)", got, tc.want, err)
			}
		})
	}
	if err := validateCandidateFilters(httptest.NewRequest("GET", "/?pool_status=unknown", nil), p); err == nil {
		t.Fatal("invalid pool status accepted")
	}
	if err := validateCandidateFilters(httptest.NewRequest("GET", "/?pool_status=admitted", nil), &Principal{}); err == nil {
		t.Fatal("department viewer used global pool scope")
	}
	if err := validateBulkFilters(Object{"pool_status": "admitted"}); err != nil {
		t.Fatal("bulk operation discarded admission scope", err)
	}
}
