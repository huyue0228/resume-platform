package platform

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func canonicalJSON(value any, spaces bool) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(value)
	raw := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	if !spaces {
		return raw
	}
	out := []byte{}
	quoted, escape := false, false
	for _, c := range raw {
		out = append(out, c)
		if escape {
			escape = false
			continue
		}
		if c == '\\' && quoted {
			escape = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if !quoted && (c == ',' || c == ':') {
			out = append(out, ' ')
		}
	}
	return out
}
func fingerprint(value any) string {
	sum := sha256.Sum256(canonicalJSON(value, true))
	return hex.EncodeToString(sum[:])
}
func (a *App) configValue(ctx context.Context, key string, fallback any) any {
	v, err := one(ctx, a.Pool, "SELECT row_to_json(c) FROM core_config c WHERE key=$1", key)
	if err != nil {
		return fallback
	}
	value := v["value"]
	if m, ok := value.(map[string]any); ok {
		if inner, yes := m["value"]; yes {
			return inner
		}
	}
	return value
}
func (a *App) setConfig(ctx context.Context, db DB, key string, value any) error {
	raw := canonicalJSON(value, false)
	_, err := db.Exec(ctx, "INSERT INTO core_config(key,value) VALUES($1,$2::jsonb) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", key, string(raw))
	return err
}
func (a *App) encryptKey(value string) (string, error) {
	key := sha256.Sum256([]byte(a.Config.Secret + ":smart-resume-filter:ai-connection:v1"))
	block, err := aes.NewCipher(key[16:])
	if err != nil {
		return "", err
	}
	plain := []byte(value)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	raw := make([]byte, 25+len(plain))
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(time.Now().Unix()))
	if _, err = rand.Read(raw[9:25]); err != nil {
		return "", err
	}
	cipher.NewCBCEncrypter(block, raw[9:25]).CryptBlocks(raw[25:], plain)
	mac := hmac.New(sha256.New, key[:16])
	mac.Write(raw)
	return base64.URLEncoding.EncodeToString(append(raw, mac.Sum(nil)...)), nil
}
func (a *App) decryptKey(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	invalid := bad("已保存的模型 API Key 无法解密，请由管理员重新保存")
	key := sha256.Sum256([]byte(a.Config.Secret + ":smart-resume-filter:ai-connection:v1"))
	raw, err := base64.URLEncoding.DecodeString(value)
	if err != nil || len(raw) < 73 || raw[0] != 0x80 || (len(raw)-57)%16 != 0 {
		return "", invalid
	}
	signed := raw[:len(raw)-32]
	mac := hmac.New(sha256.New, key[:16])
	mac.Write(signed)
	if !hmac.Equal(mac.Sum(nil), raw[len(raw)-32:]) {
		return "", invalid
	}
	block, _ := aes.NewCipher(key[16:])
	plain := make([]byte, len(signed)-25)
	cipher.NewCBCDecrypter(block, signed[9:25]).CryptBlocks(plain, signed[25:])
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > 16 || !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return "", invalid
	}
	return string(plain[:len(plain)-pad]), nil
}
func (a *App) connection(ctx context.Context) (Object, string, error) {
	c := Object{}
	for _, key := range []string{"api_style", "model_name", "base_url"} {
		c[key] = a.configValue(ctx, "ai_connection_"+key, "")
	}
	if c["api_style"] == "" {
		c["api_style"] = "chat_json"
	}
	key, err := a.decryptKey(str(a.configValue(ctx, "ai_connection_api_key", "")))
	return c, key, err
}
func connectionFingerprint(c Object, key string) string {
	sum := sha256.Sum256([]byte(key))
	return fingerprint(Object{"api_style": c["api_style"], "model_name": c["model_name"], "base_url": c["base_url"], "api_key_hash": hex.EncodeToString(sum[:])})
}
func (a *App) connectionStatus(ctx context.Context) (Object, error) {
	c, key, err := a.connection(ctx)
	if err != nil {
		return nil, err
	}
	result := clone(c)
	stored := str(a.configValue(ctx, "ai_connection_api_key", ""))
	result["api_key_configured"] = stored != ""
	result["api_key_source"] = "not_configured"
	if stored != "" {
		result["api_key_source"] = "system_config"
	}
	result["test_passed"] = str(a.configValue(ctx, "ai_connection_test_fingerprint", "")) == connectionFingerprint(c, key)
	result["tested_at"] = a.configValue(ctx, "ai_connection_tested_at", "")
	result["structured_output_mode"] = a.configValue(ctx, "ai_connection_structured_output_mode", "legacy_compat")
	return result, nil
}
func validateURL(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", bad("Base URL 必须是有效的 HTTP(S) 地址，不能携带凭据、查询或片段")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func (a *App) configItem(ctx context.Context, key string, meta Object) Object {
	item := clone(meta)
	item["key"] = key
	item["value"] = a.configValue(ctx, key, meta["default"])
	return item
}
func validateConfig(meta Object, value any) error {
	switch str(meta["value_type"]) {
	case "boolean":
		if _, ok := value.(bool); !ok {
			return bad("配置值必须为布尔值")
		}
	case "integer", "number":
		number, ok := value.(float64)
		if !ok {
			return bad("配置值类型不正确")
		}
		if str(meta["value_type"]) == "integer" && math.Trunc(number) != number {
			return bad("配置值必须是整数")
		}
		if minimum, ok := meta["min"].(float64); ok && number < minimum {
			return bad("配置值低于允许下限")
		}
		if maximum, ok := meta["max"].(float64); ok && number > maximum {
			return bad("配置值超过允许上限")
		}
	}
	return nil
}
func (a *App) configSettings(w http.ResponseWriter, r *http.Request, path string, p *Principal) error {
	key := strings.Trim(path, "/")
	if key == "" && r.Method == "GET" {
		items := []Object{}
		for _, k := range []string{"welink_enabled"} {
			items = append(items, a.configItem(r.Context(), k, a.Spec.Configs[k]))
		}
		write(w, 200, items)
		return nil
	}
	meta, ok := a.Spec.Configs[key]
	if !ok {
		return &apiError{404, "未知配置项"}
	}
	if r.Method == "PATCH" || r.Method == "PUT" {
		body, err := readBody(w, r)
		if err != nil {
			return err
		}
		if err = validateConfig(meta, body["value"]); err != nil {
			return err
		}
		if err = a.setConfig(r.Context(), a.Pool, key, body["value"]); err != nil {
			return err
		}
	} else if r.Method != "GET" {
		return &apiError{405, "请求方法不允许"}
	}
	write(w, 200, a.configItem(r.Context(), key, meta))
	return nil
}
func (a *App) modelSettings(w http.ResponseWriter, r *http.Request, path string, p *Principal) error {
	ctx := r.Context()
	if path == "ai-connection" {
		if r.Method == "PATCH" {
			body, err := readBody(w, r)
			if err != nil {
				return err
			}
			current, _, err := a.connection(ctx)
			if err != nil {
				return err
			}
			for _, field := range []string{"api_style", "model_name", "base_url"} {
				if value, ok := body[field]; ok {
					current[field] = strings.TrimSpace(str(value))
				}
			}
			if !contains([]string{"responses", "chat_json"}, str(current["api_style"])) || str(current["model_name"]) == "" {
				return bad("API 风格或模型名称无效")
			}
			base, err := validateURL(str(current["base_url"]))
			if err != nil {
				return err
			}
			current["base_url"] = base
			tx, err := a.Pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			oldBase := str(a.configValue(ctx, "ai_connection_base_url", ""))
			for field, value := range current {
				if err = a.setConfig(ctx, tx, "ai_connection_"+field, value); err != nil {
					return err
				}
			}
			key := strings.TrimSpace(str(body["api_key"]))
			if truth(body["clear_api_key"]) || (key == "" && strings.TrimRight(oldBase, "/") != base) {
				if _, err = tx.Exec(ctx, "DELETE FROM core_config WHERE key='ai_connection_api_key'"); err != nil {
					return err
				}
			} else if key != "" {
				encrypted, err := a.encryptKey(key)
				if err != nil {
					return err
				}
				if err = a.setConfig(ctx, tx, "ai_connection_api_key", encrypted); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, "DELETE FROM core_config WHERE key IN ('ai_connection_test_fingerprint','ai_connection_tested_at','ai_connection_structured_output_mode','ai_enabled','ai_connection_profile')"); err != nil {
				return err
			}
			if err = tx.Commit(ctx); err != nil {
				return err
			}
		} else if r.Method != "GET" {
			return &apiError{405, "请求方法不允许"}
		}
		value, err := a.connectionStatus(ctx)
		if err != nil {
			return err
		}
		write(w, 200, value)
		return nil
	}
	if path == "ai-connection/settings" && r.Method == "GET" {
		items := []Object{}
		keys := []string{}
		for key := range a.Spec.AIConfigs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			items = append(items, a.configItem(ctx, key, a.Spec.AIConfigs[key]))
		}
		write(w, 200, Object{"settings": items})
		return nil
	}
	if strings.HasPrefix(path, "ai-connection/settings/") && r.Method == "PATCH" {
		key := strings.TrimPrefix(path, "ai-connection/settings/")
		meta, ok := a.Spec.AIConfigs[key]
		if !ok {
			return &apiError{404, "未知 AI 配置项"}
		}
		body, err := readBody(w, r)
		if err != nil {
			return err
		}
		if err = validateConfig(meta, body["value"]); err != nil {
			return err
		}
		if err = a.setConfig(ctx, a.Pool, key, body["value"]); err != nil {
			return err
		}
		write(w, 200, a.configItem(ctx, key, meta))
		return nil
	}
	if path == "ai-connection/models" && r.Method == "POST" {
		body, err := readBody(w, r)
		if err != nil {
			return err
		}
		base, err := validateURL(str(body["base_url"]))
		if err != nil {
			return err
		}
		c, key, err := a.connection(ctx)
		if err != nil {
			return err
		}
		if str(body["api_key"]) != "" {
			key = str(body["api_key"])
		} else if strings.TrimRight(str(c["base_url"]), "/") != base {
			key = ""
		}
		payload, _, err := a.modelHTTP(ctx, "GET", base+"/models", key, nil)
		if err != nil {
			write(w, 200, Object{"models": []string{}, "code": "ai_connection_error", "detail": "模型列表读取失败"})
			return nil
		}
		names := []string{}
		for _, item := range list(payload["data"]) {
			name := strings.TrimSpace(str(obj(item)["id"]))
			if name != "" && !contains(names, name) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		write(w, 200, Object{"models": names})
		return nil
	}
	if path == "ai-connection/test" && r.Method == "POST" {
		c, key, err := a.connection(ctx)
		if err != nil {
			return err
		}
		mode, err := a.probeModel(ctx, c, key)
		if err != nil {
			a.Pool.Exec(ctx, "DELETE FROM core_config WHERE key IN ('ai_connection_test_fingerprint','ai_connection_tested_at','ai_connection_structured_output_mode')")
			write(w, 200, Object{"ok": false, "code": "ai_connection_error", "detail": "模型连接或结构化输出验证失败，请检查连接和 CA 配置"})
			return nil
		}
		tested := now()
		tx, err := a.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		for k, v := range (Object{"ai_connection_test_fingerprint": connectionFingerprint(c, key), "ai_connection_tested_at": tested, "ai_connection_structured_output_mode": mode}) {
			if err = a.setConfig(ctx, tx, k, v); err != nil {
				return err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		result := clone(c)
		result["ok"] = true
		result["detail"] = "模型连接测试成功"
		result["tested_at"] = tested
		result["structured_output_mode"] = mode
		write(w, 200, result)
		return nil
	}
	return &apiError{405, "请求方法不允许"}
}
func (a *App) modelHTTP(ctx context.Context, method, address, key string, payload any) (Object, int, error) {
	timeout := time.Duration(num(a.configValue(ctx, "ai_timeout_seconds", 60))) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(canonicalJSON(payload, false))
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := *a.HTTP
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if boolEnv("AGENT_KERNEL_MODEL_INSECURE_SKIP_VERIFY") {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, bad("模型服务连接失败")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, res.StatusCode, bad("模型服务拒绝请求")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, res.StatusCode, bad("模型响应超限")
	}
	var value Object
	if json.Unmarshal(raw, &value) != nil {
		return nil, res.StatusCode, bad("模型返回无效 JSON")
	}
	return value, res.StatusCode, nil
}
func (a *App) probeModel(ctx context.Context, c Object, key string) (string, error) {
	base, err := validateURL(str(c["base_url"]))
	if err != nil || str(c["model_name"]) == "" {
		return "", bad("模型连接未配置")
	}
	schema := Object{"type": "object", "properties": Object{"ok": Object{"type": "boolean"}}, "required": []string{"ok"}, "additionalProperties": false}
	for _, mode := range []string{"strict_schema", "json_compat"} {
		body := Object{"model": c["model_name"]}
		endpoint := "/chat/completions"
		messages := []Object{{"role": "user", "content": "这是连接测试。只返回 JSON: {\"ok\":true}"}}
		if c["api_style"] == "responses" {
			endpoint = "/responses"
			body["input"] = messages
			if mode == "strict_schema" {
				body["text"] = Object{"format": Object{"type": "json_schema", "name": "connection_probe", "strict": true, "schema": schema}}
			} else {
				body["text"] = Object{"format": Object{"type": "json_object"}}
			}
		} else {
			body["messages"] = messages
			if mode == "strict_schema" {
				body["response_format"] = Object{"type": "json_schema", "json_schema": Object{"name": "connection_probe", "strict": true, "schema": schema}}
			} else {
				body["response_format"] = Object{"type": "json_object"}
			}
		}
		response, status, err := a.modelHTTP(ctx, "POST", base+endpoint, key, body)
		if err != nil {
			if mode == "strict_schema" && (status == 400 || status == 422) {
				continue
			}
			return "", err
		}
		text := modelText(response)
		var result Object
		if json.Unmarshal([]byte(text), &result) != nil || !truth(result["ok"]) || len(result) != 1 {
			return "", bad("模型结构化输出验证失败")
		}
		return mode, nil
	}
	return "", errors.New("structured model unsupported")
}
func modelText(response Object) string {
	choices := list(response["choices"])
	if len(choices) > 0 {
		return str(obj(obj(choices[0])["message"])["content"])
	}
	if value := str(response["output_text"]); value != "" {
		return value
	}
	var text strings.Builder
	for _, output := range list(response["output"]) {
		for _, part := range list(obj(output)["content"]) {
			if obj(part)["type"] == "output_text" {
				text.WriteString(str(obj(part)["text"]))
			}
		}
	}
	return text.String()
}
