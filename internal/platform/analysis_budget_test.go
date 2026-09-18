package platform

import (
	"errors"
	"testing"

	pdftext "resume-platform/internal/pdftext"
)

func TestBudgetFailureKeepsSpecificDiagnosticsAndNeverBecomesAssessment(t *testing.T) {
	for _, code := range []string{"token_budget_exhausted", "next_request_budget_insufficient", "context_limit_exceeded", "turn_budget_exhausted", "tool_budget_exhausted", "analysis_stalled"} {
		t.Run(code, func(t *testing.T) {
			request := Object{"protocol_version": protocolVersion, "task_id": "synthetic", "idempotency_key": "key", "workflow_revision": 1, "pin": Object{"pin_id": "synthetic"}}
			result := clone(request)
			result["manifest"] = Object{"terminal_state": "FAILED", "failure_code": code}
			result["safe_trace"] = Object{"input_tokens": 90000, "output_tokens": 6338, "budget": Object{"stop_reason": "next_request", "remaining_tokens": 23662}}
			err := validateAnalysis(request, result, pdftext.Text{}, []string{"job"})
			var failure *taskFailure
			if !errors.As(err, &failure) {
				t.Fatal("incomplete analysis accepted")
			}
			want := "agent_budget_exhausted"
			if code == "analysis_stalled" {
				want = "agent_stalled"
			}
			if failure.Code != want || str(obj(failure.Trace["budget"])["stop_reason"]) != "next_request" || str(failure.Manifest["failure_code"]) != code {
				t.Fatal("failure diagnostics were discarded")
			}
		})
	}
}
