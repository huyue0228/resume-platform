package platform

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func defaultOrdering(resource string) string {
	order := map[string]string{"candidates": "-updated_at", "resumes": "-imported_at", "workflows": "-updated_at", "workflow-attempts": "-created_at", "agent-decisions": "-created_at", "pipeline/runs": "-created_at", "schools": "name", "school-tags": "code,id", "school-tag-rules": "priority,id", "major-categories": "sort_order,code,id", "major-aliases": "_category_sort_order,_category_code,name,id"}[resource]
	if order == "" {
		return "id"
	}
	return order
}

func pageBounds(r *http.Request, total int) (int, int, error) {
	size := int(num(r.URL.Query().Get("page_size")))
	if size <= 0 {
		size = 20
	}
	size = min(size, 500)
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		if raw == "last" {
			page = max(1, (total+size-1)/size)
		} else {
			var err error
			page, err = strconv.Atoi(raw)
			if err != nil {
				return 0, 0, &apiError{404, "无效页码"}
			}
		}
	}
	if page < 1 || page > 1 && page > (total+size-1)/size {
		return 0, 0, &apiError{404, "无效页码"}
	}
	return page, size, nil
}

func writePage(w http.ResponseWriter, r *http.Request, values []Object, total, page, size int) {
	next, previous := any(nil), any(nil)
	link := func(page int) string {
		u := *r.URL
		q := u.Query()
		if page == 1 {
			q.Del("page")
		} else {
			q.Set("page", strconv.Itoa(page))
		}
		u.RawQuery = q.Encode()
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return scheme + "://" + r.Host + u.String()
	}
	if page*size < total {
		next = link(page + 1)
	}
	if page > 1 {
		previous = link(page - 1)
	}
	write(w, 200, Object{"count": total, "next": next, "previous": previous, "results": values})
}

// 常用列表先在 PostgreSQL 中分页，再加载当前页的关联对象，避免为二十行页面展开整个简历库。
// 需要计算业务投影的筛选仍走相同的权限和筛选实现。
func (a *App) pagedResource(w http.ResponseWriter, r *http.Request, resource string, p *Principal) (bool, error) {
	for key, values := range r.URL.Query() {
		if len(values) > 0 && values[0] != "" && !contains([]string{"page", "page_size", "ordering", "format"}, key) {
			return false, nil
		}
	}
	if (resource == "candidates" || resource == "resumes" || resource == "workflows" || resource == "agent-decisions") && !p.has("resume.view") {
		return false, nil
	}
	if resource == "workflow-attempts" && !p.has("attempt.view_all") {
		return false, nil
	}
	table := a.Spec.Resources[resource].Table
	from := quote(table) + " t"
	if resource == "major-aliases" {
		from += " JOIN core_majorcategory mc ON mc.id=t.category_id"
	}
	order := r.URL.Query().Get("ordering")
	if order == "" {
		order = defaultOrdering(resource)
	}
	keys := []string{}
	for _, key := range strings.Split(order, ",") {
		direction := " ASC"
		if strings.HasPrefix(key, "-") {
			direction = " DESC"
		}
		key = strings.TrimPrefix(key, "-")
		if resource == "major-aliases" && (key == "_category_sort_order" || key == "_category_code") {
			column := "mc.sort_order"
			if key == "_category_code" {
				column = "mc.code"
			}
			keys = append(keys, column+direction)
			continue
		}
		field, ok := fieldFor(a, table, key)
		if !ok {
			return false, nil
		}
		keys = append(keys, `t."`+field.Column+`"`+direction)
	}
	keys = append(keys, "t.id ASC")
	where := ""
	if resource == "jobs" {
		where = " WHERE t.is_active"
	}
	ctx := r.Context()
	var total int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM `+from+where).Scan(&total); err != nil {
		return true, err
	}
	page, size, err := pageBounds(r, total)
	if err != nil {
		return true, err
	}
	records, err := rows(ctx, a.Pool, fmt.Sprintf(`SELECT row_to_json(t) FROM %s%s ORDER BY %s LIMIT $1 OFFSET $2`, from, where, strings.Join(keys, ",")), size, (page-1)*size)
	if err != nil {
		return true, err
	}
	values := []Object{}
	for _, row := range records {
		value, err := a.serialize(ctx, resource, row, p, false)
		if err != nil {
			return true, err
		}
		values = append(values, value)
	}
	writePage(w, r, values, total, page, size)
	return true, nil
}
