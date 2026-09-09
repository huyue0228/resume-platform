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
	a := integrationApp(t)
	if err := a.Seed(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := a.Seed(context.Background()); err != nil {
		t.Fatalf("idempotent seed: %v", err)
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
	d := prepareSnapshot(s)
	if d["current_volunteer_ref"] != "south" || d["status"] != "job_not_found" {
		t.Fatalf("volunteer boundary changed: %v", d)
	}
	obj(list(s["volunteers"])[1])["rejected"] = true
	d = prepareSnapshot(s)
	if d["current_volunteer_ref"] != "north" || d["status"] != "ready" {
		t.Fatalf("rejected volunteer did not advance: %v", d)
	}
}
func TestReviewOnlyAndIncompleteProfile(t *testing.T) {
	result := Object{"profile": Object{"risks": []any{"profile_incomplete"}}}
	match := Object{"score": .95}
	frozen := Object{"lane": "enforced", "thresholds": Object{"dispatch": .75, "review": .5}}
	if recommendation(result, match, frozen) != "review" {
		t.Fatal("incomplete profile auto-dispatched")
	}
	frozen["lane"] = "review_only"
	match["score"] = .1
	if recommendation(result, match, frozen) != "review" {
		t.Fatal("review-only policy was bypassed")
	}
}
func TestDurationNearestRank(t *testing.T) {
	metric := durationMetric([]float64{10, 2, 4, 8})
	if metric["avg"] != float64(6) || metric["median"] != float64(6) || metric["p90"] != float64(10) {
		t.Fatalf("unexpected metrics: %v", metric)
	}
}
