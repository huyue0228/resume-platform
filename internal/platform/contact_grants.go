package platform

import (
	"context"
	"strings"
)

func contactRoleMatchesDepartment(role string, department Object) bool {
	return isJobDepartment(department["level"]) && (role != "secondary_hr" || num(department["level"]) == 2)
}

func isDepartmentHR(role any) bool { return role == "secondary_hr" }

func (a *App) rolePermissionAllowed(name, code string) bool {
	if strings.HasPrefix(code, "settings.") {
		return name == "管理员"
	}
	if contains([]string{"二级部门HR", "接口人", "简历筛选人"}, name) {
		return contains(a.Spec.RolePermissions[name], code)
	}
	return true
}

func (a *App) grantCanViewAttempt(p *Principal, grant DepartmentGrant, at Object) bool {
	if !p.grantHas(grant, "attempt.view_department") {
		return false
	}
	if isDepartmentHR(grant.Contact["contact_level"]) {
		return true
	}
	if !contains([]string{"dispatched", "passed", "rejected"}, str(at["status"])) {
		return false
	}
	return grant.Contact["contact_level"] != "tertiary" || num(at["assigned_screener_id"]) == num(grant.Contact["id"])
}

func (a *App) canManageAttempt(ctx context.Context, db DB, p *Principal, at Object, code string) bool {
	if !p.has(code) {
		return false
	}
	if p.has("attempt.view_all") {
		return true
	}
	dep, err := a.get(ctx, db, "core_department", at["current_department_id"])
	if err != nil {
		return false
	}
	for _, grant := range p.departmentGrants() {
		if isDepartmentHR(grant.Contact["contact_level"]) && p.grantHas(grant, code) && p.grantHas(grant, "attempt.view_department") && a.grantCovers(ctx, db, grant, dep) {
			return true
		}
	}
	return false
}

func (a *App) grantCovers(ctx context.Context, db DB, grant DepartmentGrant, department Object) bool {
	if grant.Department == nil || department == nil {
		return false
	}
	if num(grant.Department["id"]) == num(department["id"]) {
		return true
	}
	return contactIncludesDescendants(grant.Contact["contact_level"]) && a.departmentInScope(ctx, db, department, grant.Department["id"])
}

func grantCanDelegate(p *Principal, grant DepartmentGrant) bool {
	return p.grantHas(grant, "attempt.view_department") && p.grantHas(grant, "attempt.transfer_department") && contactIncludesDescendants(grant.Contact["contact_level"]) && truth(grant.Contact["can_delegate"])
}

func (a *App) canTransferAttempt(ctx context.Context, db DB, p *Principal, at, target Object) bool {
	if !p.has("attempt.transfer_department") {
		return false
	}
	if p.has("attempt.view_all") {
		return true
	}
	current, err := a.get(ctx, db, "core_department", at["current_department_id"])
	if err != nil {
		return false
	}
	for _, grant := range p.departmentGrants() {
		if !grantCanDelegate(p, grant) || !a.grantCovers(ctx, db, grant, current) {
			continue
		}
		if target == nil || (isJobDepartment(target["level"]) && (!isDepartmentHR(grant.Contact["contact_level"]) || a.grantCovers(ctx, db, grant, target))) {
			return true
		}
	}
	return false
}

func canFeedbackAttempt(p *Principal, at Object) bool {
	for _, grant := range p.departmentGrants() {
		if p.grantHas(grant, "attempt.view_department") && p.grantHas(grant, "attempt.feedback") && num(grant.Department["id"]) == num(at["current_department_id"]) {
			if num(at["assigned_screener_id"]) > 0 || at["screener_assigned_at"] != nil {
				if num(at["assigned_screener_id"]) == num(grant.Contact["id"]) && grant.Contact["contact_level"] == "tertiary" {
					return true
				}
				continue
			}
			if grant.Contact["contact_level"] == "secondary" {
				return true
			}
		}
	}
	return false
}

func (a *App) canExportAttempt(ctx context.Context, p *Principal, at Object) bool {
	if p.has("attempt.view_all") {
		return p.has("attempt.export")
	}
	department, err := a.get(ctx, a.Pool, "core_department", at["current_department_id"])
	if err != nil {
		return false
	}
	for _, grant := range p.departmentGrants() {
		if a.grantCanViewAttempt(p, grant, at) && p.grantHas(grant, "attempt.export") && a.grantCovers(ctx, a.Pool, grant, department) {
			return true
		}
	}
	return false
}

