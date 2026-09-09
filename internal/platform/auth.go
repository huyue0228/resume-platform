package platform

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

type Principal struct {
	User        Object
	Permissions map[string]bool
	Roles       []string
	Contact     Object
	Department  Object
}

func (p *Principal) has(code string) bool { return p != nil && p.Permissions[code] }
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
	p := &Principal{User: user, Permissions: map[string]bool{}, Roles: []string{}}
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
		permissions, err := rows(ctx, a.Pool, `SELECT row_to_json(p) FROM auth_permission p JOIN django_content_type c ON p.content_type_id=c.id WHERE c.app_label='accounts' AND (p.id IN (SELECT permission_id FROM accounts_user_user_permissions WHERE user_id=$1) OR p.id IN (SELECT gp.permission_id FROM auth_group_permissions gp JOIN accounts_user_groups ug ON ug.group_id=gp.group_id WHERE ug.user_id=$1))`, user["id"])
		if err != nil {
			return nil, err
		}
		for _, row := range permissions {
			code := strings.ReplaceAll(str(row["codename"]), "__", ".")
			if known[code] {
				p.Permissions[code] = true
			}
		}
	}
	if user["contact_id"] != nil {
		p.Contact, _ = a.get(ctx, a.Pool, "core_contact", user["contact_id"])
		if p.Contact != nil {
			p.Department, _ = a.get(ctx, a.Pool, "core_department", p.Contact["department_id"])
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
	scope := Object{"type": "none"}
	if p.Contact != nil {
		result["contact"], _ = a.serialize(ctx, "contacts", p.Contact, p, false)
	}
	if p.has("attempt.view_all") {
		scope = Object{"type": "all"}
	} else if p.has("attempt.view_department") && truth(p.Contact["is_active"]) && p.Department != nil {
		descendant := str(p.Contact["contact_level"]) == "secondary" && num(p.Department["level"]) == 2
		ids := []any{p.Department["id"]}
		if descendant {
			children, _ := rows(ctx, a.Pool, "SELECT row_to_json(d) FROM core_department d WHERE parent_id=$1 AND level=3 ORDER BY id", p.Department["id"])
			for _, d := range children {
				ids = append(ids, d["id"])
			}
		}
		scope = Object{"type": "department", "department_id": p.Department["id"], "department_level": p.Department["level"], "department_ids": ids, "include_descendants": descendant}
	}
	result["data_scope"] = scope
	return result
}
func (a *App) visibleAttempt(ctx context.Context, p *Principal, attempt Object) bool {
	if p.has("attempt.view_all") {
		return true
	}
	if !p.has("attempt.view_department") || p.Contact == nil || !truth(p.Contact["is_active"]) || p.Department == nil {
		return false
	}
	status := str(attempt["status"])
	if status != "dispatched" && status != "passed" && status != "rejected" {
		return false
	}
	dep, err := a.get(ctx, a.Pool, "core_department", attempt["current_department_id"])
	if err != nil {
		return false
	}
	if str(p.Contact["contact_level"]) == "secondary" && num(p.Department["level"]) == 2 {
		return num(dep["id"]) == num(p.Department["id"]) || (num(dep["level"]) == 3 && num(dep["parent_id"]) == num(p.Department["id"]))
	}
	return str(p.Contact["contact_level"]) == "tertiary" && num(p.Department["level"]) == 3 && num(dep["id"]) == num(p.Department["id"])
}
