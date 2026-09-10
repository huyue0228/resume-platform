package platform

import "context"

func isJobDepartment(level any) bool {
	return num(level) == 1 || num(level) == 2
}

func departmentContactLevel(_ Object) string { return "secondary" }

func contactIncludesDescendants(level any) bool {
	return level == "secondary" || level == "secondary_hr"
}
func validContactLevel(level string) bool {
	return contains([]string{"secondary", "tertiary", "secondary_hr"}, level)
}
func contactRole(level string) (string, string) {
	if level == "secondary_hr" {
		return level, "二级部门HR"
	}
	return level + "_contact", map[string]string{"secondary": "接口人", "tertiary": "简历筛选人"}[level]
}

func (a *App) departmentInScope(ctx context.Context, db DB, department Object, rootID any) bool {
	visited := map[int64]bool{}
	for department != nil && !visited[num(department["id"])] {
		id := num(department["id"])
		if id == num(rootID) {
			return true
		}
		visited[id] = true
		department, _ = a.get(ctx, db, "core_department", department["parent_id"])
	}
	return false
}
