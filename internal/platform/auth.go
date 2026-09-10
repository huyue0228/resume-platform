package platform

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

type Principal struct {
	User              Object
	Permissions       map[string]bool
	Roles             []string
	Contact           Object
	Department        Object
	Grants            []DepartmentGrant
	GlobalPermissions map[string]bool
	GrantPermissions  map[string]map[string]bool
}

type DepartmentGrant struct {
	Contact, Department Object
}

func (p *Principal) departmentGrants() []DepartmentGrant {
	if p == nil {
		return nil
	}
	if p.Grants != nil {
		return p.Grants
	}
	if p.Contact != nil && p.Department != nil {
		return []DepartmentGrant{{p.Contact, p.Department}}
	}
	return nil
}

func (p *Principal) grantHas(grant DepartmentGrant, code string) bool {
	if !truth(grant.Contact["is_active"]) || !validContactLevel(str(grant.Contact["contact_level"])) || !contactRoleMatchesDepartment(str(grant.Contact["contact_level"]), grant.Department) {
		return false
	}
	if p.GlobalPermissions == nil || truth(p.User["is_superuser"]) {
		return p.has(code)
	}
	return p.GlobalPermissions[code] || p.GrantPermissions[str(grant.Contact["contact_level"])][code]
}

func (p *Principal) isAdministrator() bool {
	return p != nil && (truth(p.User["is_superuser"]) || contains(p.Roles, "管理员"))
}
func (p *Principal) has(code string) bool {
	return p != nil && p.Permissions[code] && (!strings.HasPrefix(code, "settings.") || p.isAdministrator())
}
func (p *Principal) allowed(codes any) bool {
	if codes == nil {
		return p != nil
	}
	if s, ok := codes.(string); ok {
		return p.has(s)
	}
	for _, v := range list(codes) {
		if p.has(str(v)) {
			return true
		}
	}
	return false
}
func (a *App) principal(r *http.Request) (*Principal, error) {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Token ") {
		return nil, &apiError{401, "身份认证信息未提供"}
	}
	user, err := one(r.Context(), a.Pool, "SELECT row_to_json(u) FROM accounts_user u JOIN authtoken_token t ON t.user_id=u.id WHERE t.key=$1 AND u.is_active", strings.TrimPrefix(value, "Token "))
	if err != nil {
		return nil, &apiError{401, "无效或已过期的登录凭据"}
	}
	return a.userPrincipal(r.Context(), user)
}
func (a *App) userPrincipal(ctx context.Context, user Object) (*Principal, error) {
	p := &Principal{User: user, Permissions: map[string]bool{}, Roles: []string{}, Grants: []DepartmentGrant{}, GlobalPermissions: map[string]bool{}, GrantPermissions: map[string]map[string]bool{}}
	groups, err := rows(ctx, a.Pool, "SELECT row_to_json(g) FROM auth_group g JOIN accounts_user_groups ug ON ug.group_id=g.id WHERE ug.user_id=$1 ORDER BY g.name", user["id"])
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		p.Roles = append(p.Roles, str(g["name"]))
	}
	known := map[string]bool{}
	for _, module := range a.Spec.PermissionTree {
		for _, child := range list(module["children"]) {
			known[str(obj(child)["code"])] = true
		}
	}
	if truth(user["is_superuser"]) {
		p.Permissions = known
	} else {
		permissions, err := rows(ctx, a.Pool, `SELECT jsonb_build_object('codename',p.codename,'group_name','') FROM auth_permission p JOIN django_content_type c ON p.content_type_id=c.id JOIN accounts_user_user_permissions up ON up.permission_id=p.id WHERE c.app_label='accounts' AND up.user_id=$1
UNION ALL SELECT jsonb_build_object('codename',p.codename,'group_name',g.name) FROM auth_permission p JOIN django_content_type c ON p.content_type_id=c.id JOIN auth_group_permissions gp ON gp.permission_id=p.id JOIN auth_group g ON g.id=gp.group_id JOIN accounts_user_groups ug ON ug.group_id=g.id WHERE c.app_label='accounts' AND ug.user_id=$1`, user["id"])
		if err != nil {
			return nil, err
		}
		for _, row := range permissions {
			code := strings.ReplaceAll(str(row["codename"]), "__", ".")
			if known[code] && a.rolePermissionAllowed(str(row["group_name"]), code) {
				p.Permissions[code] = true
				level := map[string]string{"接口人": "secondary", "简历筛选人": "tertiary", "二级部门HR": "secondary_hr"}[str(row["group_name"])]
				if level == "" {
					p.GlobalPermissions[code] = true
				} else {
					if p.GrantPermissions[level] == nil {
						p.GrantPermissions[level] = map[string]bool{}
					}
					p.GrantPermissions[level][code] = true
				}
			}
		}
	}
	contacts, err := rows(ctx, a.Pool, "SELECT row_to_json(c) FROM core_contact c WHERE employee_no=$1 ORDER BY id", user["username"])
	if err != nil {
		return nil, err
	}
	for _, contact := range contacts {
		department, err := a.get(ctx, a.Pool, "core_department", contact["department_id"])
		if err != nil {
			return nil, err
		}
		p.Grants = append(p.Grants, DepartmentGrant{contact, department})
		if truth(contact["is_active"]) && department != nil && (p.Contact == nil || num(contact["id"]) == num(user["contact_id"])) {
			p.Contact, p.Department = contact, department
		}
	}
	for code := range p.Permissions {
		if !p.has(code) {
			delete(p.Permissions, code)
		}
	}
	return p, nil
}
func (a *App) me(ctx context.Context, p *Principal) Object {
	result := Object{}
	for _, key := range []string{"id", "username", "first_name", "last_name", "email", "role", "is_superuser", "is_staff"} {
		result[key] = p.User[key]
	}
	result["roles"] = p.Roles
	permissions := []string{}
	for code := range p.Permissions {
		permissions = append(permissions, code)
	}
	sort.Strings(permissions)
	result["permissions"] = permissions
	result["contact"] = nil
	result["contacts"] = []any{}
	scope := Object{"type": "none"}
	if p.Contact != nil {
		result["contact"], _ = a.serialize(ctx, "contacts", p.Contact, p, false)
	}
	ids := map[int64]bool{}
	assignments := []any{}
	includeDescendants := false
	for _, grant := range p.departmentGrants() {
		value, _ := a.serialize(ctx, "contacts", grant.Contact, p, false)
		result["contacts"] = append(list(result["contacts"]), value)
		if !p.grantHas(grant, "attempt.view_department") {
			continue
		}
		descendant := contactIncludesDescendants(grant.Contact["contact_level"])
		includeDescendants = includeDescendants || descendant
		ids[num(grant.Department["id"])] = true
		if descendant {
			children, _ := rows(ctx, a.Pool, "WITH RECURSIVE subtree AS (SELECT id FROM core_department WHERE parent_id=$1 UNION SELECT d.id FROM core_department d JOIN subtree s ON d.parent_id=s.id) SELECT row_to_json(d) FROM core_department d JOIN subtree s ON d.id=s.id ORDER BY d.id", grant.Department["id"])
			for _, d := range children {
				ids[num(d["id"])] = true
			}
		}
		assignments = append(assignments, Object{"contact_id": grant.Contact["id"], "department_id": grant.Department["id"], "department_level": grant.Department["level"], "contact_level": grant.Contact["contact_level"], "include_descendants": descendant, "can_delegate": grantCanDelegate(p, grant)})
	}
	if p.has("attempt.view_all") {
		scope = Object{"type": "all"}
	} else if len(assignments) > 0 {
		ordered := make([]int64, 0, len(ids))
		for id := range ids {
			ordered = append(ordered, id)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
		scope = Object{"type": "department", "department_ids": ordered, "include_descendants": includeDescendants, "assignments": assignments}
		if len(assignments) == 1 {
			scope["department_id"] = obj(assignments[0])["department_id"]
			scope["department_level"] = obj(assignments[0])["department_level"]
		}
	}
	result["data_scope"] = scope
	return result
}
func (a *App) visibleAttempt(ctx context.Context, p *Principal, attempt Object) bool {
	if p.has("attempt.view_all") {
		return true
	}
	if !p.has("attempt.view_department") {
		return false
	}
	dep, err := a.get(ctx, a.Pool, "core_department", attempt["current_department_id"])
	if err != nil {
		return false
	}
	for _, grant := range p.departmentGrants() {
		if a.grantCanViewAttempt(p, grant, attempt) && a.grantCovers(ctx, a.Pool, grant, dep) {
			return true
		}
	}
	return false
}
