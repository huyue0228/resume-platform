package platform

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Route struct {
	Path    string
	Actions map[string]string
	View    string
}

func (a *App) Handler(static http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				a.Log.Error("request panic", "path", r.URL.Path)
				write(w, 500, Object{"detail": "操作失败，请稍后重试"})
			}
		}()
		// 容器探针通过回环地址访问，不受对外域名白名单影响。
		if r.URL.Path == "/healthz" {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if a.Pool.Ping(ctx) != nil || a.Redis.Ping(ctx).Err() != nil {
				write(w, 503, Object{"ok": false})
				return
			}
			write(w, 200, Object{"ok": true, "runtime": "go"})
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		allowed := false
		for _, entry := range strings.Split(env("DJANGO_ALLOWED_HOSTS", "*"), ",") {
			entry = strings.TrimSpace(strings.ToLower(entry))
			if entry == "*" || entry == host || strings.HasPrefix(entry, ".") && (host == strings.TrimPrefix(entry, ".") || strings.HasSuffix(host, entry)) {
				allowed = true
			}
		}
		if !allowed {
			write(w, 400, Object{"detail": "请求 Host 不在允许范围"})
			return
		}
		// 保持原 Token API 的跨域行为；OAuth 会话仅由同源页面完成。
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Origin") != "" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "accept, authorization, content-type, user-agent, x-csrftoken, x-requested-with")
			w.Header().Set("Access-Control-Allow-Methods", "DELETE, GET, OPTIONS, PATCH, POST, PUT")
			if r.Method == "OPTIONS" && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(200)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")

		if !strings.HasPrefix(r.URL.Path, "/api/") {
			static.ServeHTTP(w, r)
			return
		}
		if r.Method == "GET" && !strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(r.URL.Path, ".json") {
			http.Redirect(w, r, r.URL.Path+"/?"+r.URL.RawQuery, 301)
			return
		}
		if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), ".json") {
			copyReq := r.Clone(r.Context())
			copyURL := *r.URL
			copyReq.URL = &copyURL
			copyURL.Path = strings.TrimSuffix(strings.TrimSuffix(r.URL.Path, "/"), ".json") + "/"
			r = copyReq
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
		if strings.HasPrefix(path, "auth/w3/") {
			if err := a.oauth(w, r, strings.TrimPrefix(path, "auth/w3/")); err != nil {
				a.writeError(w, err)
			}
			return
		}
		if path == "auth/login" {
			write(w, 404, Object{"detail": "未找到"})
			return
		}
		if path == "analytics/usage/overview" && metricsAuthorized(r) {
			if err := a.analytics(w, r, path, nil); err != nil {
				a.writeError(w, err)
			}
			return
		}
		p, err := a.principal(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Token")
			a.writeError(w, err)
			return
		}
		if err := a.api(w, r, path, p); err != nil {
			a.writeError(w, err)
		}
	})
}
func (a *App) api(w http.ResponseWriter, r *http.Request, path string, p *Principal) error {
	if isAllocationPath(path) {
		return a.allocationAPI(w, r, path, p)
	}
	if strings.HasPrefix(path, "jobs/") && strings.HasSuffix(path, "/reception") {
		return a.demandReceptionAPI(w, r, path, p)
	}
	if path == "pipeline/schedules" || strings.HasPrefix(path, "pipeline/schedules/") {
		return a.schedulesAPI(w, r, path, p)
	}
	if path == "pipeline/config-check" {
		return a.processingConfigurationCheck(w, r, p)
	}
	if path == "position-pools/config/reprocess" {
		return a.reprocessConfiguration(w, r, p)
	}
	if path == "position-pools/config" {
		return a.poolConfigAPI(w, r, p)
	}
	if strings.HasPrefix(path, "position-pools/members") {
		return a.poolMembersAPI(w, r, path, p)
	}
	switch path {
	case "":
		if r.Method != "GET" && r.Method != "HEAD" {
			return &apiError{405, "请求方法不允许"}
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		root := Object{"configs": scheme + "://" + r.Host + "/api/configs/"}
		for resource := range a.Spec.Resources {
			root[resource] = scheme + "://" + r.Host + "/api/" + resource + "/"
		}
		write(w, 200, root)
		return nil
	case "me":
		if r.Method != "GET" {
			return &apiError{405, "请求方法不允许"}
		}
		write(w, 200, a.me(r.Context(), p))
		return nil
	case "auth/logout":
		if r.Method != "POST" {
			return &apiError{405, "请求方法不允许"}
		}
		_, err := a.Pool.Exec(r.Context(), "DELETE FROM authtoken_token WHERE user_id=$1", p.User["id"])
		if err != nil {
			return err
		}
		write(w, 200, Object{"detail": "已退出登录"})
		return nil
	case "permissions":
		if !p.has("settings.manage_permissions") {
			return &apiError{403, "无权执行该操作"}
		}
		write(w, 200, a.Spec.PermissionTree)
		return nil
	case "pipeline/run":
		if !p.has("pipeline.run") {
			return &apiError{403, "无处理权限"}
		}
		return a.submitRun(w, r, p)
	case "import":
		if !p.has("resume.import") {
			return &apiError{403, "无导入权限"}
		}
		return a.importFiles(w, r, p)
	}
	if strings.HasPrefix(path, "import/templates/") {
		if !p.has("resume.import") {
			return &apiError{403, "无导入权限"}
		}
		return a.importTemplate(w, r, strings.TrimPrefix(path, "import/templates/"))
	}
	if strings.HasPrefix(path, "ai-connection") {
		if !p.has("settings.manage_ai_connection") {
			return &apiError{403, "无模型连接管理权限"}
		}
		return a.modelSettings(w, r, path, p)
	}
	if strings.HasPrefix(path, "analytics/") {
		return a.analytics(w, r, path, p)
	}
	if path == "configs" || strings.HasPrefix(path, "configs/") {
		if !p.has("settings.manage_config") && !p.has("department.manage") {
			return &apiError{403, "无配置权限"}
		}
		return a.configSettings(w, r, strings.TrimPrefix(path, "configs"), p)
	}
	for _, route := range a.Spec.Routes {
		if len(route.Actions) == 0 || strings.Contains(route.Path, "format") {
			continue
		}
		pattern := strings.Replace(route.Path, "api/^", "^/api/", 1)
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		match := re.FindStringSubmatch(r.URL.Path)
		if match == nil {
			continue
		}
		method := strings.ToLower(r.Method)
		if method == "head" {
			method = "get"
		}
		if method == "options" {
			write(w, 200, Object{"name": route.View, "renders": []string{"application/json"}, "parses": []string{"application/json", "multipart/form-data"}})
			return nil
		}
		action, ok := route.Actions[method]
		if !ok {
			return &apiError{405, "请求方法不允许"}
		}
		resource := ""
		for name := range a.Spec.Resources {
			if strings.HasPrefix(path, name+"/") || path == name {
				if len(name) > len(resource) {
					resource = name
				}
			}
		}
		if resource == "" {
			continue
		}
		spec := a.Spec.Resources[resource]
		permission := spec.Permission
		if code, ok := spec.PermissionsByAction[action]; ok {
			permission = code
		}
		if !p.allowed(permission) {
			return &apiError{403, "无权执行该操作"}
		}
		var id any
		for i, name := range re.SubexpNames() {
			if name == "pk" {
				n, err := strconv.ParseInt(match[i], 10, 64)
				if err != nil || n < 1 {
					return &apiError{404, "未找到记录"}
				}
				id = n
			}
		}
		switch action {
		case "list", "retrieve":
			return a.readResource(w, r, resource, id, p)
		case "create", "update", "partial_update":
			return a.writeResource(w, r, resource, id, p)
		case "destroy":
			return a.deleteResource(w, r, resource, id, p)
		default:
			return a.resourceAction(w, r, resource, action, id, p)
		}
	}
	return &apiError{404, "未找到"}
}
func (a *App) readResource(w http.ResponseWriter, r *http.Request, resource string, id any, p *Principal) error {
	if resource == "pipeline/runs" && id == nil {
		return a.taskList(w, r, p, false)
	}
	if id != nil {
		row, err := a.get(r.Context(), a.Pool, a.Spec.Resources[resource].Table, id)
		if err != nil {
			return err
		}
		if resource == "workflow-attempts" && !a.visibleAttempt(r.Context(), p, row) {
			return &apiError{404, "未找到记录"}
		}
		value, err := a.serialize(r.Context(), resource, row, p, true)
		if err != nil {
			return err
		}
		write(w, 200, value)
		return nil
	}
	if handled, err := a.pagedResource(w, r, resource, p); handled {
		return err
	}
	values, err := a.filtered(r.Context(), resource, r, p)
	if err != nil {
		return err
	}
	return paginate(w, r, values)
}
func paginate(w http.ResponseWriter, r *http.Request, values []Object) error {
	page, size, err := pageBounds(r, len(values))
	if err != nil {
		return err
	}
	start := min((page-1)*size, len(values))
	writePage(w, r, values[start:min(start+size, len(values))], len(values), page, size)
	return nil
}
func queryValues(r *http.Request, key string) []string {
	out := []string{}
	for _, value := range r.URL.Query()[key] {
		for _, v := range strings.Split(value, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (a *App) filtered(ctx context.Context, resource string, r *http.Request, p *Principal) ([]Object, error) {
	var drillIDs map[int64]bool
	if resource == "candidates" {
		if err := validateCandidateFilters(r, p); err != nil {
			return nil, err
		}
		var err error
		drillIDs, err = a.drilldownIDs(ctx, r)
		if err != nil {
			return nil, err
		}
	}
	var raw []Object
	var err error
	raw, err = a.all(ctx, a.Pool, a.Spec.Resources[resource].Table)
	if err != nil {
		return nil, err
	}
	out := []Object{}
	for _, row := range raw {
		if resource == "departments" && !isJobDepartment(row["level"]) {
			continue
		}
		if drillIDs != nil && !drillIDs[num(row["id"])] {
			continue
		}
		if resource == "workflow-attempts" && !a.visibleAttempt(ctx, p, row) {
			continue
		}
		if resource == "jobs" && r.URL.Query().Get("is_active") == "" && !truth(row["is_active"]) {
			continue
		}
		if resource == "pipeline/runs" && r.URL.Query().Get("active") == "true" && !activeRun(str(row["status"])) {
			continue
		}
		value, err := a.serialize(ctx, resource, row, p, false)
		if err != nil {
			var e *apiError
			if errors.As(err, &e) && e.Status == 404 {
				continue
			}
			return nil, err
		}
		if resource == "candidates" {
			if !p.has("resume.view") {
				for _, key := range []string{"processing_run_id", "processing_result", "workflow_status", "reason_code"} {
					if r.URL.Query().Get(key) != "" {
						return nil, &apiError{403, "部门范围不支持该筛选条件"}
					}
				}
			}
			if runID := r.URL.Query().Get("processing_run_id"); runID != "" {
				item, _ := one(ctx, a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE run_id=$1 AND candidate_id=$2", num(runID), row["id"])
				if item == nil {
					continue
				}
				value["reason_code"] = str(item["reason_code"])
				value["processing_result"] = str(item["result_type"])
				result := r.URL.Query().Get("processing_result")
				if result == "success" {
					result = "completed"
				}
				if result == "skipped" {
					if str(item["status"]) != "skipped_manual_change" {
						continue
					}
				} else if contains([]string{"review", "dispatch", "archive"}, result) {
					var exists bool
					err := a.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_agentdispatchdecision d JOIN core_candidateworkflow w ON d.workflow_id=w.id WHERE d.processing_run_id=$1 AND w.candidate_id=$2 AND d.recommendation=$3)", num(runID), row["id"], result).Scan(&exists)
					if err != nil {
						return nil, err
					}
					if !exists {
						continue
					}
				} else if result != "" && str(item["result_type"]) != result {
					continue
				}
			}
		}
		matched := true
		for key, vs := range r.URL.Query() {
			if len(vs) == 0 {
				continue
			}
			q := vs[len(vs)-1]
			if q == "" {
				continue
			}
			if strings.HasPrefix(key, "analytics_") {
				continue
			}
			if resource == "candidates" {
				ok, handled, err := a.candidateFilter(ctx, r, p, row, value, key, q)
				if err != nil {
					return nil, err
				}
				if handled {
					if !ok {
						matched = false
					}
					continue
				}
			}
			if contains([]string{"page", "page_size", "ordering", "active", "processing_run_id", "processing_result", "fields", "include_resume_files"}, key) {
				continue
			}
			if key == "search" {
				haystack := strings.ToLower(str(value["name"]) + " " + str(value["candidate_name"]) + " " + str(value["position_name"]) + " " + str(value["username"]))
				if p.has("resume.view") {
					haystack += " " + str(value["phone"])
				}
				if !textMatches(haystack, q) {
					matched = false
				}
				continue
			}
			field := strings.TrimSuffix(key, "_in")
			if key == "system_statuses" {
				field = "system_status"
			}
			if key == "ids" {
				field = "id"
			}
			actual, known := value[field]
			if strings.HasPrefix(field, "current_") {
				if v, ok := obj(value["current_resume"])[strings.TrimPrefix(field, "current_")]; ok {
					actual = v
					known = true
				}
			}
			if field == "attempt_status" {
				actual = obj(value["current_attempt"])["status"]
				known = true
			}
			if strings.HasPrefix(key, "imported_") {
				date := str(row["imported_at"])
				if len(date) >= 10 {
					date = date[:10]
				}
				if key == "imported_after" && date < q || key == "imported_before" && date > q {
					matched = false
				}
				continue
			}
			if !known {
				if f, ok := fieldFor(a, a.Spec.Resources[resource].Table, field); ok {
					actual = row[f.Column]
					known = true
				}
			}
			if !known {
				continue
			}
			if strings.HasSuffix(key, "_in") || key == "ids" || key == "system_statuses" || contains([]string{"allocation_source", "reason_code", "system_status", "workflow_status", "attempt_status"}, key) {
				if !contains(queryValues(r, key), str(actual)) {
					matched = false
				}
			} else if _, ok := actual.(bool); ok {
				if str(actual) != q {
					matched = false
				}
			} else if f, ok := fieldFor(a, a.Spec.Resources[resource].Table, field); ok && f.Relation != "" {
				if str(actual) != q {
					matched = false
				}
			} else if strings.HasSuffix(field, "_id") || field == "id" || field == "level" || field == "headcount" {
				if str(actual) != q {
					matched = false
				}
			} else if !textMatches(str(actual), q) {
				matched = false
			}
		}
		if matched {
			out = append(out, value)
		}
	}
	sortRows := map[int64]Object{}
	for _, row := range raw {
		if resource == "departments" && !isJobDepartment(row["level"]) {
			continue
		}
		sortRows[num(row["id"])] = row
	}
	ordering := r.URL.Query().Get("ordering")
	if ordering == "" {
		ordering = defaultOrdering(resource)
	}
	if ordering == "" {
		ordering = "id"
	}
	keys := strings.Split(ordering, ",")
	sort.SliceStable(out, func(i, j int) bool {
		for _, key := range keys {
			desc := strings.HasPrefix(key, "-")
			key = strings.TrimPrefix(key, "-")
			x, y := out[i][key], out[j][key]
			if x == nil {
				x = sortRows[num(out[i]["id"])][key]
			}
			if y == nil {
				y = sortRows[num(out[j]["id"])][key]
			}
			if str(x) == str(y) {
				continue
			}
			if key == "id" || key == "priority" || key == "sort_order" || key == "_category_sort_order" || key == "headcount" || key == "level" {
				if desc {
					return num(x) > num(y)
				}
				return num(x) < num(y)
			}
			if desc {
				return str(x) > str(y)
			}
			return str(x) < str(y)
		}
		return false
	})
	return out, nil
}
