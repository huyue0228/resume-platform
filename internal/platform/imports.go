package platform

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
	"io"
	"math"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/extrame/xls"
	"github.com/xuri/excelize/v2"
)

type ImportSchema struct {
	Key, Label, Filename string
	SheetName            string `json:"sheet_name"`
	Headers              []string
}

const importSourceRowKey = "_source_row"

type importedContactGrant struct {
	rowNumber             int
	canDelegate, isActive bool
}

func (a *App) readTable(raw []byte, filename, key string) ([]Object, error) {
	return a.readTableReader(bytes.NewReader(raw), filename, key)
}

func (a *App) readTableReader(source io.ReadSeeker, filename, key string) (records []Object, err error) {
	defer func() {
		if recover() != nil {
			records = nil
			err = bad("表格损坏或格式无效")
		}
	}()
	schema, ok := a.Spec.ImportSchemas[key]
	if !ok {
		return nil, bad("未知导入类型")
	}
	var data [][]string
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".csv":
		raw, readErr := io.ReadAll(source)
		if readErr != nil {
			return nil, bad("表格损坏或无法读取")
		}
		raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
		if !utf8.Valid(raw) {
			var decodeErr error
			raw, decodeErr = simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
			if decodeErr != nil || bytes.Contains(raw, []byte("�")) {
				return nil, bad("CSV 编码无效，请使用 UTF-8 或 GB18030")
			}
		}
		if bytes.IndexByte(raw, 0) >= 0 {
			return nil, bad("CSV 内容无效")
		}
		reader := csv.NewReader(bytes.NewReader(raw))
		// 以标准模板表头识别常见分隔符，兼容 UTF-8 与中文 Windows CSV。
		first := strings.SplitN(string(raw), "\n", 2)[0]
		best := 0
		for _, separator := range []rune{',', '\t', ';', '，'} {
			probe := csv.NewReader(strings.NewReader(first))
			probe.Comma = separator
			fields, e := probe.Read()
			score := 0
			if e == nil {
				for _, field := range fields {
					if contains(schema.Headers, strings.TrimSpace(field)) {
						score++
					}
				}
			}
			if score > best {
				best = score
				reader.Comma = separator
			}
		}
		reader.FieldsPerRecord = -1
		data, err = reader.ReadAll()
	case ".xls":
		var book *xls.WorkBook
		book, err = xls.OpenReader(source, "utf-8")
		if err == nil {
			sheet := book.GetSheet(0)
			if sheet == nil {
				return nil, bad("表格没有工作表")
			}
			if sheet.MaxRow > 50000 {
				return nil, bad("单表超过 50000 行上限")
			}
			for i := 0; i <= int(sheet.MaxRow); i++ {
				row := sheet.Row(i)
				if row == nil {
					data = append(data, nil)
					continue
				}
				values := []string{}
				for col := 0; col < row.LastCol(); col++ {
					values = append(values, row.Col(col))
				}
				data = append(data, values)
			}
		}
	case ".xlsx":
		var book *excelize.File
		// Worksheet XML spills to disk above the memory threshold; file size is unrestricted.
		book, err = excelize.OpenReader(source, excelize.Options{UnzipSizeLimit: math.MaxInt64, UnzipXMLSizeLimit: 32 << 20})
		if err == nil {
			defer book.Close()
			sheets := book.GetSheetList()
			if len(sheets) == 0 {
				return nil, bad("表格没有工作表")
			}
			data, err = book.GetRows(sheets[0])
		}
	default:
		return nil, bad("表格仅支持 XLSX、XLS 或 CSV")
	}
	if err != nil {
		return nil, bad("表格损坏或无法读取")
	}
	if len(data) == 0 {
		return nil, bad("表格为空")
	}
	if len(data) > 50001 {
		return nil, bad("单表超过 50000 行上限")
	}
	if key == "contacts" {
		for i := len(data[0]) - 1; i >= 0; i-- {
			if strings.TrimSpace(data[0][i]) == "接口人层级" {
				data[0][i] = "角色"
			}
			if strings.TrimSpace(data[0][i]) != "三级部门" {
				continue
			}
			for _, row := range data[1:] {
				if i < len(row) && strings.TrimSpace(row[i]) != "" {
					return nil, bad("新版不再使用三级部门，请将人员归属填写到一层或二层部门，并下载最新版模板")
				}
			}
			for j, row := range data {
				if i < len(row) {
					data[j] = append(row[:i], row[i+1:]...)
				}
			}
		}
	}
	headers := data[0]
	counts := map[string]int{}
	for _, h := range headers {
		counts[h]++
	}
	missing, unknown, duplicate := []string{}, []string{}, []string{}
	for _, h := range schema.Headers {
		if counts[h] == 0 {
			missing = append(missing, h)
		}
	}
	for _, h := range headers {
		if !contains(schema.Headers, h) {
			unknown = append(unknown, h)
		}
		if counts[h] > 1 && !contains(duplicate, h) {
			duplicate = append(duplicate, h)
		}
	}
	if len(missing)+len(unknown)+len(duplicate) > 0 {
		return nil, bad(fmt.Sprintf("%s表头不符合标准模板：缺少字段【%s】；未知字段【%s】；重复字段【%s】。请下载最新版标准模板", schema.Label, strings.Join(missing, "、"), strings.Join(unknown, "、"), strings.Join(duplicate, "、")))
	}
	for index, row := range data[1:] {
		record := Object{}
		nonempty := false
		for i, h := range headers {
			value := ""
			if i < len(row) {
				value = strings.TrimSpace(row[i])
			}
			record[h] = value
			if value != "" {
				nonempty = true
			}
		}
		if nonempty {
			if key == "contacts" {
				record[importSourceRowKey] = index + 2
			}
			records = append(records, record)
		}
	}
	return records, nil
}
func boolCell(v any) bool {
	return contains([]string{"是", "true", "1", "yes", "y", "启用"}, strings.ToLower(str(v)))
}
func educationCell(value string) string {
	for _, entry := range []struct {
		key     string
		aliases []string
	}{{"doctor", []string{"博士", "博士研究生", "doctor", "phd"}}, {"master", []string{"硕士", "硕士研究生", "研究生", "master"}}, {"bachelor", []string{"本科", "大学本科", "学士", "bachelor"}}, {"associate", []string{"大专", "专科", "大学专科", "associate"}}} {
		if contains(entry.aliases, strings.ToLower(value)) {
			return entry.key
		}
	}
	return ""
}
func dateCell(s string) any {
	if s == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", "2006/1/2", "2006-1-2", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	if serial, err := strconv.ParseFloat(s, 64); err == nil && serial > 1 && serial < 200000 {
		if t, err := excelize.ExcelDateToTime(serial, false); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return nil
}
func (a *App) findUnique(ctx context.Context, db DB, table, column string, value any) (Object, error) {
	if _, ok := fieldFor(a, table, column); !ok {
		return nil, bad("无效业务键")
	}
	items, err := rows(ctx, db, "SELECT row_to_json(t) FROM "+quote(table)+" t WHERE "+quote(column)+"=$1 ORDER BY id", value)
	if err != nil {
		return nil, err
	}
	if len(items) > 1 {
		return nil, bad("数据库存在重复业务键，请先处理重复记录")
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items[0], nil
}
func (a *App) importDepartment(ctx context.Context, db DB, primary, secondary, entity string) (Object, error) {
	if strings.TrimSpace(primary) == "" && strings.TrimSpace(secondary) == "" {
		return nil, bad("一层部门和二层部门不能同时为空")
	}
	var parent Object
	for level, name := range []string{primary, secondary} {
		if name == "" {
			continue
		}
		items, err := rows(ctx, db, "SELECT row_to_json(d) FROM core_department d WHERE level=$1 AND lower(regexp_replace(name,'[[:space:]]','','g'))=$2 AND parent_id IS NOT DISTINCT FROM $3::bigint ORDER BY id", level+1, normalized(name), parent["id"])
		if err != nil {
			return nil, err
		}
		if len(items) > 1 {
			return nil, bad("部门层级存在重复业务键")
		}
		if len(items) == 1 {
			parent = items[0]
		} else {
			parent, err = a.save(ctx, db, "core_department", nil, Object{"name": name, "level": level + 1, "parent_id": parent["id"], "entity": entity})
			if err != nil {
				return nil, err
			}
		}
	}
	return parent, nil
}
func importJobKey(row Object) string {
	return strings.Join([]string{normalized(str(row["招聘主体"])), normalized(str(row["一层部门"])), normalized(str(row["二层部门"])), normalized(str(row["对外发布名称"])), normalized(str(row["职位名称"])), normalized(str(row["岗位类别"]))}, "\x1f")
}
func (a *App) importFiles(w http.ResponseWriter, r *http.Request, p *Principal) error {
	if r.Method != "POST" {
		return &apiError{405, "请求方法不允许"}
	}
	// This is a memory threshold, not an upload size limit. Larger files spill to disk.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return bad("上传失败，请检查文件、网络连接和临时存储空间")
	}
	defer r.MultipartForm.RemoveAll()
	if _, ok := r.MultipartForm.Value["processing_mode"]; ok {
		return bad("简历处理内核由系统固定，不接受 processing_mode")
	}
	mode := r.FormValue("mode")
	if mode == "" {
		mode = "incremental"
	}
	if mode != "incremental" && mode != "replace" {
		return bad("mode 必须是 incremental 或 replace")
	}
	tables := map[string][]Object{}
	var resumePackage *zip.Reader
	for _, key := range []string{"resume_list", "jobs", "schools", "contacts", "resume_package"} {
		files := r.MultipartForm.File[key]
		if len(files) == 0 {
			continue
		}
		if len(files) > 1 {
			return bad("每种类型只能上传一个文件")
		}
		file, err := files[0].Open()
		if err != nil {
			return bad("上传文件无法读取")
		}
		if key == "resume_package" {
			defer file.Close()
			resumePackage, err = zip.NewReader(file, files[0].Size)
			if err != nil {
				return bad("简历包不是有效 ZIP")
			}
		} else {
			table, err := a.readTableReader(file, files[0].Filename, key)
			file.Close()
			if err != nil {
				return err
			}
			tables[key] = table
		}
	}
	if len(tables) == 0 && resumePackage == nil {
		return bad("未上传任何文件")
	}
	ctx := r.Context()
	counts := Object{}
	for _, key := range []string{"candidates_created", "candidates_updated", "resumes_created", "resumes_updated", "jobs", "schools", "contacts", "contacts_merged", "candidates_skipped", "jobs_skipped"} {
		counts[key] = 0
	}
	inc := func(key string) { counts[key] = num(counts[key]) + 1 }
	warnings := []any{}
	jobRows := []Object{}
	keys := map[string]bool{}
	missingRows := []any{}
	for i, row := range tables["jobs"] {
		if str(row["职位名称"]) == "" && str(row["对外发布名称"]) == "" {
			continue
		}
		if str(row["一层部门"]) == "" && str(row["二层部门"]) == "" {
			return bad(fmt.Sprintf("岗位文件第 %d 行一层部门和二层部门不能同时为空", i+2))
		}
		key := importJobKey(row)
		if keys[key] {
			return bad("岗位文件存在重复业务键")
		}
		keys[key] = true
		if str(row["工作职责"]) == "" {
			inc("jobs_skipped")
			missingRows = append(missingRows, i+2)
			continue
		}
		hc, err := strconv.ParseFloat(str(row["HC"]), 64)
		if str(row["HC"]) != "" && (err != nil || hc < 0 || hc != float64(int64(hc))) {
			return bad(fmt.Sprintf("岗位文件第 %d 行 HC 必须是非负整数", i+2))
		}
		jobRows = append(jobRows, row)
	}
	if len(missingRows) > 0 {
		warnings = append(warnings, Object{"code": "job_responsibility_missing", "count": len(missingRows), "rows": missingRows, "message": "工作职责为空的岗位已跳过"})
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(72460910)"); err != nil {
		return err
	}
	if mode == "replace" && (tables["resume_list"] != nil || resumePackage != nil) {
		var active bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_processingrun WHERE status IN ('pending','running','waiting_conflict','cancelling'))").Scan(&active); err != nil {
			return err
		}
		if active {
			return &apiError{409, "存在在途任务，请先完成或取消后再替换简历"}
		}
		candidates, err := a.all(ctx, tx, "core_candidate")
		if err != nil {
			return err
		}
		for _, c := range candidates {
			if err = a.deleteRow(ctx, tx, "core_candidate", c["id"], map[string]bool{}); err != nil {
				return err
			}
		}
	}
	if mode == "replace" && tables["schools"] != nil {
		schools, err := a.all(ctx, tx, "core_school")
		if err != nil {
			return err
		}
		for _, school := range schools {
			if err = a.deleteRow(ctx, tx, "core_school", school["id"], map[string]bool{}); err != nil {
				return err
			}
		}
	}
	missingSchools := []int64{}
	for _, row := range tables["schools"] {
		name := str(row["学校"])
		if name == "" {
			continue
		}
		tagName := str(row["院校标签"])
		var tag Object
		if tagName != "" {
			tag, err = a.findUnique(ctx, tx, "core_schooltag", "code", tagName)
			if err != nil {
				return err
			}
			tag, err = a.save(ctx, tx, "core_schooltag", tag["id"], Object{"code": tagName, "name": tagName, "is_active": true})
			if err != nil {
				return err
			}
		}
		school, err := a.findUnique(ctx, tx, "core_school", "name", name)
		if err != nil {
			return err
		}
		py, initials := pinyinNames(name)
		school, err = a.save(ctx, tx, "core_school", school["id"], Object{"name": name, "platform": tagName, "school_tag_id": tag["id"], "name_pinyin": py, "name_pinyin_initials": initials})
		if err != nil {
			return err
		}
		if str(school["province"]) == "" {
			missingSchools = append(missingSchools, num(school["id"]))
		}
		inc("schools")
	}
	contactIDs := []int64{}
	emails := map[string]string{}
	people := map[string]string{}
	grantKeys := map[string]importedContactGrant{}
	mergedContactRows := []any{}
	for index, row := range tables["contacts"] {
		rowNumber := index + 2
		if sourceRow := num(row[importSourceRowKey]); sourceRow > 1 {
			rowNumber = int(sourceRow)
		}
		no := str(row["工号"])
		if no == "" {
			continue
		}
		email := strings.ToLower(str(row["邮箱"]))
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email {
			return bad("接口人邮箱格式无效")
		}
		if owner := emails[email]; owner != "" && owner != no {
			return bad("接口人文件中同一邮箱对应多个工号")
		}
		emails[email] = no
		identity := normalized(str(row["姓名"])) + "\x1f" + email
		if previous, ok := people[no]; ok && previous != identity {
			return bad("同一工号的姓名和邮箱必须一致")
		}
		people[no] = identity
		if no == "012358" {
			return bad("内置管理员工号不能绑定接口人")
		}
		var duplicate bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_contact WHERE lower(email)=$1 AND employee_no<>$2) OR EXISTS(SELECT 1 FROM accounts_user WHERE lower(email)=$1 AND username<>$2)", email, no).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate {
			return bad("邮箱已被其他工号使用")
		}
		department, err := a.importDepartment(ctx, tx, str(row["一层部门"]), str(row["二层部门"]), "")
		if err != nil {
			return err
		}
		level := departmentContactLevel(department)
		declared := str(row["角色"])
		if declared != "" {
			level = map[string]string{"接口人": "secondary", "简历筛选人": "tertiary", "二级部门HR": "secondary_hr", "二级接口人": "secondary", "三级接口人": "tertiary", "secondary": "secondary", "tertiary": "tertiary", "secondary_hr": "secondary_hr"}[declared]
			if level == "" {
				return bad("角色必须是接口人、简历筛选人或二级部门HR")
			}
		}
		if !contactRoleMatchesDepartment(level, department) {
			return bad("部门HR角色必须匹配授权部门层级")
		}
		key := no + "\x1f" + str(department["id"]) + "\x1f" + level
		grant := importedContactGrant{
			rowNumber:   rowNumber,
			canDelegate: contactIncludesDescendants(level) && (str(row["可转派"]) == "" || boolCell(row["可转派"])),
			isActive:    str(row["是否启用"]) == "" || boolCell(row["是否启用"]),
		}
		if previous, exists := grantKeys[key]; exists {
			conflicts := []string{}
			if previous.canDelegate != grant.canDelegate {
				conflicts = append(conflicts, "可转派")
			}
			if previous.isActive != grant.isActive {
				conflicts = append(conflicts, "是否启用")
			}
			if len(conflicts) > 0 {
				departmentNames := []string{}
				for _, name := range []string{str(row["一层部门"]), str(row["二层部门"])} {
					if name != "" {
						departmentNames = append(departmentNames, name)
					}
				}
				_, roleName := contactRole(level)
				return bad(fmt.Sprintf("接口人文件第 %d 行与第 %d 行的授权设置冲突：工号【%s】，部门【%s】，角色【%s】；%s不一致，请统一后重新导入", rowNumber, previous.rowNumber, no, strings.Join(departmentNames, " / "), roleName, strings.Join(conflicts, "、")))
			}
			mergedContactRows = append(mergedContactRows, rowNumber)
			inc("contacts_merged")
			continue
		}
		grantKeys[key] = grant
		contact, err := a.findContactGrant(ctx, tx, no, department["id"], level)
		if err != nil {
			return err
		}
		if declared == "" && contact != nil {
			level = str(contact["contact_level"])
		}
		py, initials := pinyinNames(str(row["姓名"]))
		contact, err = a.save(ctx, tx, "core_contact", contact["id"], Object{"employee_no": no, "name": row["姓名"], "name_pinyin": py, "name_pinyin_initials": initials, "email": email, "department_id": department["id"], "contact_level": level, "can_delegate": grant.canDelegate, "is_active": grant.isActive})
		if err != nil {
			return err
		}
		if err = a.syncRelations(ctx, tx, "contacts", contact, Object{}); err != nil {
			return err
		}
		contactIDs = append(contactIDs, num(contact["id"]))
		inc("contacts")
	}
	if len(mergedContactRows) > 0 {
		warnings = append(warnings, Object{"code": "contact_duplicate_grants_merged", "count": len(mergedContactRows), "rows": mergedContactRows, "message": "相同设置的部门角色授权已合并"})
	}
	if mode == "replace" && tables["contacts"] != nil {
		if _, err = tx.Exec(ctx, "UPDATE core_contact SET is_active=false WHERE NOT (id=ANY($1::bigint[]))", contactIDs); err != nil {
			return err
		}
		contacts, err := rows(ctx, tx, "SELECT DISTINCT ON (employee_no) row_to_json(c) FROM core_contact c ORDER BY employee_no,id")
		if err != nil {
			return err
		}
		for _, contact := range contacts {
			if err = a.syncContactUser(ctx, tx, contact); err != nil {
				return err
			}
		}
	}
	if len(jobRows) > 0 {
		existing, err := a.all(ctx, tx, "core_job")
		if err != nil {
			return err
		}
		jobMap := map[string]Object{}
		for _, job := range existing {
			dep, err := a.get(ctx, tx, "core_department", job["department_id"])
			if err != nil {
				return err
			}
			primary, secondary, _ := a.hierarchyWithDB(ctx, tx, dep)
			row := Object{"招聘主体": job["entity"], "一层部门": primary["name"], "二层部门": secondary["name"], "对外发布名称": job["public_name"], "职位名称": job["position_name"], "岗位类别": job["category"]}
			key := importJobKey(row)
			if jobMap[key] != nil {
				return bad("数据库岗位存在重复业务键")
			}
			jobMap[key] = job
		}
		if err = a.reuseLegacyJobs(ctx, tx, jobRows, existing, jobMap); err != nil {
			return err
		}
		imported := []int64{}
		for _, row := range jobRows {
			dep, err := a.importDepartment(ctx, tx, str(row["一层部门"]), str(row["二层部门"]), str(row["招聘主体"]))
			if err != nil {
				return err
			}
			values := Object{"department_id": dep["id"], "entity": row["招聘主体"], "category": row["岗位类别"], "public_name": row["对外发布名称"], "is_public": boolCell(row["是否对外发布"]), "position_name": row["职位名称"], "job_family": row["岗位族"], "location": row["工作地点"], "education": row["学历"], "responsibilities": row["工作职责"], "headcount": importInt(str(row["HC"])), "is_active": true}
			job, err := a.save(ctx, tx, "core_job", jobMap[importJobKey(row)]["id"], values)
			if err != nil {
				return err
			}
			majors := []any{}
			for _, name := range regexp.MustCompile(`[、,，;；\n]+`).Split(str(row["需求专业"]), -1) {
				if name = strings.TrimSpace(name); name != "" {
					majors = append(majors, name)
				}
			}
			if err = a.syncRelations(ctx, tx, "jobs", job, Object{"major_names": majors}); err != nil {
				return err
			}
			imported = append(imported, num(job["id"]))
			inc("jobs")
		}
		if mode == "replace" {
			if _, err = tx.Exec(ctx, "UPDATE core_job SET is_active=false WHERE NOT (id=ANY($1::bigint[]))", imported); err != nil {
				return err
			}
		}
	}
	affected := map[int64]bool{}
	educations := map[string]string{}
	ranks := map[string]int{"associate": 1, "bachelor": 2, "master": 3, "doctor": 4}
	for _, row := range tables["resume_list"] {
		hash := identity(str(row["姓名"]), str(row["手机号"]))
		education := educationCell(str(row["学历"]))
		if ranks[education] > ranks[educations[hash]] {
			educations[hash] = education
		}
	}
	for _, row := range tables["resume_list"] {
		name, phone, apply := str(row["姓名"]), str(row["手机号"]), str(row["应聘ID"])
		if name == "" || apply == "" {
			continue
		}
		if normalizedPhone(phone) == "" {
			inc("candidates_skipped")
			continue
		}
		hash := identity(name, phone)
		candidate, err := a.findUnique(ctx, tx, "core_candidate", "identity_hash", hash)
		if err != nil {
			return err
		}
		isNew := candidate == nil
		gender := "U"
		if contains([]string{"男", "m", "male", "man", "1"}, strings.ToLower(str(row["性别"]))) {
			gender = "M"
		} else if contains([]string{"女", "f", "female", "woman", "0"}, strings.ToLower(str(row["性别"]))) {
			gender = "F"
		}
		values := Object{"identity_hash": hash, "name": name, "phone": phone, "gender": gender, "household_province": row["户口所在地"], "first_degree_school": row["第一学历毕业院校"], "highest_degree_school": row["最高学历毕业院校"], "highest_major": row["最高学历专业"]}
		if education := educations[hash]; education != "" {
			values["highest_education"] = education
		}
		candidate, err = a.save(ctx, tx, "core_candidate", candidate["id"], values)
		if err != nil {
			return err
		}
		if isNew {
			inc("candidates_created")
		} else {
			inc("candidates_updated")
		}
		affected[num(candidate["id"])] = true
		resume, err := a.findUnique(ctx, tx, "core_resume", "apply_id", apply)
		if err != nil {
			return err
		}
		isNew = resume == nil
		if resume != nil && num(resume["candidate_id"]) != num(candidate["id"]) {
			var history bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM core_assignmentattempt WHERE resume_id=$1)", resume["id"]).Scan(&history); err != nil {
				return err
			}
			if history {
				return &apiError{409, "应聘 ID 已有分配历史，不能更换候选人身份"}
			}
		}
		status := str(row["应聘状态"])
		if status == "" {
			status = "待处理"
		}
		resume, err = a.save(ctx, tx, "core_resume", resume["id"], Object{"apply_id": apply, "candidate_id": candidate["id"], "entity": row["招聘主体"], "org": row["所属机构"], "position_name": row["对外职位名称"], "status": status, "apply_date": dateCell(str(row["应聘日期"]))})
		if err != nil {
			return err
		}
		if isNew {
			inc("resumes_created")
		} else {
			inc("resumes_updated")
		}
	}
	// 文件先写入私有暂存目录；提交失败则恢复被替换原件。
	files, err := a.stagePackage(ctx, tx, resumePackage, affected)
	if err != nil {
		return err
	}
	defer files.close()
	if err = files.install(); err != nil {
		return err
	}
	for id := range affected {
		if _, err = tx.Exec(ctx, "UPDATE core_candidateworkflow SET revision=revision+1,active_processing_scope_item_id=NULL,active_processing_token=NULL,active_processing_expires_at=NULL WHERE candidate_id=$1", id); err != nil {
			return err
		}
	}
	ids := []int64{}
	for id := range affected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var run Object
	if len(ids) > 0 {
		run, err = a.createRun(ctx, tx, "resume_process", Object{"source": "resume_import"}, ids, p)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	files.committed = true
	a.wakeQueue(ctx)
	runs := []any{}
	agentState := "not_requested"
	status := 200
	if run != nil {
		value, err := a.serialize(ctx, "pipeline/runs", run, p, true)
		if err != nil {
			return err
		}
		runs = append(runs, value)
		agentState = "submitted"
		status = 202
	}
	enrichment, err := a.enqueueSchoolEnrichment(ctx, missingSchools)
	if err != nil {
		enrichment = Object{"status": "queue_failed", "school_count": len(missingSchools)}
	}
	detail := "导入完成"
	if len(mergedContactRows) > 0 {
		detail = fmt.Sprintf("导入完成，已合并 %d 条相同的部门角色授权", len(mergedContactRows))
	}
	write(w, status, Object{"detail": detail, "counts": counts, "warnings": warnings, "school_province_enrichment": enrichment, "agent_processing": agentState, "processing_runs": runs})
	return nil
}

