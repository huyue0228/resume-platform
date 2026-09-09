package platform

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TEST_LEGACY_DATABASE_URL 必须指向用旧版 Django migration 初始化的独立验收库。
func TestUpgradePreservesLegacyDataAndRejectsActiveV1(t *testing.T) {
	address := os.Getenv("TEST_LEGACY_DATABASE_URL")
	if address == "" {
		t.Skip("TEST_LEGACY_DATABASE_URL is not configured")
	}
	if !strings.Contains(address, "resume_upgrade_verify") {
		t.Fatal("upgrade test requires dedicated resume_upgrade_verify database")
	}
	ctx := context.Background()
	a, err := New(ctx, Config{DatabaseURL: address, RedisURL: env("TEST_REDIS_URL", "redis://127.0.0.1:56379/0"), Secret: "test-only-secret", MediaRoot: t.TempDir(), Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.Pool.Exec(ctx, "UPDATE core_processingrun SET status='running' WHERE protocol_version='resume-analysis/v1'"); err != nil {
		t.Fatal(err)
	}
	if err = a.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "在途旧协议") {
		t.Fatalf("active v1 migration was not blocked: %v", err)
	}
	before := map[string]string{}
	for _, table := range []string{"accounts_user", "accounts_user_groups", "auth_group", "auth_group_permissions", "authtoken_token", "core_candidate", "core_resume", "core_candidateworkflow", "core_config"} {
		var raw string
		if err = a.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(t ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "`+table+`" t`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		before[table] = raw
	}
	if _, err = a.Pool.Exec(ctx, "UPDATE core_processingrun SET status='cancelled',finished_at=now() WHERE protocol_version='resume-analysis/v1'"); err != nil {
		t.Fatal(err)
	}
	if err = a.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.Migrate(ctx); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if err = a.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	for table, expected := range before {
		var raw string
		if err = a.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(t ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "`+table+`" t`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if raw != expected {
			t.Errorf("upgrade changed existing data in %s", table)
		}
	}
	_, key, err := a.connection(ctx)
	if err != nil || key != "legacy-fixture-key" {
		t.Fatal("Go cannot decrypt the existing Django model key")
	}
	var columns int
	if err = a.Pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_name='core_processingrunscopeitem' AND column_name IN ('processing_node','text_extraction_id')").Scan(&columns); err != nil || columns != 2 {
		t.Fatal("text migration columns are missing")
	}
}
