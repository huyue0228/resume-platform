package platform

import (
	"context"
	"errors"
)

func (a *App) seedInitialData(ctx context.Context, db DB) error {
	for key, meta := range a.Spec.AIConfigs {
		if _, err := db.Exec(ctx, "INSERT INTO core_config(key,value) VALUES($1,$2::jsonb) ON CONFLICT(key) DO NOTHING", key, string(canonicalJSON(meta["default"], false))); err != nil {
			return err
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO core_config(key,value) VALUES('welink_enabled','false'::jsonb) ON CONFLICT(key) DO NOTHING`); err != nil {
		return err
	}
	if _, err := a.seedRecord(ctx, db, "core_schooltag", "code", "NON_TARGET", Object{"code": "NON_TARGET", "name": "非目标院校", "is_active": true, "is_default": false}); err != nil {
		return err
	}
	departments := map[string]Object{}
	for _, definition := range []Object{{"name": "技术部", "level": 1}, {"name": "产品部", "level": 1}, {"name": "技术二部", "level": 2, "parent_name": "技术部"}, {"name": "产品二部", "level": 2, "parent_name": "产品部"}} {
		values := clone(definition)
		delete(values, "parent_name")
		values["parent_id"] = departments[str(definition["parent_name"])]["id"]
		row, err := one(ctx, db, "SELECT row_to_json(d) FROM core_department d WHERE name=$1 AND parent_id IS NOT DISTINCT FROM $2::bigint ORDER BY id LIMIT 1", values["name"], values["parent_id"])
		if missingRecord(err) {
			row, err = a.save(ctx, db, "core_department", nil, values)
		}
		if err != nil {
			return err
		}
		departments[str(definition["name"])] = row
	}
	for _, definition := range []Object{{"employee_no": "L2001", "name": "技术接口人A", "department": "技术二部", "role": "secondary"}, {"employee_no": "L2002", "name": "产品接口人B", "department": "产品二部", "role": "secondary"}, {"employee_no": "T3001", "name": "技术简历筛选人A", "department": "技术二部", "role": "tertiary"}, {"employee_no": "T3002", "name": "算法简历筛选人B", "department": "技术二部", "role": "tertiary"}, {"employee_no": "T3003", "name": "产品简历筛选人C", "department": "产品二部", "role": "tertiary"}} {
		dep := departments[str(definition["department"])]
		level := str(definition["role"])
		role, group := contactRole(level)
		contact, err := a.seedRecord(ctx, db, "core_contact", "employee_no", definition["employee_no"], Object{"employee_no": definition["employee_no"], "name": definition["name"], "email": normalized(str(definition["employee_no"])) + "@example.com", "department_id": dep["id"], "contact_level": level, "can_delegate": level != "tertiary", "is_active": true})
		if err != nil {
			return err
		}
		if err = a.seedUser(ctx, db, str(definition["employee_no"]), role, group, contact); err != nil {
			return err
		}
	}
	for _, entry := range []Object{{"username": "admin", "role": "admin", "group": "管理员"}, {"username": "hr", "role": "primary_hr", "group": "一级部门HR"}} {
		if err := a.seedUser(ctx, db, str(entry["username"]), str(entry["role"]), str(entry["group"]), nil); err != nil {
			return err
		}
	}
	return nil
}
func missingRecord(err error) bool {
	var failure *apiError
	return errors.As(err, &failure) && failure.Status == 404
}
func (a *App) seedRecord(ctx context.Context, db DB, table, key string, value any, defaults Object) (Object, error) {
	row, err := one(ctx, db, "SELECT row_to_json(t) FROM "+quote(table)+" t WHERE "+quote(key)+"=$1 ORDER BY id LIMIT 1", value)
	if missingRecord(err) {
		return a.save(ctx, db, table, nil, defaults)
	}
	return row, err
}
func (a *App) seedUser(ctx context.Context, db DB, username, role, group string, contact Object) error {
	existing, err := one(ctx, db, "SELECT row_to_json(u) FROM accounts_user u WHERE username=$1", username)
	if err == nil && existing != nil {
		return nil
	}
	if !missingRecord(err) {
		return err
	}
	email := username + "@example.com"
	if contact != nil {
		email = str(contact["email"])
	}
	user, err := a.save(ctx, db, "accounts_user", nil, Object{"username": username, "email": email, "role": role, "contact_id": contact["id"], "password": "!" + token(20), "is_active": true, "is_staff": group == "管理员", "date_joined": now()})
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, "INSERT INTO accounts_user_groups(user_id,group_id) SELECT $1,id FROM auth_group WHERE name=$2 ON CONFLICT DO NOTHING", user["id"], group)
	return err
}
