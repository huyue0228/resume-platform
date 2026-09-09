package platform

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type oauthConfig struct {
	Enabled, PKCE                                                                                            bool
	ClientID, Secret, Authorize, Token, Userinfo, Redirect, Frontend, Scope, EmployeeField, EmailField, Auth string
	TTL                                                                                                      time.Duration
}

func oauthSettings() oauthConfig {
	return oauthConfig{Enabled: boolEnv("W3_OAUTH2_ENABLED"), PKCE: strings.EqualFold(env("W3_OAUTH2_USE_PKCE", "True"), "true") || env("W3_OAUTH2_USE_PKCE", "True") == "1", ClientID: env("W3_OAUTH2_CLIENT_ID", ""), Secret: env("W3_OAUTH2_CLIENT_SECRET", ""), Authorize: env("W3_OAUTH2_AUTHORIZE_URL", ""), Token: env("W3_OAUTH2_TOKEN_URL", ""), Userinfo: env("W3_OAUTH2_USERINFO_URL", ""), Redirect: env("W3_OAUTH2_REDIRECT_URI", ""), Frontend: env("W3_OAUTH2_FRONTEND_CALLBACK_URL", "/login"), Scope: env("W3_OAUTH2_SCOPE", ""), EmployeeField: env("W3_OAUTH2_EMPLOYEE_NO_FIELD", "employeeNumber"), EmailField: env("W3_OAUTH2_EMAIL_FIELD", "email"), Auth: env("W3_OAUTH2_CLIENT_AUTH_METHOD", "client_secret_basic"), TTL: time.Duration(num(env("W3_OAUTH2_TRANSACTION_TTL_SECONDS", "300"))) * time.Second}
}
func (c oauthConfig) ready() bool {
	if !c.Enabled || c.ClientID == "" || oauthTimeout() <= 0 || c.TTL <= 0 || c.EmployeeField == "" || c.EmailField == "" {
		return false
	}
	if c.Auth != "none" && c.Auth != "client_secret_basic" && c.Auth != "client_secret_post" {
		return false
	}
	if c.Auth != "none" && c.Secret == "" {
		return false
	}
	for _, raw := range []string{c.Authorize, c.Token, c.Userinfo, c.Redirect} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(boolEnv("DJANGO_DEBUG") && u.Scheme == "http" && contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname()))) {
			return false
		}
	}
	redirect, _ := url.Parse(c.Redirect)
	frontend, err := url.Parse(c.Frontend)
	return redirect.Path == "/api/auth/w3/callback/" && redirect.RawQuery == "" && redirect.Fragment == "" && err == nil && frontend.Host == "" && frontend.Scheme == "" && frontend.Path == "/login" && frontend.Fragment == "" && !frontend.Query().Has("oauth2") && !frontend.Query().Has("oauth2_error") && !strings.HasPrefix(c.Frontend, "//")
}
func addQuery(raw string, values map[string]string) string {
	u, _ := url.Parse(raw)
	q := u.Query()
	for k, v := range values {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func (a *App) oauth(w http.ResponseWriter, r *http.Request, path string) error {
	c := oauthSettings()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if path == "status" && r.Method == "GET" {
		var start any
		if c.ready() {
			start = "/api/auth/w3/start/"
		}
		write(w, 200, Object{"enabled": c.Enabled, "ready": c.ready(), "debug_token_login_enabled": a.Config.Debug && !c.ready(), "start_url": start})
		return nil
	}
	if !c.ready() {
		return &apiError{503, "W3 OAuth2 尚未正确配置"}
	}
	secure := strings.HasPrefix(c.Redirect, "https://")
	if path == "start" && r.Method == "GET" {
		session, state, verifier := token(32), token(32), ""
		values := map[string]string{"response_type": "code", "client_id": c.ClientID, "redirect_uri": c.Redirect, "state": state}
		if c.Scope != "" {
			values["scope"] = c.Scope
		}
		if c.PKCE {
			verifier = base64.RawURLEncoding.EncodeToString([]byte(token(32)))
			hash := sha256.Sum256([]byte(verifier))
			values["code_challenge"] = base64.RawURLEncoding.EncodeToString(hash[:])
			values["code_challenge_method"] = "S256"
		}
		raw, _ := json.Marshal(Object{"state": state, "verifier": verifier})
		if err := a.Redis.Set(r.Context(), "oauth:transaction:"+session, raw, c.TTL).Err(); err != nil {
			return &apiError{503, "登录服务暂不可用"}
		}
		http.SetCookie(w, &http.Cookie{Name: "resume_oauth", Value: session, Path: "/api/auth/w3/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(c.TTL.Seconds())})
		http.Redirect(w, r, addQuery(c.Authorize, values), 302)
		return nil
	}
	cookie, err := r.Cookie("resume_oauth")
	if err != nil {
		if path == "callback" {
			http.Redirect(w, r, addQuery(c.Frontend, map[string]string{"oauth2_error": "state_invalid"}), 302)
			return nil
		}
		return bad("W3 登录凭据无效或已过期，请重新登录")
	}
	if path == "callback" && r.Method == "GET" {
		redirectError := func(code string) error {
			http.Redirect(w, r, addQuery(c.Frontend, map[string]string{"oauth2_error": code}), 302)
			return nil
		}
		raw, err := a.Redis.GetDel(r.Context(), "oauth:transaction:"+cookie.Value).Bytes()
		var transaction Object
		if err != nil || json.Unmarshal(raw, &transaction) != nil || r.URL.Query().Get("state") == "" || subtle.ConstantTimeCompare([]byte(str(transaction["state"])), []byte(r.URL.Query().Get("state"))) != 1 {
			return redirectError("state_invalid")
		}
		if r.URL.Query().Get("error") != "" {
			return redirectError("provider_denied")
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			return redirectError("authorization_code_missing")
		}
		form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.Redirect}}
		if c.PKCE {
			form.Set("code_verifier", str(transaction["verifier"]))
		}
		if c.Auth != "client_secret_basic" {
			form.Set("client_id", c.ClientID)
		}
		if c.Auth == "client_secret_post" {
			form.Set("client_secret", c.Secret)
		}
		req, _ := http.NewRequestWithContext(r.Context(), "POST", c.Token, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		if c.Auth == "client_secret_basic" {
			req.SetBasicAuth(c.ClientID, c.Secret)
		}
		tokens, err := a.oauthRequest(req)
		accessToken, validToken := tokens["access_token"].(string)
		if err != nil || !validToken || strings.TrimSpace(accessToken) == "" {
			return redirectError("token_exchange_failed")
		}
		req, _ = http.NewRequestWithContext(r.Context(), "GET", addQuery(c.Userinfo, map[string]string{"access_token": str(tokens["access_token"]), "scope": c.Scope, "client_id": c.ClientID}), nil)
		req.Header.Set("Accept", "application/json")
		info, err := a.oauthRequest(req)
		if err != nil {
			return redirectError("userinfo_failed")
		}
		employee := nested(info, c.EmployeeField)
		if _, ok := employee.(string); !ok {
			if n, number := employee.(float64); !number || math.Trunc(n) != n {
				return redirectError("employee_no_missing")
			}
		}
		employee = strings.TrimSpace(str(employee))
		emailRaw, ok := nested(info, c.EmailField).(string)
		if !ok {
			return redirectError("email_missing")
		}
		email := strings.ToLower(strings.TrimSpace(emailRaw))
		if str(employee) == "" {
			return redirectError("employee_no_missing")
		}
		address, emailErr := mail.ParseAddress(email)
		if emailErr != nil || address.Address != email || !strings.Contains(strings.SplitN(email, "@", 2)[1], ".") {
			return redirectError("email_missing")
		}
		user, err := one(r.Context(), a.Pool, "SELECT row_to_json(u) FROM accounts_user u WHERE username=$1 AND lower(email)=$2", str(employee), email)
		if err != nil {
			return redirectError("account_not_found")
		}
		if !truth(user["is_active"]) {
			return redirectError("account_inactive")
		}
		value, err := a.sessionToken(r.Context(), user["id"])
		if err != nil {
			return err
		}
		session := token(32)
		pending, _ := json.Marshal(Object{"user_id": user["id"], "token": value})
		if err = a.Redis.Set(r.Context(), "oauth:pending:"+session, pending, c.TTL).Err(); err != nil {
			return &apiError{503, "登录服务暂不可用"}
		}
		http.SetCookie(w, &http.Cookie{Name: "resume_oauth", Value: session, Path: "/api/auth/w3/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(c.TTL.Seconds())})
		http.Redirect(w, r, addQuery(c.Frontend, map[string]string{"oauth2": "success"}), 302)
		return nil
	}
	if path == "complete" && r.Method == "POST" {
		raw, err := a.Redis.GetDel(r.Context(), "oauth:pending:"+cookie.Value).Bytes()
		var pending Object
		if err != nil || json.Unmarshal(raw, &pending) != nil {
			return bad("W3 登录凭据无效或已过期，请重新登录")
		}
		user, err := one(r.Context(), a.Pool, "SELECT row_to_json(u) FROM accounts_user u JOIN authtoken_token t ON t.user_id=u.id WHERE u.id=$1 AND t.key=$2 AND u.is_active", pending["user_id"], pending["token"])
		if err != nil {
			return bad("W3 登录账号不可用，请联系管理员")
		}
		p, err := a.userPrincipal(r.Context(), user)
		if err != nil {
			return err
		}
		http.SetCookie(w, &http.Cookie{Name: "resume_oauth", Value: "", Path: "/api/auth/w3/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		write(w, 200, Object{"token": pending["token"], "user": a.me(r.Context(), p)})
		return nil
	}
	return &apiError{405, "请求方法不允许"}
}
func nested(value Object, path string) any {
	var v any = value
	for _, part := range strings.Split(path, ".") {
		v = obj(v)[part]
	}
	return v
}
func (a *App) oauthRequest(req *http.Request) (Object, error) {
	ctx, cancel := context.WithTimeout(req.Context(), oauthTimeout())
	defer cancel()
	client := *a.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, bad("上游身份服务连接失败")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, bad("上游身份服务拒绝请求")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var value Object
	if json.Unmarshal(raw, &value) != nil {
		return nil, bad("身份服务返回内容无效")
	}
	return value, nil
}

func oauthTimeout() time.Duration {
	value, err := strconv.ParseFloat(env("W3_OAUTH2_TIMEOUT_SECONDS", "10"), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 86400 {
		return 0
	}
	return time.Duration(value * float64(time.Second))
}
