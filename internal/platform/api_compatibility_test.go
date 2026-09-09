package platform

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// 从外部旧版本检出的 Django 在隔离库导出同一组请求，供迁移验收比较。
func TestAPICompatibilitySnapshot(t *testing.T) {
	path := os.Getenv("TEST_API_GOLDEN")
	if path == "" {
		t.Skip("TEST_API_GOLDEN is not configured")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]Object
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	a := integrationApp(t)
	p := adminPrincipal(t, a)
	got := map[string]Object{}
	for path, item := range expected {
		response := apiRequest(t, a, p, "GET", path, nil)
		var body any
		if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: non-JSON response", path)
		}
		got[path] = Object{"status": response.Code, "body": body}
		if int(num(item["status"])) != response.Code {
			t.Errorf("%s: status=%d expected=%v", path, response.Code, item["status"])
		}
	}
	for path, expectedResponse := range expected {
		differences := compatibilityDiff(map[string]any(expectedResponse), clone(got[path]), path)
		for i, difference := range differences {
			if i < 20 {
				t.Error("API mismatch: " + difference)
			}
		}
	}
	raw, _ = json.Marshal(got)
	if output := os.Getenv("TEST_API_GOT"); output != "" {
		if err = os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func compatibilityDiff(expected, actual any, path string) []string {
	// v2 唯一预期展示差异：步骤说明新增平台全文提取。其余字段继续严格比较。
	if strings.HasPrefix(path, "/api/pipeline/runs/.body.") && strings.HasSuffix(path, ".description") && expected == "逐份分析简历与岗位匹配情况，并保存结果。" && actual == "逐份提取全文、校验文本、分析岗位匹配并保存结果；材料异常可单份重试。" {
		return nil
	}
	if reflect.DeepEqual(expected, actual) {
		return nil
	}
	if x, ok := expected.(string); ok {
		if y, ok := actual.(string); ok {
			a, e1 := time.Parse(time.RFC3339Nano, x)
			b, e2 := time.Parse(time.RFC3339Nano, y)
			if e1 == nil && e2 == nil && a.Equal(b) {
				return nil
			}
		}
	}
	x, xok := expected.(map[string]any)
	y, yok := actual.(map[string]any)
	if xok && yok {
		differences := []string{}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		for k := range keys {
			if k == "elapsed_seconds" {
				continue
			}
			a, existsA := x[k]
			b, existsB := y[k]
			if !existsA || !existsB {
				differences = append(differences, path+"."+k+" (field presence)")
			} else {
				differences = append(differences, compatibilityDiff(a, b, path+"."+k)...)
			}
		}
		sort.Strings(differences)
		return differences
	}
	a, aok := expected.([]any)
	b, bok := actual.([]any)
	if aok && bok && len(a) == len(b) {
		differences := []string{}
		for i := range a {
			differences = append(differences, compatibilityDiff(a[i], b[i], fmt.Sprintf("%s[%d]", path, i))...)
		}
		return differences
	}
	return []string{path}
}