func (a *App) findContactGrant(ctx context.Context, db DB, employee string, department any, role string) (Object, error) {
	grant, err := one(ctx, db, "SELECT row_to_json(c) FROM core_contact c WHERE employee_no=$1 AND department_id IS NOT DISTINCT FROM $2::bigint AND contact_level=$3", employee, department, role)
	if missingRecord(err) {
		return nil, nil
	}
	return grant, err
}

// The legacy user.contact_id remains a display pointer. All grants belong to the
// single employee account and authorization must never depend on that pointer.
func (a *App) syncContactUser(ctx context.Context, db DB, c Object) error {
	employee, email := str(c["employee_no"]), strings.ToLower(str(c["email"]))
	users, err := rows(ctx, db, "SELECT row_to_json(u) FROM accounts_user u WHERE username=$1 OR lower(email)=$2 OR contact_id IN (SELECT id FROM core_contact WHERE employee_no=$1) ORDER BY id FOR UPDATE", employee, email)
	if err != nil {
		return err
	}
	var user Object
	for _, u := range users {
		if str(u["username"]) == "012358" {
			return bad("内置管理员不允许绑定接口人")
		}
		if str(u["username"]) != employee || user != nil {
			return bad("工号和邮箱映射到不同账号")
		}
		user = u
	}
	// Name and email describe the person, while role/active/delegation describe a grant.
	if _, err = db.Exec(ctx, "UPDATE core_contact SET name=$2,email=$3,name_pinyin=$4,name_pinyin_initials=$5 WHERE employee_no=$1", employee, c["name"], email, c["name_pinyin"], c["name_pinyin_initials"]); err != nil {
		return err
	}
	grants, err := rows(ctx, db, "SELECT row_to_json(c) FROM core_contact c WHERE employee_no=$1 ORDER BY is_active DESC,id", employee)
	if err != nil || (user == nil && len(grants) == 0) {
		return err
	}
	var anchor Object
	groups := []string{}
	role := str(user["role"])
	for _, grant := range grants {
		if !truth(grant["is_active"]) || !validContactLevel(str(grant["contact_level"])) {
			continue
		}
		grantRole, group := contactRole(str(grant["contact_level"]))
		if anchor == nil {
			anchor = grant
			if !contains([]string{"admin", "primary_hr", "hr"}, role) {
				role = grantRole
			}
		}
		if !contains(groups, group) {
			groups = append(groups, group)
		}
	}
	values := Object{"username": employee, "email": email, "contact_id": anchor["id"]}
	if role != "" {
		values["role"] = role
	}
	if user == nil {
		values["date_joined"], values["password"], values["is_active"] = now(), "!"+token(20), true
	}
	user, err = a.save(ctx, db, "accounts_user", user["id"], values)
	if err != nil {
		return err
	}
	if _, err = db.Exec(ctx, "DELETE FROM accounts_user_groups WHERE user_id=$1 AND group_id IN (SELECT id FROM auth_group WHERE name IN ('接口人','简历筛选人','二级部门HR'))", user["id"]); err != nil {
		return err
	}
	for _, group := range groups {
		g, err := one(ctx, db, "SELECT row_to_json(g) FROM auth_group g WHERE name=$1", group)
		if missingRecord(err) {
			g, err = a.save(ctx, db, "auth_group", nil, Object{"name": group})
			if err != nil {
				return err
			}
			// Initialize new roles once; preserve administrator edits to existing roles.
			for _, code := range a.Spec.RolePermissions[group] {
				if _, err = db.Exec(ctx, "INSERT INTO auth_group_permissions(group_id,permission_id) SELECT $1,p.id FROM auth_permission p JOIN django_content_type c ON c.id=p.content_type_id WHERE c.app_label='accounts' AND p.codename=$2 ON CONFLICT DO NOTHING", g["id"], strings.ReplaceAll(code, ".", "__")); err != nil {
					return err
				}
			}
		}
		if err != nil {
			return err
		}
		if _, err = db.Exec(ctx, "INSERT INTO accounts_user_groups(user_id,group_id) VALUES($1,$2) ON CONFLICT DO NOTHING", user["id"], g["id"]); err != nil {
			return err
		}
	}
	return nil
}
