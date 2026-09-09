package platform

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

func localTime(value any) string {
	t, err := time.Parse(time.RFC3339Nano, str(value))
	if err != nil {
		return ""
	}
	return t.In(shanghai).Format("2006-01-02 15:04:05")
}
func hours(start, end any) any {
	s, e := parseTime(start), parseTime(end)
	if s.IsZero() || e.IsZero() || e.Before(s) {
		return nil
	}
	return math.Round(e.Sub(s).Hours()*100) / 100
}
func parseTime(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t
	}
	t, _ := time.Parse(time.RFC3339Nano, str(v))
	return t
}
func safeCell(v any) any {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		trim := strings.TrimSpace(s)
		if len(trim) > 0 && strings.ContainsRune("=+-@", rune(trim[0])) {
			return "'" + s
		}
	}
	return v
}
func sheet(book *excelize.File, name string, headers []string, values [][]any) error {
	if book.GetSheetName(0) == "Sheet1" {
		if err := book.SetSheetName("Sheet1", name); err != nil {
			return err
		}
	} else if _, err := book.NewSheet(name); err != nil {
		return err
	}
	head := []any{}
	for _, h := range headers {
		head = append(head, h)
	}
	if err := book.SetSheetRow(name, "A1", &head); err != nil {
		return err
	}
	for i, row := range values {
		for j, value := range row {
			row[j] = safeCell(value)
		}
		if err := book.SetSheetRow(name, fmt.Sprintf("A%d", i+2), &row); err != nil {
			return err
		}
	}
	end, _ := excelize.ColumnNumberToName(len(headers))
	style, err := book.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Color: []string{"D9EAF7"}, Pattern: 1}})
	if err != nil {
		return err
	}
	if err = book.SetCellStyle(name, "A1", end+"1", style); err != nil {
		return err
	}
	if err = book.SetPanes(name, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	if err = book.AutoFilter(name, fmt.Sprintf("A1:%s%d", end, len(values)+1), nil); err != nil {
		return err
	}
	for i, h := range headers {
		col, _ := excelize.ColumnNumberToName(i + 1)
		width := float64(min(36, max(12, len([]rune(h))*2+4)))
		if err = book.SetColWidth(name, col, col, width); err != nil {
			return err
		}
	}
	return nil
}
func sendWorkbook(w http.ResponseWriter, book *excelize.File, filename string) error {
	buffer, err := book.WriteToBuffer()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\"export.xlsx\"; filename*=UTF-8''"+url.PathEscape(filename))
	w.Header().Set("Cache-Control", "private, no-store")
	_, err = w.Write(buffer.Bytes())
	return err
}
func (a *App) importTemplate(w http.ResponseWriter, r *http.Request, key string) error {
	if r.Method != "GET" {
		return &apiError{405, "请求方法不允许"}
	}
	schema, ok := a.Spec.ImportSchemas[key]
	if !ok {
		return &apiError{404, "未知导入模板类型"}
	}
	book := excelize.NewFile()
	defer book.Close()
	if err := sheet(book, schema.SheetName, schema.Headers, nil); err != nil {
		return err
	}
	if err := sheet(book, "填写说明", []string{"模板版本", "1"}, [][]any{{"模板类型", schema.Label}, {"填写要求", "请勿修改、删除、重复或新增第一行表头；无值字段保留列并留空。"}, {"数据位置", "请在首个工作表第二行起填写数据。"}}); err != nil {
		return err
	}
	for i, h := range schema.Headers {
		options := map[string][]string{"是否对外发布": {"是", "否"}, "性别": {"男", "女"}, "接口人层级": {"二级接口人", "三级接口人"}, "可转派": {"是", "否"}, "是否启用": {"是", "否"}}[h]
		if len(options) == 0 {
			continue
		}
		col, _ := excelize.ColumnNumberToName(i + 1)
		validation := excelize.NewDataValidation(true)
		validation.SetSqref(col + "2:" + col + "5000")
		if err := validation.SetDropList(options); err != nil {
			return err
		}
		if err := book.AddDataValidation(schema.SheetName, validation); err != nil {
			return err
		}
	}
	return sendWorkbook(w, book, schema.Filename)
}
func (a *App) choiceLabel(table, field string, value any) string {
	f, _ := fieldFor(a, table, field)
	for _, choice := range f.Choices {
		if str(choice[0]) == str(value) {
			return str(choice[1])
		}
	}
	return str(value)
}
func (a *App) attemptTimings(ctx context.Context, at Object) (Object, error) {
	result := Object{}
	for _, k := range []string{"first_dispatched_at", "current_department_entered_at", "feedback_at", "hr_dispatch_duration_hours", "current_department_duration_hours", "total_feedback_duration_hours"} {
		result[k] = ""
	}
	if at == nil {
		return result, nil
	}
	events, err := rows(ctx, a.Pool, "SELECT row_to_json(e) FROM core_assignmenthandlingevent e WHERE attempt_id=$1 ORDER BY occurred_at,id", at["id"])
	if err != nil {
		return nil, err
	}
	var first, entered, feedback any
	cancelled := false
	for _, e := range events {
		switch str(e["event_type"]) {
		case "department_dispatched":
			if first == nil {
				first = e["occurred_at"]
			}
		case "feedback_passed", "feedback_rejected":
			feedback = e["occurred_at"]
		case "cancelled":
			cancelled = true
		}
		if contains([]string{"department_dispatched", "department_transferred"}, str(e["event_type"])) && num(e["to_department_id"]) == num(at["current_department_id"]) {
			entered = e["occurred_at"]
		}
	}
	end := feedback
	if end == nil && !cancelled {
		end = time.Now()
	}
	result["first_dispatched_at"] = localTime(first)
	result["current_department_entered_at"] = localTime(entered)
	result["feedback_at"] = localTime(feedback)
	result["hr_dispatch_duration_hours"] = hours(at["created_at"], first)
	result["current_department_duration_hours"] = hours(entered, end)
	result["total_feedback_duration_hours"] = hours(first, feedback)
	return result, nil
}

