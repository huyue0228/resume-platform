package platform

import (
	"context"
	"strings"
)

// Preserve legacy department rows and event snapshots for history, while active
// receivers and grants move to their level-two mailbox. No screener is guessed.
func (a *App) migrateDepartmentInbox(ctx context.Context, db DB) error {
	if _, err := db.Exec(ctx, "CREATE TABLE IF NOT EXISTS platform_go_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	var applied bool
	if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM platform_go_migrations WHERE version='department-inbox/v1')").Scan(&applied); err != nil {
		return err
	}
	if applied {
		return nil
	}
	if _, err := db.Exec(ctx, `
ALTER TABLE core_assignmentattempt ADD COLUMN IF NOT EXISTS assigned_screener_id bigint REFERENCES core_contact(id) ON DELETE SET NULL;
ALTER TABLE core_assignmentattempt ADD COLUMN IF NOT EXISTS assigned_screener_employee_no_snapshot varchar(32) NOT NULL DEFAULT '';
ALTER TABLE core_assignmentattempt ADD COLUMN IF NOT EXISTS assigned_screener_name_snapshot varchar(64) NOT NULL DEFAULT '';
ALTER TABLE core_assignmentattempt ADD COLUMN IF NOT EXISTS screener_assigned_at timestamptz;
ALTER TABLE core_contact DROP CONSTRAINT IF EXISTS core_contact_employee_no_key;
DROP INDEX IF EXISTS core_contact_employee_department_key;
DROP INDEX IF EXISTS core_contact_employee_department_role_key;
UPDATE core_contact SET contact_level='secondary' WHERE contact_level='primary';
UPDATE accounts_user SET role='secondary_contact' WHERE role='primary_contact';
UPDATE accounts_user SET role='primary_hr' WHERE role='hr';`); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS platform_go_department_inbox_history (
      attempt_id bigint PRIMARY KEY REFERENCES core_assignmentattempt(id) ON DELETE CASCADE,
      previous_department_id bigint NOT NULL,previous_department_name text NOT NULL,migrated_at timestamptz NOT NULL DEFAULT now());
INSERT INTO platform_go_department_inbox_history(attempt_id,previous_department_id,previous_department_name)
SELECT a.id,d.id,a.current_department_name_snapshot FROM core_assignmentattempt a JOIN core_department d ON d.id=a.current_department_id JOIN core_department p ON p.id=d.parent_id AND p.level=2 WHERE d.level=3 ON CONFLICT DO NOTHING;
UPDATE core_assignmentattempt a SET current_department_name_snapshot=p.name FROM core_department d JOIN core_department p ON p.id=d.parent_id AND p.level=2 WHERE d.level=3 AND a.current_department_id=d.id`); err != nil {
		return err
	}

	for table, model := range a.Spec.Models {
		if table == "core_department" {
			continue
		}
		for _, field := range model.Fields {
			if field.Relation == "core_department" {
				if _, err := db.Exec(ctx, "UPDATE "+quote(table)+" t SET "+quote(field.Column)+"=d.parent_id FROM core_department d JOIN core_department p ON p.id=d.parent_id AND p.level=2 WHERE d.level=3 AND t."+quote(field.Column)+"=d.id"); err != nil {
					return err
				}
			}
		}
	}
	// Multiple old third-level grants can converge on one mailbox. Keep a stable
	// grant id and all historical references, and combine their activation flags.
	if _, err := db.Exec(ctx, `CREATE TEMP TABLE inbox_grant_merge ON COMMIT DROP AS
SELECT id,min(id) OVER (PARTITION BY employee_no,department_id,contact_level) keeper,
bool_or(is_active) OVER (PARTITION BY employee_no,department_id,contact_level) active,
bool_or(can_delegate) OVER (PARTITION BY employee_no,department_id,contact_level) delegate FROM core_contact;
UPDATE accounts_user u SET contact_id=m.keeper FROM inbox_grant_merge m WHERE u.contact_id=m.id AND m.id<>m.keeper;
UPDATE core_assignmentattempt a SET assigned_screener_id=m.keeper FROM inbox_grant_merge m WHERE a.assigned_screener_id=m.id AND m.id<>m.keeper;
UPDATE core_contact c SET is_active=m.active,can_delegate=(m.delegate AND c.contact_level<>'tertiary') FROM inbox_grant_merge m WHERE c.id=m.keeper;
DELETE FROM core_contact c USING inbox_grant_merge m WHERE c.id=m.id AND m.id<>m.keeper;
SET CONSTRAINTS ALL IMMEDIATE;
CREATE UNIQUE INDEX core_contact_employee_department_role_key ON core_contact(employee_no,COALESCE(department_id,0),contact_level);`); err != nil {
		return err
	}
	for _, names := range [][2]string{{"二级接口人", "接口人"}, {"三级接口人", "简历筛选人"}, {"一级接口人", "接口人"}, {"HR", "一级部门HR"}} {
		// Prefer existing role configuration if an administrator has already made
		// the new role; migrating membership must not expand its permissions.
		for _, statement := range strings.Split(`INSERT INTO accounts_user_groups(user_id,group_id)
SELECT ug.user_id,n.id FROM accounts_user_groups ug JOIN auth_group o ON o.id=ug.group_id JOIN auth_group n ON n.name=$2 WHERE o.name=$1 ON CONFLICT DO NOTHING;
DELETE FROM accounts_user_groups WHERE group_id IN (SELECT o.id FROM auth_group o WHERE o.name=$1 AND EXISTS(SELECT 1 FROM auth_group WHERE name=$2));
DELETE FROM auth_group_permissions WHERE group_id IN (SELECT o.id FROM auth_group o WHERE o.name=$1 AND EXISTS(SELECT 1 FROM auth_group WHERE name=$2));
DELETE FROM auth_group WHERE name=$1 AND EXISTS(SELECT 1 FROM auth_group WHERE name=$2);
UPDATE auth_group SET name=$2 WHERE name=$1`, ";") {
			if _, err := db.Exec(ctx, statement, names[0], names[1]); err != nil {
				return err
			}
		}
	}
	// Apply the newly agreed role policy once during upgrade. Later role edits
	// remain durable because ordinary Seed only initializes missing roles.
	codes := []string{}
	for _, code := range a.Spec.RolePermissions["一级部门HR"] {
		codes = append(codes, strings.ReplaceAll(code, ".", "__"))
	}
	if _, err := db.Exec(ctx, `INSERT INTO auth_group_permissions(group_id,permission_id)
SELECT g.id,p.id FROM auth_group g CROSS JOIN auth_permission p JOIN django_content_type ct ON ct.id=p.content_type_id WHERE g.name='一级部门HR' AND ct.app_label='accounts' AND p.codename=ANY($1::text[]) ON CONFLICT DO NOTHING`, codes); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `DELETE FROM auth_group_permissions gp USING auth_group g,auth_permission p,django_content_type ct WHERE gp.group_id=g.id AND gp.permission_id=p.id AND ct.id=p.content_type_id AND ct.app_label='accounts' AND g.name<>'管理员' AND p.codename LIKE 'settings\_\_%' ESCAPE '\'`); err != nil {
		return err
	}
	_, err := db.Exec(ctx, "INSERT INTO platform_go_migrations(version) VALUES('department-inbox/v1')")
	return err
}
