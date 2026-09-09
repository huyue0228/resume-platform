package platform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestW3OAuthPKCEMapsExistingAccountOnce(t *testing.T) {
	a := integrationApp(t)
	p := adminPrincipal(t, a)
	var challenge string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.ParseForm()
			user, password, ok := r.BasicAuth()
			if !ok || user != "fixture-client" || password != "fixture-secret" {
				t.Error("OAuth client authentication changed")
			}
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
				t.Error("PKCE verifier mismatch")
			}
			write(w, 200, Object{"access_token": "fixture-access"})
		case "/userinfo":
			if r.URL.Query().Get("access_token") != "fixture-access" {
				t.Error("W3 access token query missing")
			}
			write(w, 200, Object{"employeeNumber": p.User["username"], "email": strings.ToUpper(str(p.User["email"]))})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	for key, value := range map[string]string{"DJANGO_DEBUG": "True", "W3_OAUTH2_ENABLED": "True", "W3_OAUTH2_CLIENT_ID": "fixture-client", "W3_OAUTH2_CLIENT_SECRET": "fixture-secret", "W3_OAUTH2_AUTHORIZE_URL": provider.URL + "/authorize", "W3_OAUTH2_TOKEN_URL": provider.URL + "/token", "W3_OAUTH2_USERINFO_URL": provider.URL + "/userinfo", "W3_OAUTH2_REDIRECT_URI": "http://localhost/api/auth/w3/callback/", "W3_OAUTH2_FRONTEND_CALLBACK_URL": "/login", "W3_OAUTH2_USE_PKCE": "True"} {
		t.Setenv(key, value)
	}
	handler := a.Handler(http.NotFoundHandler())
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest("GET", "/api/auth/w3/start/", nil))
	if start.Code != 302 {
		t.Fatalf("start %d %s", start.Code, start.Body.String())
	}
	location, _ := url.Parse(start.Header().Get("Location"))
	state := location.Query().Get("state")
	challenge = location.Query().Get("code_challenge")
	cookies := start.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("login cookie protection changed")
	}
	callbackRequest := httptest.NewRequest("GET", "/api/auth/w3/callback/?code=fixture-code&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(cookies[0])
	callback := httptest.NewRecorder()
	handler.ServeHTTP(callback, callbackRequest)
	if callback.Code != 302 || callback.Header().Get("Location") != "/login?oauth2=success" {
		t.Fatalf("callback failed: status=%d redirect=%s", callback.Code, callback.Header().Get("Location"))
	}
	if strings.Contains(callback.Header().Get("Location"), "token=") {
		t.Fatal("session token leaked in redirect")
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, callbackRequest)
	if !strings.Contains(replay.Header().Get("Location"), "state_invalid") {
		t.Fatal("OAuth callback replay accepted")
	}
	completeRequest := httptest.NewRequest("POST", "/api/auth/w3/complete/", nil)
	completeRequest.AddCookie(callback.Result().Cookies()[0])
	complete := httptest.NewRecorder()
	handler.ServeHTTP(complete, completeRequest)
	result := responseObject(t, complete, 200)
	existing, err := a.sessionToken(context.Background(), p.User["id"])
	if err != nil {
		t.Fatal(err)
	}
	if str(result["token"]) != existing || num(obj(result["user"])["id"]) != num(p.User["id"]) {
		t.Fatal("OAuth did not reuse existing account/session")
	}
	replay = httptest.NewRecorder()
	handler.ServeHTTP(replay, completeRequest)
	if replay.Code != 400 {
		t.Fatal("OAuth completion replay accepted")
	}
	if _, err = a.IssueDevToken(context.Background(), str(p.User["username"])); err == nil {
		t.Fatal("W3-ready deployment allowed development token issuance")
	}
}
func TestW3RequiresTLSAndSafeCallback(t *testing.T) {
	t.Setenv("W3_OAUTH2_ENABLED", "true")
	t.Setenv("W3_OAUTH2_CLIENT_ID", "fixture")
	t.Setenv("W3_OAUTH2_CLIENT_SECRET", "fixture")
	t.Setenv("DJANGO_DEBUG", "false")
	for _, key := range []string{"AUTHORIZE_URL", "TOKEN_URL", "USERINFO_URL"} {
		t.Setenv("W3_OAUTH2_"+key, "http://untrusted.test/oauth")
	}
	t.Setenv("W3_OAUTH2_REDIRECT_URI", "https://resume.test/api/auth/w3/callback/")
	if oauthSettings().ready() {
		t.Fatal("production W3 accepted plaintext OAuth credentials")
	}
}
