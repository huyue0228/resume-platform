package platform

import (
	"context"
	"net/http"
)

// A request-local read model for filtering. It contains no analysis snapshots,
// evidence or handling history. The existing serializer still owns current
// application selection, status precedence and department visibility.
type candidateReadModel struct {
	workflows       map[int64]Object
	resumes         map[int64][]Object
	attempts        map[int64][]Object
	members         map[int64][]Object
	items           map[int64]Object
	tags            map[int64][]Object
	related         map[string]map[int64]Object
	recommendations map[int64]bool
}

type candidateSummaryKey struct{}

func candidateSummaries(ctx context.Context) *candidateReadModel {
	data, _ := ctx.Value(candidateSummaryKey{}).(*candidateReadModel)
	return data
}

func (a *App) loadCandidateReadModel(ctx context.Context, r *http.Request) (*candidateReadModel, error) {
	data := &candidateReadModel{
		workflows: map[int64]Object{}, resumes: map[int64][]Object{}, attempts: map[int64][]Object{},
		members: map[int64][]Object{}, items: map[int64]Object{}, tags: map[int64][]Object{},
		related: map[string]map[int64]Object{},
	}
	queries := []struct {
		sql string
		add func(Object)
	}{
		{`SELECT row_to_json(w) FROM core_candidateworkflow w`, func(v Object) { data.workflows[num(v["candidate_id"])] = v }},
		{`SELECT row_to_json(v) FROM (SELECT r.*,COALESCE(a.status,'pending') lifecycle_status,COALESCE(a.reason,'') lifecycle_reason,a.completed_at lifecycle_completed_at FROM core_resume r LEFT JOIN platform_applications a ON a.resume_id=r.id ORDER BY r.volunteer_rank NULLS LAST,r.apply_date NULLS LAST,r.id) v`, func(v Object) {
			id := num(v["candidate_id"])
			data.resumes[id] = append(data.resumes[id], v)
		}},
		{`SELECT row_to_json(v) FROM (SELECT id,workflow_id,resume_id,attempt_no,status,source,current_department_id,assigned_screener_id,feedback_reason_code,feedback_reason_label_snapshot,feedback_note,manual_reason,match_reason FROM core_assignmentattempt ORDER BY attempt_no,id) v`, func(v Object) {
			id := num(v["workflow_id"])
			data.attempts[id] = append(data.attempts[id], v)
		}},
		{`SELECT row_to_json(v) FROM (SELECT DISTINCT ON (candidate_id,resume_id) id,candidate_id,resume_id,status FROM platform_pool_memberships WHERE status IN ('pending_review','pending_allocation','allocated','needs_reanalysis') ORDER BY candidate_id,resume_id,id DESC) v`, func(v Object) {
			id := num(v["candidate_id"])
			data.members[id] = append(data.members[id], v)
		}},
		{`SELECT row_to_json(v) FROM (SELECT l.candidate_id,t.id,t.code,t.name FROM core_candidate_school_tags l JOIN core_schooltag t ON t.id=l.schooltag_id ORDER BY t.code,t.id) v`, func(v Object) {
			id := num(v["candidate_id"])
			data.tags[id] = append(data.tags[id], v)
		}},
	}
	for _, query := range queries {
		values, err := rows(ctx, a.Pool, query.sql)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			query.add(value)
		}
	}
	// Only these reference fields are consumed by candidate summaries and RBAC.
	for _, query := range []struct{ table, sql string }{
		{"core_job", `SELECT row_to_json(v) FROM (SELECT id,department_id FROM core_job) v`},
		{"core_department", `SELECT row_to_json(v) FROM (SELECT id,name,level,parent_id FROM core_department) v`},
		{"core_schooltag", `SELECT row_to_json(v) FROM (SELECT id,code,name FROM core_schooltag) v`},
	} {
		values, err := rows(ctx, a.Pool, query.sql)
		if err != nil {
			return nil, err
		}
		data.related[query.table] = map[int64]Object{}
		for _, value := range values {
			data.related[query.table][num(value["id"])] = value
		}
	}
	itemSQL := `SELECT row_to_json(v) FROM (SELECT DISTINCT ON (candidate_id) candidate_id,reason_code,result_type,status FROM core_processingrunscopeitem ORDER BY candidate_id,created_at DESC,id DESC) v`
	var args []any
	if runID := r.URL.Query().Get("processing_run_id"); runID != "" {
		itemSQL = `SELECT row_to_json(v) FROM (SELECT candidate_id,reason_code,result_type,status FROM core_processingrunscopeitem WHERE run_id=$1) v`
		args = []any{num(runID)}
	}
	items, err := rows(ctx, a.Pool, itemSQL, args...)
	if err != nil {
		return nil, err
	}
	for _, value := range items {
		data.items[num(value["candidate_id"])] = value
	}
	if result := r.URL.Query().Get("processing_result"); r.URL.Query().Get("processing_run_id") != "" && contains([]string{"review", "dispatch", "archive"}, result) {
		values, err := rows(ctx, a.Pool, `SELECT row_to_json(v) FROM (SELECT DISTINCT w.candidate_id FROM core_agentdispatchdecision d JOIN core_candidateworkflow w ON w.id=d.workflow_id WHERE d.processing_run_id=$1 AND d.recommendation=$2) v`, num(r.URL.Query().Get("processing_run_id")), result)
		if err != nil {
			return nil, err
		}
		data.recommendations = map[int64]bool{}
		for _, value := range values {
			data.recommendations[num(value["candidate_id"])] = true
		}
	}
	return data, nil
}

func (a *App) pagedCandidates(w http.ResponseWriter, r *http.Request, p *Principal) error {
	ctx := context.WithValue(r.Context(), candidateSummaryKey{}, true)
	values, err := a.filtered(ctx, "candidates", r, p)
	if err != nil {
		return err
	}
	page, size, err := pageBounds(r, len(values))
	if err != nil {
		return err
	}
	start := min((page-1)*size, len(values))
	result := []Object{}
	for _, summary := range values[start:min(start+size, len(values))] {
		row, err := a.get(r.Context(), a.Pool, "core_candidate", summary["id"])
		if err != nil {
			return err
		}
		value, err := a.serialize(r.Context(), "candidates", row, p, false)
		if err != nil {
			return err
		}
		if r.URL.Query().Get("processing_run_id") != "" {
			value["reason_code"], value["processing_result"] = summary["reason_code"], summary["processing_result"]
		}
		result = append(result, value)
	}
	writePage(w, r, result, len(values), page, size)
	return nil
}