type exportRecord struct {
	Candidate, Resume, Attempt, View Object
	Files                            []Object
}

func (a *App) exportValues(ctx context.Context, record exportRecord) (Object, error) {
	c, resume, at, view := record.Candidate, record.Resume, record.Attempt, record.View
	job, _ := a.get(ctx, a.Pool, "core_job", resume["job_id"])
	initial, _ := a.get(ctx, a.Pool, "core_department", at["initial_department_id"])
	current, _ := a.get(ctx, a.Pool, "core_department", at["current_department_id"])
	primary, secondary, _ := a.hierarchy(ctx, current)
	workflow, _ := one(ctx, a.Pool, "SELECT row_to_json(w) FROM core_candidateworkflow w WHERE candidate_id=$1", c["id"])
	values := clone(c)
	values["candidate_name"] = c["name"]
	values["candidate_phone"] = c["phone"]
	values["highest_education"] = a.choiceLabel("core_candidate", "highest_education", c["highest_education"])
	values["candidate_imported_at"] = localTime(c["imported_at"])
	for _, degree := range []string{"first", "highest"} {
		tag, _ := a.get(ctx, a.Pool, "core_schooltag", c[degree+"_degree_tag_id"])
		values[degree+"_degree_tag"] = str(tag["name"])
		if str(values[degree+"_degree_tag"]) == "" {
			values[degree+"_degree_tag"] = str(c[degree+"_degree_platform"])
		}
	}
	tags, err := a.candidateTags(ctx, c)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, t := range tags {
		names = append(names, str(t["name"]))
	}
	values["school_tags"] = strings.Join(names, "、")
	for _, k := range []string{"entity", "org", "apply_date", "volunteer_rank", "assigned_entity", "job_category", "category_mode", "category_reason"} {
		values[k] = resume[k]
	}
	values["current_apply_id"] = resume["apply_id"]
	values["current_position_name"] = resume["position_name"]
	values["original_status"] = resume["status"]
	values["resume_filename"] = str(resume["resume_file"])
	applies, files := []string{}, []string{}
	for _, r := range record.Files {
		applies = append(applies, str(r["apply_id"]))
		if str(r["resume_file"]) != "" {
			files = append(files, filepath.Base(str(r["resume_file"])))
		}
	}
	values["all_apply_ids"] = strings.Join(applies, "、")
	values["all_resume_filenames"] = strings.Join(files, "、")
	for k, source := range map[string]string{"job_public_name": "public_name", "job_position_name": "position_name", "job_family": "job_family", "job_location": "location", "education_requirement": "education", "responsibilities": "responsibilities", "headcount": "headcount"} {
		values[k] = job[source]
	}
	dep, _ := a.get(ctx, a.Pool, "core_department", job["department_id"])
	_, jobDep, _ := a.hierarchy(ctx, dep)
	values["job_secondary_department"] = jobDep["name"]
	majors, err := rows(ctx, a.Pool, "SELECT row_to_json(m) FROM core_jobmajor m WHERE job_id=$1 ORDER BY id", job["id"])
	if err != nil {
		return nil, err
	}
	names = []string{}
	for _, m := range majors {
		names = append(names, str(m["major"]))
	}
	values["required_majors"] = strings.Join(names, "、")
	values["is_public"] = ""
	if job != nil {
		values["is_public"] = "否"
		if truth(job["is_public"]) {
			values["is_public"] = "是"
		}
	}
	values["allocation_source"] = a.choiceLabel("core_assignmentattempt", "source", at["source"])
	if at == nil {
		values["allocation_source"] = a.choiceLabel("core_assignmentattempt", "source", view["allocation_source"])
	}
	values["attempt_status"] = a.choiceLabel("core_assignmentattempt", "status", at["status"])
	values["initial_department"] = initial["name"]
	values["current_primary_department"] = primary["name"]
	values["current_secondary_department"] = secondary["name"]
	values["current_department"] = current["name"]
	values["allocation_reason"] = at["manual_reason"]
	if str(values["allocation_reason"]) == "" {
		values["allocation_reason"] = at["match_reason"]
	}
	// 历史分配记录继续按普通业务导出，平台不再创建专项记录。
	if at["route_code"] == "ai_special_route" {
		values["allocation_reason"] = "AI 自动分配"
	}
	values["confidence_score"] = at["confidence_score"]
	values["feedback_result"] = a.choiceLabel("core_assignmentattempt", "feedback_result", at["feedback_result"])
	values["feedback_reason_code"] = at["feedback_reason_code"]
	values["feedback_reason"] = at["feedback_reason_label_snapshot"]
	if str(values["feedback_reason"]) == "" {
		values["feedback_reason"] = a.choiceLabel("core_assignmentattempt", "feedback_reason_code", at["feedback_reason_code"])
	}
	values["feedback_note"] = at["feedback_note"]
	values["resume_status"] = view["system_status_label"]
	values["reason_code"] = view["reason_code"]
	item, _ := one(ctx, a.Pool, "SELECT row_to_json(i) FROM core_processingrunscopeitem i WHERE candidate_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1", c["id"])
	values["reason_detail"] = item["result_message"]
	values["workflow_status"] = a.choiceLabel("core_candidateworkflow", "status", workflow["status"])
	values["archive_reason"] = a.choiceLabel("core_candidateworkflow", "archive_reason", workflow["archive_reason"])
	values["archive_detail"] = workflow["archive_detail"]
	timings, err := a.attemptTimings(ctx, at)
	if err != nil {
		return nil, err
	}
	for k, v := range timings {
		values[k] = v
	}
	return values, nil
}
func (a *App) export(w http.ResponseWriter, r *http.Request, resource, action string, p *Principal) error {
	if action == "result_report" {
		return a.resultReport(w, r, p)
	}
	values, err := a.filtered(r.Context(), resource, r, p)
	if err != nil {
		return err
	}
	book := excelize.NewFile()
	defer book.Close()
	if resource == "jobs" {
		if len(values) == 0 {
			return bad("当前筛选条件下没有可下载的启用岗位")
		}
		data := [][]any{}
		for _, j := range values {
			public := "否"
			if truth(j["is_public"]) {
				public = "是"
			}
			data = append(data, []any{j["entity"], j["primary_department_name"], j["secondary_department_name"], j["category"], j["public_name"], public, j["position_name"], j["job_family"], j["location"], j["education"], j["responsibilities"], strings.Join(stringValues(j["majors"]), "、"), j["headcount"]})
		}
		if err = sheet(book, "职位清单", a.Spec.ImportSchemas["jobs"].Headers, data); err != nil {
			return err
		}
		w.Header().Set("X-Export-Count", str(len(values)))
		return sendWorkbook(w, book, "职位清单.xlsx")
	}
	fieldKeys, headers := []string{}, []string{}
	requested, explicit := r.URL.Query()["fields"]
	wanted := []string{}
	if explicit {
		wanted = strings.Split(strings.Join(requested, ","), ",")
		if len(wanted) == 0 || strings.TrimSpace(strings.Join(wanted, "")) == "" {
			return bad("fields 不能为空")
		}
	}
	known := map[string]bool{}
	for _, g := range list(a.Spec.ExportFields["groups"]) {
		for _, v := range list(obj(g)["fields"]) {
			f := obj(v)
			key := str(f["key"])
			known[key] = true
			if (!explicit && truth(f["default_selected"])) || (explicit && contains(wanted, key)) {
				fieldKeys = append(fieldKeys, key)
				headers = append(headers, str(f["label"]))
			}
		}
	}
	for _, key := range wanted {
		if !known[key] {
			return bad("存在未知导出字段：" + key)
		}
	}
	records := []exportRecord{}
	byCandidate := map[int64]int{}
	for _, value := range values {
		var c, resume, at Object
		files := []Object{}
		if resource == "candidates" {
			c, err = a.get(r.Context(), a.Pool, "core_candidate", value["id"])
			if err != nil {
				return err
			}
			resume, _ = a.get(r.Context(), a.Pool, "core_resume", obj(value["current_resume"])["id"])
			at, _ = a.get(r.Context(), a.Pool, "core_assignmentattempt", obj(value["current_attempt"])["id"])
			if p.has("resume.view") {
				files, err = rows(r.Context(), a.Pool, "SELECT row_to_json(r) FROM core_resume r WHERE candidate_id=$1 ORDER BY volunteer_rank NULLS LAST,apply_date NULLS FIRST,id", c["id"])
				if err != nil {
					return err
				}
			} else if resume != nil {
				files = []Object{resume}
			}
		} else {
			at, err = a.get(r.Context(), a.Pool, "core_assignmentattempt", value["id"])
			if err != nil {
				return err
			}
			resume, err = a.get(r.Context(), a.Pool, "core_resume", at["resume_id"])
			if err != nil {
				return err
			}
			c, err = a.get(r.Context(), a.Pool, "core_candidate", resume["candidate_id"])
			if err != nil {
				return err
			}
			files = []Object{resume}
			value, err = a.serialize(r.Context(), "candidates", c, p, false)
			if err != nil {
				return err
			}
		}
		if index, ok := byCandidate[num(c["id"])]; ok {
			records[index].Files = append(records[index].Files, files...)
			if num(at["attempt_no"]) > num(records[index].Attempt["attempt_no"]) {
				records[index].Attempt = at
				records[index].Resume = resume
			}
			continue
		}
		byCandidate[num(c["id"])] = len(records)
		records = append(records, exportRecord{c, resume, at, value, files})
	}
	data := [][]any{}
	for _, record := range records {
		v, err := a.exportValues(r.Context(), record)
		if err != nil {
			return err
		}
		row := []any{}
		for _, key := range fieldKeys {
			row = append(row, v[key])
		}
		data = append(data, row)
	}
	if err = sheet(book, "简历库", headers, data); err != nil {
		return err
	}
	include := true
	if raw, ok := r.URL.Query()["include_resume_files"]; ok {
		switch strings.ToLower(strings.Join(raw, "")) {
		case "true", "1":
		case "false", "0":
			include = false
		default:
			return bad("include_resume_files 必须是 true 或 false")
		}
	}
	w.Header().Set("X-Export-Candidate-Count", str(len(records)))
	if !include {
		w.Header().Set("X-Export-Count", str(len(records)))
		return sendWorkbook(w, book, "简历库清单.xlsx")
	}
	archiveFile, err := os.CreateTemp("", "resume-export-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(archiveFile.Name())
	defer archiveFile.Close()
	archive := zip.NewWriter(archiveFile)
	xlsx, err := book.WriteToBuffer()
	if err != nil {
		return err
	}
	entry, err := archive.Create("简历库清单.xlsx")
	if err != nil {
		return err
	}
	if _, err = entry.Write(xlsx.Bytes()); err != nil {
		return err
	}
	if _, err = archive.Create("简历文件/"); err != nil {
		return err
	}
	missing := []string{}
	seen := map[int64]bool{}
	names := map[string]bool{}
	count := 0
	total := int64(0)
	for _, record := range records {
		for _, resume := range record.Files {
			if seen[num(resume["id"])] {
				continue
			}
			seen[num(resume["id"])] = true
			path, err := a.resumeFile(str(resume["resume_file"]))
			if err != nil {
				missing = append(missing, str(record.Candidate["name"])+"（"+str(resume["apply_id"])+"）")
				continue
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			info, err := file.Stat()
			if err != nil {
				file.Close()
				return err
			}
			total += info.Size()
			if total > 512<<20 {
				file.Close()
				return bad("本次导出原件超过 512 MiB，请缩小选择范围")
			}
			name := filepath.Base(path)
			if names[name] {
				name = fmt.Sprintf("%s（%v）%s", strings.TrimSuffix(name, filepath.Ext(name)), resume["id"], filepath.Ext(name))
			}
			names[name] = true
			entry, err := archive.Create("简历文件/" + name)
			if err != nil {
				file.Close()
				return err
			}
			_, err = io.Copy(entry, file)
			file.Close()
			if err != nil {
				return err
			}
			count++
		}
	}
	if len(missing) > 0 {
		entry, err := archive.Create("缺失简历文件清单.txt")
		if err != nil {
			return err
		}
		if _, err = io.WriteString(entry, "以下应聘记录暂无简历文件（未上传简历包或未匹配）：\n"+strings.Join(missing, "\n")); err != nil {
			return err
		}
	}
	if err = archive.Close(); err != nil {
		return err
	}
	if _, err = archiveFile.Seek(0, 0); err != nil {
		return err
	}
	info, err := archiveFile.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("X-Export-Count", str(count))
	w.Header().Set("X-Export-Missing", str(len(missing)))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape("简历导出.zip"))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "简历导出.zip", info.ModTime(), archiveFile)
	return nil
}
func dateRange(r *http.Request, fromKey, toKey string, maxDays int, required bool) (time.Time, time.Time, error) {
	today := time.Now().In(shanghai)
	end := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, shanghai)
	var err error
	if raw := r.URL.Query().Get(toKey); raw != "" {
		end, err = time.ParseInLocation("2006-01-02", raw, shanghai)
		if err != nil {
			return time.Time{}, time.Time{}, bad("日期格式必须为 YYYY-MM-DD")
		}
	} else if required {
		return time.Time{}, time.Time{}, bad(fromKey + " 和 " + toKey + " 均为必填日期")
	}
	start := end.AddDate(0, 0, -29)
	if raw := r.URL.Query().Get(fromKey); raw != "" {
		start, err = time.ParseInLocation("2006-01-02", raw, shanghai)
		if err != nil {
			return time.Time{}, time.Time{}, bad("日期格式必须为 YYYY-MM-DD")
		}
	} else if required {
		return time.Time{}, time.Time{}, bad(fromKey + " 和 " + toKey + " 均为必填日期")
	}
	if start.After(end) {
		return time.Time{}, time.Time{}, bad("开始日期不能晚于结束日期")
	}
	if maxDays > 0 && end.Sub(start) >= time.Duration(maxDays)*24*time.Hour {
		return time.Time{}, time.Time{}, bad(fmt.Sprintf("日期范围最长为 %d 天", maxDays))
	}
	return start, end.AddDate(0, 0, 1), nil
}
func (a *App) resultReport(w http.ResponseWriter, r *http.Request, p *Principal) error {
	start, end, err := dateRange(r, "imported_after", "imported_before", 0, true)
	if err != nil {
		return err
	}
	resumes, err := rows(r.Context(), a.Pool, "SELECT row_to_json(r) FROM core_resume r WHERE imported_at>=$1 AND imported_at<$2 ORDER BY imported_at,id", start, end)
	if err != nil {
		return err
	}
	statuses := []string{"raw", "archived", "pending_reallocation", "pending_review", "pending_dispatch", "pending_screening", "screening_passed", "screening_rejected"}
	groups := map[string]Object{}
	rejections := map[string]int{}
	total := Object{"imported": 0, "allocated": 0}
	details := [][]any{}
	for _, resume := range resumes {
		c, err := a.get(r.Context(), a.Pool, "core_candidate", resume["candidate_id"])
		if err != nil {
			return err
		}
		view, err := a.serialize(r.Context(), "candidates", c, p, false)
		if err != nil {
			return err
		}
		at, _ := a.get(r.Context(), a.Pool, "core_assignmentattempt", obj(view["current_attempt"])["id"])
		dep, _ := a.get(r.Context(), a.Pool, "core_department", at["current_department_id"])
		primary, secondary, _ := a.hierarchy(r.Context(), dep)
		if q := r.URL.Query().Get("primary_department_id"); q != "" {
			if num(q) <= 0 {
				return bad("primary_department_id 必须是正整数")
			}
			if num(q) != num(primary["id"]) {
				continue
			}
		}
		if q := r.URL.Query().Get("department_id"); q != "" {
			if num(q) <= 0 {
				return bad("department_id 必须是正整数")
			}
			if num(q) != num(secondary["id"]) {
				continue
			}
		}
		v, err := a.exportValues(r.Context(), exportRecord{c, resume, at, view, nil})
		if err != nil {
			return err
		}
		pn, sn := str(primary["name"]), str(secondary["name"])
		if pn == "" {
			pn = "未分配"
			if dep != nil {
				pn = "未归属一级部门"
			}
		}
		if sn == "" {
			sn = "未分配"
		}
		key := pn + "\x1f" + sn
		if groups[key] == nil {
			groups[key] = Object{"imported": 0, "allocated": 0}
		}
		for _, bucket := range []Object{groups[key], total} {
			bucket["imported"] = num(bucket["imported"]) + 1
			bucket[str(view["system_status"])] = num(bucket[str(view["system_status"])]) + 1
			if at != nil {
				bucket["allocated"] = num(bucket["allocated"]) + 1
			}
		}
		if at["status"] == "rejected" && str(at["feedback_reason_code"]) != "" {
			rejections[key+"\x1f"+str(at["feedback_reason_code"])]++
		}
		row := []any{localTime(resume["imported_at"]), c["name"], resume["apply_id"], resume["entity"], resume["position_name"], resume["volunteer_rank"], v["highest_education"], v["initial_department"], primary["name"], secondary["name"], dep["name"], v["allocation_source"], v["resume_status"], v["feedback_result"], v["feedback_reason_code"], v["feedback_reason"], v["feedback_note"]}
		for _, key := range []string{"first_dispatched_at", "current_department_entered_at", "feedback_at", "hr_dispatch_duration_hours", "current_department_duration_hours", "total_feedback_duration_hours"} {
			row = append(row, v[key])
		}
		details = append(details, row)
	}
	headers := []string{"当前接收一级部门", "当前接收二级部门", "导入简历数", "分配简历数"}
	for _, s := range statuses {
		headers = append(headers, systemLabels[s])
	}
	summary := [][]any{}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	keys = append(keys, "合计\x1f")
	for _, key := range keys {
		bucket := groups[key]
		if strings.HasPrefix(key, "合计\x1f") {
			bucket = total
		}
		names := strings.Split(key, "\x1f")
		row := []any{names[0], names[1], num(bucket["imported"]), num(bucket["allocated"])}
		for _, s := range statuses {
			row = append(row, num(bucket[s]))
		}
		summary = append(summary, row)
	}
	rejected := [][]any{}
	keys = []string{}
	for key := range rejections {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.Split(key, "\x1f")
		rejected = append(rejected, []any{parts[0], parts[1], parts[2], a.choiceLabel("core_assignmentattempt", "feedback_reason_code", parts[2]), rejections[key]})
	}
	book := excelize.NewFile()
	defer book.Close()
	if err = sheet(book, "部门汇总", headers, summary); err != nil {
		return err
	}
	if err = sheet(book, "简历明细", []string{"导入时间", "姓名", "应聘ID", "招聘主体", "岗位", "志愿", "最高学历", "首次部门", "当前接收一级部门", "当前接收二级部门", "当前接收节点", "分配来源", "简历状态", "反馈结果", "不通过原因码", "不通过原因", "反馈备注", "首次下发时间", "当前部门进入时间", "反馈时间", "HR 下发时长（小时）", "当前部门处理时长（小时）", "总反馈时长（小时）"}, details); err != nil {
		return err
	}
	if err = sheet(book, "不通过原因汇总", []string{"当前接收一级部门", "当前接收二级部门", "不通过原因码", "不通过原因", "数量"}, rejected); err != nil {
		return err
	}
	return sendWorkbook(w, book, "简历结果报表_"+start.Format("20060102")+"_"+end.AddDate(0, 0, -1).Format("20060102")+".xlsx")
}
