package platform

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
)

// 内置专业词表沿用迁移前 seed_base 的内容；只补缺项，保留管理员的修改。
//
//go:embed initial_seed.json
var initialSeed []byte

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
	var categories []Object
	if err := json.Unmarshal(initialSeed, &categories); err != nil {
		return err
	}
	for _, entry := range categories {
		values := clone(entry)
		delete(values, "aliases")
		category, err := a.seedRecord(ctx, db, "core_majorcategory", "code", entry["code"], values)
		if err != nil {
			return err
		}
		for _, alias := range list(entry["aliases"]) {
			normalizedName := normalized(str(alias))
			var exists bool
			if err = db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_majoralias WHERE category_id=$1 AND normalized_name=$2 AND source='builtin')", category["id"], normalizedName).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				if _, err = a.save(ctx, db, "core_majoralias", nil, Object{"category_id": category["id"], "name": alias, "normalized_name": normalizedName, "source": "builtin", "match_type": "contains", "note": "内置第一版词表", "is_active": true}); err != nil {
					return err
				}
			}
		}
	}
	departments := map[string]Object{}
	for _, definition := range []Object{{"name": "技术二部", "level": 2}, {"name": "产品二部", "level": 2}, {"name": "技术平台组", "level": 3, "parent_name": "技术二部"}, {"name": "算法应用组", "level": 3, "parent_name": "技术二部"}, {"name": "产品运营组", "level": 3, "parent_name": "产品二部"}} {
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
	for _, definition := range []Object{{"employee_no": "L2001", "name": "技术二级接口人A", "department": "技术二部"}, {"employee_no": "L2002", "name": "产品二级接口人B", "department": "产品二部"}, {"employee_no": "T3001", "name": "技术三级接口人A", "department": "技术平台组"}, {"employee_no": "T3002", "name": "算法三级接口人B", "department": "算法应用组"}, {"employee_no": "T3003", "name": "产品三级接口人C", "department": "产品运营组"}} {
		dep := departments[str(definition["department"])]
		level, role, group := "secondary", "secondary_contact", "二级接口人"
		if num(dep["level"]) == 3 {
			level, role, group = "tertiary", "tertiary_contact", "三级接口人"
		}
		contact, err := a.seedRecord(ctx, db, "core_contact", "employee_no", definition["employee_no"], Object{"employee_no": definition["employee_no"], "name": definition["name"], "email": normalized(str(definition["employee_no"])) + "@example.com", "department_id": dep["id"], "contact_level": level, "is_active": true})
		if err != nil {
			return err
		}
		if err = a.seedUser(ctx, db, str(definition["employee_no"]), role, group, contact); err != nil {
			return err
		}
	}
	for _, entry := range []Object{{"username": "admin", "role": "admin", "group": "管理员"}, {"username": "hr", "role": "hr", "group": "HR"}} {
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
