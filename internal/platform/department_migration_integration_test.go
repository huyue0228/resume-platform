package platform

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"resume-platform/internal/compat"
)

// Each migration/replace test owns a new database and never mutates the shared
// integration database or any deployment database.
func isolatedInboxApp(t *testing.T, legacy bool) *App {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	config, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	control, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	name := "resume_inbox_test_" + token(6)
	if _, err = control.Exec(ctx, "CREATE DATABASE "+quote(name)); err != nil {
		control.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := control.Exec(ctx, "DROP DATABASE "+quote(name)); err != nil {
			t.Error(err)
		}
		control.Close(ctx)
	})
	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || (dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") {
		t.Fatal("isolated database tests require a PostgreSQL URL")
	}
	dsn.Path = "/" + name
	a, err := New(ctx, Config{DatabaseURL: dsn.String(), RedisURL: os.Getenv("TEST_REDIS_URL"), Secret: "test-only-secret", MediaRoot: t.TempDir(), Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if legacy {
		lines := []string{}
		for _, line := range strings.Split(compat.Schema, "\n") {
			if !strings.HasPrefix(line, "\\") && !strings.Contains(line, "pg_catalog.set_config('search_path'") {
				lines = append(lines, line)
			}
		}
		if _, err = a.Pool.Exec(ctx, strings.Join(lines, "\n")); err != nil {
			t.Fatal(err)
		}
	} else if err = a.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDepartmentInboxUpgradePreservesIdentityAndHistory(t *testing.T) {
	a := isolatedInboxApp(t, true)
	ctx := context.Background()
	root := mustSave(t, a, "core_department", Object{"name": "旧一级", "level": 1})
	parent := mustSave(t, a, "core_department", Object{"name": "旧二级", "level": 2, "parent_id": root["id"]})
	leaf := mustSave(t, a, "core_department", Object{"name": "旧三级", "level": 3, "parent_id": parent["id"]})
	contact := mustSave(t, a, "core_contact", Object{"name": "筛选人", "employee_no": "EMP1", "email": "emp1@example.test", "department_id": leaf["id"], "contact_level": "tertiary"})
	user := mustSave(t, a, "accounts_user", Object{"username": "EMP1", "email": "emp1@example.test", "password": "!unusable", "contact_id": contact["id"], "date_joined": now(), "is_active": true})
	group := mustSave(t, a, "auth_group", Object{"name": "三级接口人"})
	if _, err := a.Pool.Exec(ctx, "INSERT INTO accounts_user_groups(user_id,group_id) VALUES($1,$2)", user["id"], group["id"]); err != nil {
		t.Fatal(err)
	}
	candidate := mustSave(t, a, "core_candidate", Object{"name": "历史候选人", "identity_hash": token(32)})
	resume := mustSave(t, a, "core_resume", Object{"candidate_id": candidate["id"], "apply_id": "OLD1"})
	workflow := mustSave(t, a, "core_candidateworkflow", Object{"candidate_id": candidate["id"], "current_resume_id": resume["id"]})
	model := a.Spec.Models["core_assignmentattempt"]
	original := model.Fields
	model.Fields = nil
	for _, f := range original {
		if !strings.HasPrefix(f.Name, "assigned_screener") && f.Name != "screener_assigned_at" {
			model.Fields = append(model.Fields, f)
		}
	}
	a.Spec.Models["core_assignmentattempt"] = model
	at := mustSave(t, a, "core_assignmentattempt", Object{"workflow_id": workflow["id"], "resume_id": resume["id"], "attempt_no": 1, "initial_department_id": parent["id"], "current_department_id": leaf["id"], "current_department_name_snapshot": "旧三级", "status": "dispatched"})
	model.Fields = original
	a.Spec.Models["core_assignmentattempt"] = model
	event := mustSave(t, a, "core_assignmenthandlingevent", Object{"attempt_id": at["id"], "event_type": "department_transferred", "from_department_id": parent["id"], "to_department_id": leaf["id"], "to_department_name_snapshot": "旧三级"})
	for i := 0; i < 2; i++ {
		if err := a.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.get(ctx, a.Pool, "core_contact", contact["id"])
	if err != nil || num(got["department_id"]) != num(parent["id"]) {
		t.Fatal("grant id or mailbox changed incorrectly", err)
	}
	got, err = a.get(ctx, a.Pool, "accounts_user", user["id"])
	if err != nil || num(got["contact_id"]) != num(contact["id"]) || got["password"] != "!unusable" {
		t.Fatal("upgrade changed account identity", err)
	}
	got, err = a.get(ctx, a.Pool, "core_assignmentattempt", at["id"])
	if err != nil || num(got["current_department_id"]) != num(parent["id"]) || got["assigned_screener_id"] != nil || got["status"] != "dispatched" || got["current_department_name_snapshot"] != "旧二级" {
		t.Fatal("upgrade guessed an assignee or lost work", err)
	}
	got, err = a.get(ctx, a.Pool, "core_assignmenthandlingevent", event["id"])
	if err != nil || got["to_department_name_snapshot"] != "旧三级" {
		t.Fatal("upgrade changed historical event snapshot", err)
	}
	got, err = a.get(ctx, a.Pool, "auth_group", group["id"])
	if err != nil || got["name"] != "简历筛选人" {
		t.Fatal("renaming discarded configured role", err)
	}
	// Old employee uniqueness is replaced without changing the existing account.
	mustSave(t, a, "core_contact", Object{"name": "筛选人", "employee_no": "EMP1", "email": "emp1@example.test", "department_id": parent["id"], "contact_level": "secondary"})
}

func TestReplaceImportRevokesOnlyMissingGrantsAndPreservesAccountRoles(t *testing.T) {
	a := isolatedInboxApp(t, false)
	admin := adminPrincipal(t, a)
	ctx := context.Background()
	employee := token(8)
	base := Object{"姓名": "接口人", "工号": employee, "邮箱": employee + "@example.test", "一层部门": "中心", "二层部门": "A", "角色": "接口人"}
	other := clone(base)
	other["二层部门"] = "B"
	responseObject(t, importTableFixture(t, a, admin, "contacts", "incremental", []Object{base, other}), 200)
	p := grantPrincipal(t, a, employee)
	custom := mustSave(t, a, "auth_group", Object{"name": "额外业务角色"})
	if _, err := a.Pool.Exec(ctx, "INSERT INTO accounts_user_groups(user_id,group_id) VALUES($1,$2)", p.User["id"], custom["id"]); err != nil {
		t.Fatal(err)
	}
	responseObject(t, importTableFixture(t, a, admin, "contacts", "replace", []Object{base}), 200)
	p = grantPrincipal(t, a, employee)
	active := 0
	for _, grant := range p.Grants {
		if truth(grant.Contact["is_active"]) {
			active++
		}
	}
	if active != 1 || len(p.Grants) != 2 || !truth(p.User["is_active"]) || !contains(p.Roles, "额外业务角色") {
		t.Fatal("replace changed identity or unrelated roles")
	}
}