type stagedFile struct {
	source, target, backup string
	installed              bool
}
type stagedFiles struct {
	dir       string
	items     []stagedFile
	committed bool
}

func (s *stagedFiles) install() error {
	for i := range s.items {
		f := &s.items[i]
		if _, err := os.Stat(f.target); err == nil {
			if err = os.Rename(f.target, f.backup); err != nil {
				return bad("无法备份旧简历文件")
			}
		}
		if err := os.Rename(f.source, f.target); err != nil {
			os.Rename(f.backup, f.target)
			return bad("简历文件保存失败")
		}
		f.installed = true
	}
	return nil
}
func (s *stagedFiles) close() {
	if !s.committed {
		for i := len(s.items) - 1; i >= 0; i-- {
			f := s.items[i]
			if f.installed {
				os.Remove(f.target)
				os.Rename(f.backup, f.target)
			}
		}
	}
	if s.dir != "" {
		os.RemoveAll(s.dir)
	}
}
func (a *App) stagePackage(ctx context.Context, db DB, archive *zip.Reader, affected map[int64]bool) (result *stagedFiles, err error) {
	result = &stagedFiles{}
	if archive == nil {
		return result, nil
	}
	if len(archive.File) > 10000 {
		return nil, bad("简历包文件数量超过 10000")
	}
	dest := filepath.Join(a.Config.MediaRoot, "resumes")
	if err = os.MkdirAll(dest, 0700); err != nil {
		return nil, err
	}
	result.dir, err = os.MkdirTemp(a.Config.MediaRoot, ".import-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			result.close()
		}
	}()
	pattern := regexp.MustCompile(`[（(]\s*([^（）()]+?)\s*[）)]`)
	names := map[string]bool{}
	for _, entry := range archive.File {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if entry.FileInfo().IsDir() || !strings.EqualFold(filepath.Ext(entry.Name), ".pdf") {
			continue
		}
		name, nameErr := resumePackageFilename(entry)
		if nameErr != nil {
			return result, nameErr
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return result, bad("简历包不允许符号链接")
		}
		match := pattern.FindStringSubmatch(name)
		if len(match) == 0 {
			continue
		}
		resume, findErr := a.findUnique(ctx, db, "core_resume", "apply_id", strings.TrimSpace(match[1]))
		if findErr != nil {
			return result, findErr
		}
		if resume == nil {
			continue
		}
		if names[name] {
			return result, bad("简历包存在同名 PDF")
		}
		names[name] = true
		source := filepath.Join(result.dir, str(len(result.items))+".pdf")
		if err = copyPackagePDF(ctx, entry, source); err != nil {
			return result, err
		}
		result.items = append(result.items, stagedFile{source: source, target: filepath.Join(dest, name), backup: source + ".backup"})
		if _, err = a.save(ctx, db, "core_resume", resume["id"], Object{"resume_file": name}); err != nil {
			return result, err
		}
		affected[num(resume["candidate_id"])] = true
	}
	return result, nil
}

