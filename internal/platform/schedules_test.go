package platform

import (
	"testing"
	"time"
)

func TestScheduleValidation(t *testing.T) {
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	valid := Object{"name": "夜间处理", "run_at": "2026-09-12T02:30:00+08:00", "repeat": "once", "scope": Object{"system_statuses": []any{"raw"}}}
	if _, _, err := validateSchedule(valid, now); err != nil {
		t.Fatal(err)
	}
	for _, change := range []Object{
		{"name": " "}, {"run_at": "2026-09-12T02:30:00"}, {"run_at": now.Format(time.RFC3339)},
		{"repeat": "hourly"}, {"step": "step4"}, {"scope": Object{}},
		{"scope": Object{"candidate_ids": []any{float64(1)}, "system_statuses": []any{"raw"}}},
		{"scope": Object{"candidate_ids": []any{float64(-1)}}},
		{"scope": Object{"system_statuses": []any{"unknown"}}},
		{"scope": Object{"system_statuses": []any{"raw"}, "force_reprocess": true}},
		{"scope": Object{"system_statuses": []any{"raw"}, "source": "feedback_rejected"}},
	} {
		body := clone(valid)
		for key, value := range change {
			body[key] = value
		}
		if _, _, err := validateSchedule(body, now); err == nil {
			t.Fatalf("accepted invalid schedule: %v", change)
		}
	}
}

func TestScheduleClockSkipsMissedIntervals(t *testing.T) {
	parse := func(value string) time.Time { result, _ := time.Parse(time.RFC3339, value); return result }
	for _, test := range []struct{ repeat, due, now, want string }{
		{"once", "2026-09-11T02:30:00+08:00", "2026-09-11T02:30:00+08:00", ""},
		{"daily", "2026-09-11T02:30:00+08:00", "2026-09-11T02:30:00+08:00", "2026-09-12T02:30:00+08:00"},
		{"daily", "2026-09-11T02:30:00+08:00", "2026-09-14T01:00:00+08:00", "2026-09-14T02:30:00+08:00"},
		{"weekly", "2026-09-11T02:30:00+08:00", "2026-09-25T02:30:00+08:00", "2026-10-02T02:30:00+08:00"},
		{"daily", "2028-02-28T02:30:00+08:00", "2028-02-28T02:30:00+08:00", "2028-02-29T02:30:00+08:00"},
	} {
		got := nextScheduleTime(parse(test.due), test.repeat, parse(test.now))
		if test.want == "" {
			if got != nil {
				t.Fatal("one-shot schedule repeated")
			}
		} else if got == nil || !got.Equal(parse(test.want)) {
			t.Fatalf("%+v: got %v", test, got)
		}
	}
}
