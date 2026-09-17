package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/mozillazg/go-pinyin"
	"math"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func normalized(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}
func pinyinNames(name string) (string, string) {
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	full, initials := "", ""
	for _, v := range pinyin.LazyPinyin(name, args) {
		full += v
		if len(v) > 0 {
			initials += v[:1]
		}
	}
	return full, initials
}
func textMatches(value, query string) bool {
	v, q := strings.ToLower(value), strings.ToLower(strings.TrimSpace(query))
	if strings.Contains(v, q) {
		return true
	}
	full, initials := pinyinNames(v)
	return strings.Contains(full, q) || strings.Contains(initials, q)
}
func identity(name, phone string) string {
	hash := sha256.Sum256([]byte(strings.Join(strings.Fields(name), "") + "|" + normalizedPhone(phone)))
	return hex.EncodeToString(hash[:])
}
func normalizedPhone(phone string) string {
	digits := strings.Map(func(r rune) rune {
		if r < '0' || r > '9' {
			return -1
		}
		return r
	}, phone)
	if strings.HasPrefix(digits, "86") && len(digits) > 11 {
		digits = digits[2:]
	}
	return digits
}

func (a *App) writeResource(w http.ResponseWriter, r *http.Request, resource string, id any, p *Principal) error {
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	spec := a.Spec.Resources[resource]
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if contains([]string{"candidates", "resumes", "jobs", "departments"}, resource) {
		if err = a.lockAllAllocationScopes(ctx, tx); err != nil {
			return err
		}
	}
	if resource == "contacts" || resource == "users" {
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(72460910)"); err != nil {
			return err
		}
	}
	current := Object{}
	if id != nil {
		current, err = a.get(ctx, tx, spec.Table, id)
		if err != nil {
			return err
		}
	}
	if resource == "users" {
		if _, ok := body["password"]; ok {
			return &apiError{400, Object{"password": "系统账号不接受密码，请使用 W3 登录"}}
		}
		if str(current["username"]) == "012358" {
			return bad("内置管理员不允许修改")
		}
		if name, provided := body["username"]; id != nil && provided && name != current["username"] {
			var bound bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_contact WHERE employee_no=$1)", current["username"]).Scan(&bound); err != nil {
				return err
			}
			if bound {
				return bad("账号已有部门授权，不可修改工号")
			}
		}
	}
	values := Object{}
	for name, value := range body {
		field, ok := spec.Fields[name]
		if !ok || field.ReadOnly {
			continue
		}
		if field.Source == "*" {
			continue
		}
		values[field.Source] = value
	}
	for name, field := range spec.Fields {
		if field.ReadOnly {
			continue
		}
		if id == nil && field.Required {
			if _, ok := body[name]; !ok {
				return &apiError{400, Object{name: "该字段为必填项"}}
			}
		}
	}
	// DRF 同样对布尔、整数及关联 ID 做类型校验；禁止小数 ID 被截断后指向其他记录。
	for name, value := range values {
		f, ok := fieldFor(a, spec.Table, name)
		if !ok || value == nil {
			continue
		}
		if strings.Contains(f.Type, "Integer") || f.Relation != "" {
			n, err := strconv.ParseFloat(str(value), 64)
			if _, ok := value.(bool); ok || err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n >= float64(math.MaxInt64) || n < float64(math.MinInt64) {
				return &apiError{400, Object{name: "请输入有效整数"}}
			}
			values[name] = int64(n)
		} else if f.Type == "BooleanField" {
			if _, ok := value.(bool); !ok {
				switch strings.ToLower(str(value)) {
				case "true", "1":
					values[name] = true
				case "false", "0":
					values[name] = false
				default:
					return &apiError{400, Object{name: "请输入有效布尔值"}}
				}
			}
		} else if f.Type == "CharField" || f.Type == "TextField" || f.Type == "EmailField" {
			switch value.(type) {
			case string, float64:
				values[name] = str(value)
			default:
				return &apiError{400, Object{name: "请输入有效文本"}}
			}
		}
	}
	merged := clone(current)
	for key, value := range values {
		if f, ok := fieldFor(a, spec.Table, key); ok {
			merged[f.Column] = value
		}
	}
	for name, value := range values {
		if f, ok := fieldFor(a, spec.Table, name); ok {
			if value == nil && !f.Null {
				return &apiError{400, Object{name: "该字段不可为空"}}
			}
			if s, ok := value.(string); ok {
				if f.MaxLength != nil && utf8.RuneCountInString(s) > *f.MaxLength {
					return &apiError{400, Object{name: "超出字段长度限制"}}
				}
				if !f.Blank && strings.TrimSpace(s) == "" {
					return &apiError{400, Object{name: "该字段不能为空"}}
				}
			}
			if len(f.Choices) > 0 && value != nil && str(value) != "" {
				valid := false
				for _, c := range f.Choices {
					if str(c[0]) == str(value) {
						valid = true
					}
				}
				if !valid {
					return &apiError{400, Object{name: "无效的选项"}}
				}
			}
			if f.Relation != "" && value != nil {
				if _, err := a.get(ctx, tx, f.Relation, num(value)); err != nil {
					return &apiError{400, Object{name: "关联记录不存在"}}
				}
			}
		}
	}
	if resource == "contacts" || resource == "users" {
		email := strings.ToLower(strings.TrimSpace(str(merged["email"])))
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email {
			return &apiError{400, Object{"email": "请输入有效邮箱"}}
		}
		values["email"] = email
		var duplicate bool
		if resource == "contacts" {
			err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_contact WHERE lower(email)=$1 AND employee_no<>$2) OR EXISTS(SELECT 1 FROM accounts_user WHERE lower(email)=$1 AND username<>$2)", email, merged["employee_no"]).Scan(&duplicate)
		} else {
			err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM accounts_user WHERE lower(email)=$1 AND ($2::bigint IS NULL OR id<>$2)) OR EXISTS(SELECT 1 FROM core_contact WHERE lower(email)=$1 AND employee_no<>$3)", email, id, merged["username"]).Scan(&duplicate)
		}
		if err != nil {
			return err
		}
		if duplicate {
			return &apiError{400, Object{"email": "该邮箱已被其他账号使用"}}
		}
	}
	switch resource {
	case "roles":
		if _, builtin := a.Spec.RolePermissions[str(current["name"])]; builtin && merged["name"] != current["name"] {
			return bad("内置角色名称用于权限范围识别，不可改名")
		}
	case "departments":
		level := num(merged["level"])
		if level == 0 {
			level = 2
		}
		if level < 1 || level > 2 {
			return bad("部门层级必须为 1、2")
		}
		parentID := merged["parent_id"]
		if level == 1 {
			if parentID != nil {
				return bad("一级部门不能有父部门")
			}
		} else {
			parent, err := a.get(ctx, tx, "core_department", parentID)
			if err != nil || num(parent["level"]) != level-1 {
				return bad("部门必须归属于相邻的上一级部门")
			}
			if id != nil && num(parentID) == num(id) {
				return bad("部门层级不能形成循环")
			}
		}
		if id != nil && (level != num(current["level"]) || num(parentID) != num(current["parent_id"])) {
			for table, model := range a.Spec.Models {
				for _, field := range model.Fields {
					if field.Relation == "core_department" {
						var used bool
						err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+quote(table)+" WHERE "+quote(field.Column)+"=$1)", id).Scan(&used)
						if err != nil {
							return err
						}
						if used {
							return bad("部门存在下级部门或业务引用，不可修改层级或父部门")
						}
					}
				}
			}
		}
	case "contacts":
		if id != nil && str(merged["employee_no"]) != str(current["employee_no"]) {
			return bad("工号标识账号身份，不可通过部门授权修改")
		}
		if str(merged["employee_no"]) == "012358" || str(values["email"]) == "huyue2@ueascend.com" {
			return bad("该工号或邮箱属于内置管理员，不允许绑定接口人")
		}
		dep, err := a.get(ctx, tx, "core_department", merged["department_id"])
		if err != nil || !isJobDepartment(dep["level"]) {
			return bad("接口人必须绑定有效部门")
		}
		level := str(merged["contact_level"])
		if level == "" {
			level = departmentContactLevel(dep)
		}
		values["contact_level"] = level
		if !validContactLevel(level) {
			return bad("部门角色无效")
		}
		if !contactRoleMatchesDepartment(level, dep) {
			return bad("部门HR角色必须匹配授权部门层级")
		}
		if level == "tertiary" {
			values["can_delegate"] = false
		}
	case "jobs":
		dep, err := a.get(ctx, tx, "core_department", merged["department_id"])
		if err != nil || !isJobDepartment(dep["level"]) {
			return &apiError{400, Object{"department": "岗位必须绑定一级或二级部门"}}
		}
		if strings.TrimSpace(str(merged["responsibilities"])) == "" {
			return &apiError{400, Object{"responsibilities": "工作职责不能为空"}}
		}
		if num(merged["headcount"]) < 0 {
			return bad("岗位 HC 不能为负数")
		}
	case "candidates":
		phone := normalizedPhone(str(merged["phone"]))
		if phone == "" {
			return bad("手机号不能为空")
		}
		values["phone"] = phone
		values["identity_hash"] = identity(str(merged["name"]), phone)
	case "school-tag-rules":
		enabled := true
		if v, ok := merged["is_active"]; ok {
			enabled = truth(v)
		}
		if enabled {
			for _, degree := range []string{"first", "highest"} {
				ids, provided := body[degree+"_degree_tag_ids"]
				if provided {
					if len(list(ids)) == 0 {
						return &apiError{400, Object{degree + "_degree_tag_ids": "启用规则至少需要一个院校标签"}}
					}
				} else if id == nil {
					return bad("启用规则需要第一学历和最高学历标签")
				}
			}
		}
	}
	if name, ok := values["name"]; ok {
		if _, exists := fieldFor(a, spec.Table, "name_pinyin"); exists {
			full, initials := pinyinNames(str(name))
			values["name_pinyin"] = full
			values["name_pinyin_initials"] = initials
		}
	}
	if resource == "users" {
		values["password"] = "!" + token(20)
		if id == nil {
			values["date_joined"] = now()
		}
	}
	saved, err := a.save(ctx, tx, spec.Table, id, values)
	if err != nil {
		return err
	}
	if err = a.syncRelations(ctx, tx, resource, saved, body); err != nil {
		return err
	}
	if resource == "jobs" {
		if err = a.syncJobPolicy(ctx, tx, p); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if resource == "schools" && strings.TrimSpace(str(saved["province"])) == "" {
		if _, err := a.enqueueSchoolEnrichment(ctx, []int64{num(saved["id"])}); err != nil {
			a.Log.Error("school enrichment enqueue failed")
		}
	}
	result, err := a.serialize(ctx, resource, saved, p, true)
	if err != nil {
		return err
	}
	status := 200
	if id == nil {
		status = 201
	}
	write(w, status, result)
	return nil
}
func (a *App) syncRelations(ctx context.Context, db DB, resource string, saved, body Object) error {
	switch resource {
	case "jobs":
		if raw, ok := body["major_names"]; ok {
			if _, err := db.Exec(ctx, "DELETE FROM core_jobmajor WHERE job_id=$1", saved["id"]); err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, v := range list(raw) {
				name := strings.TrimSpace(str(v))
				if name != "" && !seen[name] {
					seen[name] = true
					if _, err := a.save(ctx, db, "core_jobmajor", nil, Object{"job": saved["id"], "major": name}); err != nil {
						return err
					}
				}
			}
		}
	case "users":
		if _, err := db.Exec(ctx, "UPDATE core_contact SET email=$2 WHERE employee_no=$1", saved["username"], saved["email"]); err != nil {
			return err
		}
		if values, ok := body["role_ids"]; ok {
			if _, err := db.Exec(ctx, "DELETE FROM accounts_user_groups WHERE user_id=$1", saved["id"]); err != nil {
				return err
			}
			for _, id := range list(values) {
				if _, err := db.Exec(ctx, "INSERT INTO accounts_user_groups(user_id,group_id) VALUES($1,$2) ON CONFLICT DO NOTHING", saved["id"], num(id)); err != nil {
					return err
				}
			}
		}
	case "roles":
		if values, ok := body["permission_codes"]; ok {
			if _, err := db.Exec(ctx, "DELETE FROM auth_group_permissions WHERE group_id=$1", saved["id"]); err != nil {
				return err
			}
			known := map[string]bool{}
			for _, mod := range a.Spec.PermissionTree {
				for _, p := range list(mod["children"]) {
					known[str(obj(p)["code"])] = true
				}
			}
			for _, code := range list(values) {
				if !known[str(code)] {
					return bad("包含未知权限码")
				}
				if !a.rolePermissionAllowed(str(saved["name"]), str(code)) {
					return bad("该权限超出角色范围；系统配置仅管理员可用，全局业务权限由一级部门HR承担")
				}
				_, err := db.Exec(ctx, "INSERT INTO auth_group_permissions(group_id,permission_id) SELECT $1,p.id FROM auth_permission p JOIN django_content_type c ON c.id=p.content_type_id WHERE c.app_label='accounts' AND p.codename=$2 ON CONFLICT DO NOTHING", saved["id"], strings.ReplaceAll(str(code), ".", "__"))
				if err != nil {
					return err
				}
			}
		}
	case "school-tag-rules":
		for _, degree := range []string{"first", "highest"} {
			if values, ok := body[degree+"_degree_tag_ids"]; ok {
				if _, err := db.Exec(ctx, "DELETE FROM core_schooltagruletag WHERE rule_id=$1 AND degree_type=$2", saved["id"], degree); err != nil {
					return err
				}
				for _, id := range list(values) {
					if _, err := a.save(ctx, db, "core_schooltagruletag", nil, Object{"rule": saved["id"], "school_tag": num(id), "degree_type": degree}); err != nil {
						return err
					}
				}
			}
		}
		if values, ok := body["allowed_highest_educations"]; ok {
			if _, err := db.Exec(ctx, "DELETE FROM core_schooltagruleeducation WHERE rule_id=$1", saved["id"]); err != nil {
				return err
			}
			for _, v := range list(values) {
				if !contains([]string{"associate", "bachelor", "master", "doctor"}, str(v)) {
					return bad("学历选项无效")
				}
				if _, err := a.save(ctx, db, "core_schooltagruleeducation", nil, Object{"rule": saved["id"], "education": v}); err != nil {
					return err
				}
			}
		}
	case "contacts":
		return a.syncContactUser(ctx, db, saved)
	}
	return nil
}
func (a *App) deleteResource(w http.ResponseWriter, r *http.Request, resource string, id any, p *Principal) error {
	ctx := r.Context()
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if contains([]string{"candidates", "resumes", "jobs", "departments"}, resource) {
		if err = a.lockAllAllocationScopes(ctx, tx); err != nil {
			return err
		}
	}
	if resource == "contacts" || resource == "users" {
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(72460910)"); err != nil {
			return err
		}
	}
	table := a.Spec.Resources[resource].Table
	row, err := a.get(ctx, tx, table, id)
	if err != nil {
		return err
	}
	if resource == "jobs" {
		_, err = a.save(ctx, tx, table, id, Object{"is_active": false})
	} else if resource == "candidates" {
		err = a.deleteCandidate(ctx, tx, id)
	} else {
		if resource == "users" && str(row["username"]) == "012358" {
			return bad("内置管理员不可删除")
		}
		if resource == "users" {
			grants, err := rows(ctx, tx, "SELECT row_to_json(c) FROM core_contact c WHERE employee_no=$1 OR id=$2", row["username"], row["contact_id"])
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "UPDATE accounts_user SET contact_id=NULL WHERE id=$1", id); err != nil {
				return err
			}
			for _, grant := range grants {
				if err = a.deleteRow(ctx, tx, "core_contact", grant["id"], map[string]bool{}); err != nil {
					return err
				}
			}
		}
		err = a.deleteRow(ctx, tx, table, id, map[string]bool{})
		if err == nil && resource == "contacts" {
			err = a.syncContactUser(ctx, tx, row)
		}
	}
	if err != nil {
		return err
	}
	if resource == "jobs" {
		if err = a.syncJobPolicy(ctx, tx, p); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	write(w, 204, nil)
	return nil
}
func (a *App) deleteCandidate(ctx context.Context, db DB, id any) error {
	if _, err := a.lockWorkflow(ctx, db, id); err != nil {
		return err
	}
	var protected bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core_assignmentattempt a JOIN core_candidateworkflow w ON w.id=a.workflow_id WHERE w.candidate_id=$1) OR EXISTS(SELECT 1 FROM core_agentdispatchdecision d JOIN core_resume r ON r.id=d.resume_id WHERE r.candidate_id=$1) OR EXISTS(SELECT 1 FROM core_processingrunscopeitem WHERE candidate_id=$1 AND status IN ('pending','queued','processing','waiting_conflict'))`, id).Scan(&protected)
	if err != nil {
		return err
	}
	if protected {
		return &apiError{409, "无法删除候选人：存在正在处理的任务或受保护的分配、决策和反馈历史"}
	}
	return a.deleteRow(ctx, db, "core_candidate", id, map[string]bool{})
}
func (a *App) deleteRow(ctx context.Context, db DB, table string, id any, seen map[string]bool) error {
	key := table + ":" + str(id)
	if seen[key] {
		return nil
	}
	seen[key] = true
	for other, model := range a.Spec.Models {
		for _, field := range model.Fields {
			if field.Relation != table {
				continue
			}
			items, err := rows(ctx, db, "SELECT row_to_json(t) FROM "+quote(other)+" t WHERE "+quote(field.Column)+"=$1", id)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				continue
			}
			switch field.OnDelete {
			case "PROTECT", "RESTRICT":
				return bad("数据存在业务引用，不可删除")
			case "SET_NULL":
				if _, err = db.Exec(ctx, "UPDATE "+quote(other)+" SET "+quote(field.Column)+"=NULL WHERE "+quote(field.Column)+"=$1", id); err != nil {
					return err
				}
			default:
				for _, item := range items {
					if err = a.deleteRow(ctx, db, other, item[a.primaryColumn(other)], seen); err != nil {
						return err
					}
				}
			}
		}
	}
	_, err := db.Exec(ctx, "DELETE FROM "+quote(table)+" WHERE "+quote(a.primaryColumn(table))+"=$1", id)
	return err
}