func resumePackageFilename(entry *zip.File) (string, error) {
	name := entry.Name
	if !utf8.ValidString(name) {
		// archive/zip preserves legacy filename bytes. Chinese Windows ZIPs commonly
		// use GBK/GB18030; PostgreSQL text and the stored filename must use UTF-8.
		if entry.Flags&0x800 != 0 {
			return "", bad("简历包文件名声明为 UTF-8，但编码无效，请重新压缩后上传")
		}
		decoded, err := simplifiedchinese.GB18030.NewDecoder().String(name)
		if err != nil || strings.ContainsRune(decoded, utf8.RuneError) {
			return "", bad("简历包文件名编码无法识别，请使用 UTF-8 或 GBK/GB18030 编码重新压缩")
		}
		name = decoded
	}
	if strings.ContainsRune(name, 0) {
		return "", bad("简历包文件名包含无效字符，请重命名后重新压缩")
	}
	// Decode first: a GBK multibyte character can contain the byte 0x5c ('\\').
	return filepath.Base(strings.ReplaceAll(name, "\\", "/")), nil
}

type importContextReader struct {
	ctx context.Context
	io.Reader
}

func (r importContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func copyPackagePDF(ctx context.Context, entry *zip.File, target string) error {
	source, err := entry.Open()
	if err != nil {
		return bad("简历包损坏")
	}
	defer source.Close()
	dest, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer dest.Close()
	// Read to EOF so ZIP checksum validation still detects truncated or corrupt files.
	if _, err = io.Copy(dest, importContextReader{ctx, source}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return bad("简历文件损坏或无法保存，请检查文件和存储空间")
	}
	return dest.Close()
}

func importInt(value string) int64 { n, _ := strconv.ParseFloat(value, 64); return int64(n) }

func legacyImportJobKey(row Object) string {
	copyRow := clone(row)
	copyRow["一层部门"] = ""
	return importJobKey(copyRow)
}
func (a *App) reuseLegacyJobs(ctx context.Context, db DB, imported, existing []Object, byKey map[string]Object) error {
	groups := map[string][]Object{}
	fullKeys := map[string]bool{}
	for _, row := range imported {
		key := importJobKey(row)
		fullKeys[key] = true
		legacy := legacyImportJobKey(row)
		groups[legacy] = append(groups[legacy], row)
	}
	oldByLegacy := map[string][]Object{}
	for _, job := range existing {
		dep, err := a.get(ctx, db, "core_department", job["department_id"])
		if err != nil {
			return err
		}
		if dep == nil || dep["parent_id"] != nil || num(dep["level"]) != 2 {
			continue
		}
		row := Object{"招聘主体": job["entity"], "二层部门": dep["name"], "对外发布名称": job["public_name"], "职位名称": job["position_name"], "岗位类别": job["category"]}
		if !fullKeys[importJobKey(row)] {
			key := legacyImportJobKey(row)
			oldByLegacy[key] = append(oldByLegacy[key], job)
		}
	}
	for legacy, items := range groups {
		missing := []Object{}
		for _, row := range items {
			if byKey[importJobKey(row)] == nil {
				missing = append(missing, row)
			}
		}
		candidates := oldByLegacy[legacy]
		if len(missing) == 1 && len(candidates) == 1 && str(missing[0]["一层部门"]) != "" {
			byKey[importJobKey(missing[0])] = candidates[0]
			continue
		}
		if len(items) == 1 && len(missing) == 0 || len(items) > 1 && len(missing) != 1 {
			full := false
			for _, row := range items {
				full = full || str(row["一层部门"]) != ""
			}
			if full {
				for _, job := range candidates {
					if _, err := a.save(ctx, db, "core_job", job["id"], Object{"is_active": false}); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
