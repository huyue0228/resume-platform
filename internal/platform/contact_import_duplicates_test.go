package platform

import (
	"context"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestContactImportKeepsDistinctDepartmentsAndMergesIdenticalGrants(t *testing.T) {
	for _, format := range []string{"csv", "xlsx", "legacy-xlsx"} {
		t.Run(format, func(t *testing.T) {
			a := isolatedInboxApp(t, false)
			admin := adminPrincipal(t, a)
			employee := token(8)
			base := Object{"姓名": "兼任人员", "工号": employee, "邮箱": employee + "@example.test", "一层部门": "中心甲", "二层部门": "研发部", "角色": "接口人", "可转派": "是"}
			otherRoot := clone(base)
			otherRoot["一层部门"] = "中心乙"
			otherChild := clone(base)
			otherChild["二层部门"] = "产品部"
			primaryOnly := clone(base)
			primaryOnly["二层部门"] = ""
			screener := clone(base)
			screener["角色"] = "简历筛选人"
			secondScreener := clone(otherRoot)
			secondScreener["角色"] = "简历筛选人"
			alias := clone(base)
			alias["角色"], alias["可转派"], alias["是否启用"] = "secondary", "yes", "true"
			data := []Object{base, otherRoot, otherChild, primaryOnly, screener, secondScreener, {}, clone(base), alias}
			var accountID int64
			for _, mode := range []string{"incremental", "replace"} {
				var result Object
				if format == "csv" {
					result = responseObject(t, importTableFixture(t, a, admin, "contacts", mode, data), 200)
				} else {
					book := excelize.NewFile()
					headers := append([]string{}, a.Spec.ImportSchemas["contacts"].Headers...)
					values := [][]any{}
					for _, row := range data {
						cells := []any{}
						for _, h := range headers {
							cells = append(cells, row[h])
						}
						values = append(values, cells)
					}
					if format == "legacy-xlsx" {
						for i, h := range headers {
							if h == "角色" {
								headers[i] = "接口人层级"
							}
						}
					}
					if err := sheet(book, "人员", headers, values); err != nil {
						t.Fatal(err)
					}
					buf, err := book.WriteToBuffer()
					book.Close()
					if err != nil {
						t.Fatal(err)
					}
					result = responseObject(t, importFileFixture(t, a, admin, "contacts", mode, "人员.xlsx", buf.Bytes()), 200)
				}
				counts := obj(result["counts"])
				if num(counts["contacts"]) != 6 || num(counts["contacts_merged"]) != 2 || !strings.Contains(str(result["detail"]), "合并 2 条") {
					t.Fatalf("unexpected grant counts: %v", result)
				}
				warnings := list(result["warnings"])
				if len(warnings) != 1 || str(obj(warnings[0])["rows"]) != "[9 10]" {
					t.Fatalf("merged row numbers lost blank rows: %v", warnings)
				}
				p := grantPrincipal(t, a, employee)
				if len(p.Grants) != 6 || (accountID != 0 && accountID != num(p.User["id"])) {
					t.Fatal("department grants or the single employee account changed")
				}
				accountID = num(p.User["id"])
				paths := map[string]bool{}
				for _, g := range p.Grants {
					primary, secondary, _ := a.hierarchyWithDB(context.Background(), a.Pool, g.Department)
					paths[str(primary["name"])+"/"+str(secondary["name"])+"/"+str(g.Contact["contact_level"])] = true
					if !truth(g.Contact["is_active"]) || (g.Contact["contact_level"] == "tertiary" && truth(g.Contact["can_delegate"])) {
						t.Fatal("merged grants changed effective permissions")
					}
				}
				for _, want := range []string{"中心甲/研发部/secondary", "中心乙/研发部/secondary", "中心甲/产品部/secondary", "中心甲//secondary", "中心甲/研发部/tertiary", "中心乙/研发部/tertiary"} {
					if !paths[want] {
						t.Fatalf("lost independent grant %s: %v", want, paths)
					}
				}
			}
		})
	}
}

func TestContactImportConflictingDuplicateReportsRowsAndRollsBack(t *testing.T) {
	for _, field := range []string{"可转派", "是否启用"} {
		t.Run(field, func(t *testing.T) {
			a := isolatedInboxApp(t, false)
			admin := adminPrincipal(t, a)
			employee := token(8)
			base := Object{"姓名": "兼任人员", "工号": employee, "邮箱": employee + "@example.test", "一层部门": "中心", "二层部门": "研发部", "角色": "接口人", "可转派": "是", "是否启用": "是"}
			responseObject(t, importTableFixture(t, a, admin, "contacts", "incremental", []Object{base}), 200)
			conflict := clone(base)
			base[field] = "否"
			other := clone(conflict)
			other["二层部门"] = "不应部分导入的部门"
			result := responseObject(t, importTableFixture(t, a, admin, "contacts", "replace", []Object{{}, base, other, conflict}), 400)
			for _, want := range []string{"第 3 行", "第 5 行", employee, "中心 / 研发部", "接口人", field, "冲突"} {
				if !strings.Contains(str(result["detail"]), want) {
					t.Fatalf("missing %s in conflict: %v", want, result)
				}
			}
			p := grantPrincipal(t, a, employee)
			if len(p.Grants) != 1 || !truth(p.Grants[0].Contact["is_active"]) || !truth(p.Grants[0].Contact["can_delegate"]) {
				t.Fatal("conflicting import changed an existing grant")
			}
			var departments int
			if err := a.Pool.QueryRow(context.Background(), "SELECT count(*) FROM core_department WHERE name=$1", other["二层部门"]).Scan(&departments); err != nil || departments != 0 {
				t.Fatalf("conflicting import partially committed departments: %d %v", departments, err)
			}
		})
	}
}
