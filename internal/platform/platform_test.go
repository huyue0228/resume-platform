package platform

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"resume-platform/internal/compat"
)

func unitApp(t *testing.T) *App {
	t.Helper()
	a := &App{Config: Config{Secret: "test-only-secret"}}
	if err := json.Unmarshal(compat.Spec, &a.Spec); err != nil {
		t.Fatal(err)
	}
	return a
}
func integrationApp(t *testing.T) *App {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	a, err := New(ctx, Config{DatabaseURL: url, RedisURL: env("TEST_REDIS_URL", "redis://127.0.0.1:56379/0"), Secret: "test-only-secret", MediaRoot: t.TempDir(), Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err = a.Migrate(ctx); err != nil {
		t.Fatalf("migration: %v", err)
	}
	return a
}
func TestMigrationAndSeed(t *testing.T) {
	a := isolatedInboxApp(t, false)
	if err := a.Seed(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := a.Seed(context.Background()); err != nil {
		t.Fatalf("idempotent seed: %v", err)
	}
	var invalid, count int
	if err := a.Pool.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE
		(d.level = 1 AND d.parent_id IS NOT NULL) OR
		(d.level = 2 AND (p.id IS NULL OR p.level <> 1)) OR
		d.level NOT IN (1, 2))
		FROM core_department d LEFT JOIN core_department p ON p.id = d.parent_id`).Scan(&count, &invalid); err != nil {
		t.Fatal(err)
	}
	if count != 4 || invalid != 0 {
		t.Fatalf("fresh initialization must create a valid two-level department tree without duplicates: count=%d invalid=%d", count, invalid)
	}
}
func TestPrivateConnectionEncryption(t *testing.T) {
	a := unitApp(t)
	key := "unit-test-key"
	encrypted, err := a.encryptKey(key)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := a.decryptKey(encrypted)
	if err != nil || plain != key {
		t.Fatal("encrypted connection key did not round trip")
	}
	other := unitApp(t)
	other.Config.Secret = "different-secret"
	if _, err = other.decryptKey(encrypted); err == nil {
		t.Fatal("accepted ciphertext from a different platform secret")
	}
}
func TestAdmissionNeverSkipsVolunteer(t *testing.T) {
	s := Object{"candidate": Object{"household_province": "上海", "first_degree_tag_ref": "first", "highest_degree_tag_ref": "highest", "highest_education": "bachelor"}, "workflow": Object{}, "volunteers": []any{Object{"ref": "north", "entity": "GW", "position_name": "有岗位", "apply_date": "2026-09-01"}, Object{"ref": "south", "entity": "YLS", "position_name": "未配置岗位", "apply_date": "2026-09-02"}}, "admission_rules": []any{Object{"ref": "rule", "priority": 1, "first_tag_refs": []any{"first"}, "highest_tag_refs": []any{"highest"}, "educations": []any{"bachelor"}}}, "jobs": []any{Object{"ref": "job", "entity": "GW", "public_name": "有岗位", "position_name": "开发", "department_ref": "dep", "department_level": 2, "responsibilities": "服务开发"}}}
	s["pool_policy"] = Object{"pools": []any{Object{"code": "pool", "entity": "GW"}}, "standards": []any{Object{"code": "standard", "pool_code": "pool", "entity": "GW", "application_names": []any{"有岗位"}, "responsibilities": "服务开发"}}}
	d := prepareSnapshot(s)
	if d["current_volunteer_ref"] != "south" || d["status"] != "assessment_standard_missing" {
		t.Fatalf("volunteer boundary changed: %v", d)
	}
	obj(list(s["volunteers"])[1])["rejected"] = true
	d = prepareSnapshot(s)
	if d["current_volunteer_ref"] != "north" || d["status"] != "ready" {
		t.Fatalf("rejected volunteer did not advance: %v", d)
	}
}
func TestSingleAdmissionThresholdNeverCreatesReview(t *testing.T) {
	for _, lane := range []string{"enforced", "review_only"} {
		for _, risk := range [][]any{{}, {"profile_incomplete"}} {
			for _, test := range []struct {
				score float64
				want  string
			}{{.1, "archive"}, {.5, "archive"}, {.7499, "archive"}, {.75, "dispatch"}, {.95, "dispatch"}} {
				result := Object{"profile": Object{"risks": risk}}
				frozen := Object{"lane": lane, "thresholds": Object{"dispatch": .75, "review": .5}}
				if got := recommendation(result, Object{"score": test.score}, frozen); got != test.want {
					t.Fatalf("lane=%s score=%v result=%s", lane, test.score, got)
				}
			}
		}
	}
}

func TestDurationNearestRank(t *testing.T) {
	metric := durationMetric([]float64{10, 2, 4, 8})
	if metric["avg"] != float64(6) || metric["median"] != float64(6) || metric["p90"] != float64(10) {
		t.Fatalf("unexpected metrics: %v", metric)
	}
}
