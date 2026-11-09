package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRejectsCSRFUnknownFieldsAndLeaksNoTokens(t *testing.T) {
	m, _, token, _ := setup(t)
	h, err := Handler(m, "http://127.0.0.1:8080", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://attacker.test", ""} {
		r := httptest.NewRequest("POST", "/api/auth/logout", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("CSRF status=%d", w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Result().Body)
	if w.Code != 200 || strings.Contains(string(b), "secret") || strings.Contains(string(b), token) || strings.Contains(string(b), "access_token") {
		t.Fatalf("leaking response: %s", b)
	}
	r = httptest.NewRequest("PUT", "/api/devices/door/mode", strings.NewReader(`{"mode":"always","url":"https://attacker.test"}`))
	r.Header.Set("X-VDome-Request", "1")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("unknown field accepted: %d", w.Code)
	}
}
func TestLoginCookieAndSessionIsolation(t *testing.T) {
	m, _, _, _ := setup(t)
	h, _ := Handler(m, "https://example.test", t.TempDir())
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"phone":"second"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-VDome-Request", "1")
	r.Header.Set("Origin", "https://example.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	if w.Code != 200 || len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("invalid cookie: %+v", cookies)
	}
	if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), cookies[0].Value) {
		t.Fatal("login body contains token")
	}
}

func TestLoopbackOriginAliasesKeepPortSchemeAndCSRFChecks(t *testing.T) {
	m, _, _, _ := setup(t)
	defer m.Close()
	h, err := Handler(m, "http://127.0.0.1:8081", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		origin, site, header string
		status               int
	}{
		{"http://localhost:8081", "same-origin", "1", 200},
		{"http://127.0.0.1:8081", "same-origin", "1", 200},
		{"http://[::1]:8081", "same-origin", "1", 200},
		{"http://localhost:8080", "same-origin", "1", 403},
		{"https://localhost:8081", "same-origin", "1", 403},
		{"http://localhost.attacker.test:8081", "same-origin", "1", 403},
		{"http://localhost:8081", "cross-site", "1", 403},
		{"http://localhost:8081", "same-origin", "", 403},
		{"null", "same-origin", "1", 403},
		{"http://localhost:8081/extra", "same-origin", "1", 403},
	} {
		r := httptest.NewRequest("POST", "/api/auth/logout", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Sec-Fetch-Site", tc.site)
		r.Header.Set("X-VDome-Request", tc.header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("origin=%s site=%s: got %d want %d", tc.origin, tc.site, w.Code, tc.status)
		}
	}
	public, _ := Handler(m, "https://intercom.example", t.TempDir())
	r := httptest.NewRequest("POST", "/api/auth/logout", nil)
	r.Header.Set("Origin", "https://localhost")
	r.Header.Set("X-VDome-Request", "1")
	w := httptest.NewRecorder()
	public.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("local alias bypassed public origin")
	}
}

func TestReauthRequiresExistingSessionAndPreservesRules(t *testing.T) {
	m, _, session, _ := setup(t)
	arm(t, m, session, "always")
	h, err := Handler(m, "http://127.0.0.1:8081", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cookie, phone string
		want          int
	}{{"", "user", 401}, {session, "different-account", 502}, {session, "user", 200}} {
		req := httptest.NewRequest("POST", "/api/auth/reauth", strings.NewReader(`{"phone":"`+tc.phone+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-VDome-Request", "1")
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: tc.cookie})
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != tc.want {
			t.Fatalf("reauth status=%d want=%d", response.Code, tc.want)
		}
		if strings.Contains(response.Body.String(), "secret") {
			t.Fatal("credentials exposed")
		}
	}
	v, _ := m.View(session)
	if v.Rules["door"].Mode != "always" {
		t.Fatal("reauth erased existing rule")
	}
}
