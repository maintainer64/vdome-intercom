package app

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"vdome/internal/domain"
)

const cookieName = "vdome_session"

type HTTP struct {
	m        *Manager
	origin   string
	secure   bool
	dist     string
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func Handler(m *Manager, origin, dist string) (http.Handler, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return nil, errors.New("invalid VDOME_ORIGIN")
	}
	h := &HTTP{m: m, origin: origin, secure: u.Scheme == "https", dist: dist, attempts: map[string][]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/state", h.state)
	mux.HandleFunc("POST /api/auth/login", h.login)
	mux.HandleFunc("POST /api/auth/reauth", h.reauthenticate)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
	mux.HandleFunc("PUT /api/devices/{id}/mode", h.mode)
	mux.HandleFunc("POST /api/devices/{id}/open", h.open)
	mux.HandleFunc("POST /api/devices/{id}/simulate-call", h.simulate)
	mux.HandleFunc("GET /api/devices/{id}/snapshot", h.snapshot)
	mux.HandleFunc("POST /api/devices/{id}/stream", h.startStream)
	mux.HandleFunc("GET /api/streams/{stream}/{file}", h.streamFile)
	mux.HandleFunc("DELETE /api/streams/{stream}", h.stopStream)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "Метод не найден"})
	})
	mux.HandleFunc("/", h.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; media-src 'self' blob:; worker-src 'self' blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-VDome-Request") != "1" || !originAllowed(r.Header.Get("Origin"), h.origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeJSON(w, 403, map[string]string{"error": "Недопустимый источник запроса"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	}), nil
}

// Browsers treat localhost and loopback IPs as separate origins. Accept local
// aliases only when the configured origin is local and scheme/port both match.
// Public origins remain exact; Sec-Fetch-Site and the custom header still apply.
func originAllowed(origin, configured string) bool {
	if origin == "" || origin == configured {
		return true
	}
	actual, err := url.Parse(origin)
	if err != nil {
		return false
	}
	expected, err := url.Parse(configured)
	if err != nil || actual.Scheme != expected.Scheme || actual.User != nil || actual.Path != "" || actual.RawQuery != "" || actual.Fragment != "" {
		return false
	}
	loopback := func(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return loopback(actual.Hostname()) && loopback(expected.Hostname()) && port(actual) == port(expected)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func token(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		writeJSON(w, 415, map[string]string{"error": "Ожидается application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		writeJSON(w, 400, map[string]string{"error": "Некорректный JSON"})
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "Ожидается один JSON-объект"})
		return false
	}
	return true
}
func apiError(w http.ResponseWriter, err error) {
	status, message := 502, "Операция не подтверждена. Проверьте связь и журнал; автоматический повтор не выполняется."
	if errors.Is(err, domain.ErrUnauthorized) {
		status = 401
		message = domain.ErrUnauthorized.Error()
	}
	if errors.Is(err, domain.ErrUnconfigured) {
		status = 503
		message = domain.ErrUnconfigured.Error()
	}
	if strings.HasPrefix(err.Error(), "cooldown:") {
		status = 409
		message = "Подождите 10 секунд между командами открытия"
	}
	var limited *domain.RateLimitError
	if errors.As(err, &limited) {
		status, message = 429, limited.Error()
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(limited.Until).Seconds())+1)))
	}
	writeJSON(w, status, map[string]string{"error": message})
}
func (h *HTTP) state(w http.ResponseWriter, r *http.Request) {
	v, err := h.m.View(token(r))
	if err != nil && !errors.Is(err, domain.ErrUnauthorized) {
		apiError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (h *HTTP) reauthenticate(w http.ResponseWriter, r *http.Request) {
	if !h.allowLogin(r) {
		writeJSON(w, 429, map[string]string{"error": "Слишком много попыток входа"})
		return
	}
	var c domain.Credentials
	if !decode(w, r, &c) {
		return
	}
	if err := h.m.Reauthenticate(r.Context(), token(r), c); err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"authenticated": true})
}
func (h *HTTP) allowLogin(r *http.Request) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	for key, attempts := range h.attempts {
		if len(attempts) == 0 || now.Sub(attempts[len(attempts)-1]) > time.Minute {
			delete(h.attempts, key)
		}
	}
	if len(h.attempts) > 1024 {
		return false
	}
	var recent []time.Time
	for _, at := range h.attempts[ip] {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= 10 {
		return false
	}
	h.attempts[ip] = append(recent, now)
	return true
}
func (h *HTTP) login(w http.ResponseWriter, r *http.Request) {
	if !h.allowLogin(r) {
		writeJSON(w, 429, map[string]string{"error": "Слишком много попыток. Подождите минуту"})
		return
	}
	var c domain.Credentials
	if !decode(w, r, &c) {
		return
	}
	challenge, value, err := h.m.Login(r.Context(), c)
	if err != nil {
		apiError(w, err)
		return
	}
	if value != "" {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", MaxAge: 30 * 24 * 3600, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteStrictMode})
	}
	writeJSON(w, 200, map[string]any{"challenge": challenge, "authenticated": value != ""})
}
func (h *HTTP) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.m.Logout(token(r)); err != nil {
		apiError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (h *HTTP) mode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode domain.Mode `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Mode != domain.Off && in.Mode != domain.Next && in.Mode != domain.Always {
		writeJSON(w, 400, map[string]string{"error": "Неизвестный режим"})
		return
	}
	if err := h.m.SetMode(token(r), r.PathValue("id"), in.Mode); err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (h *HTTP) open(w http.ResponseWriter, r *http.Request) {
	if err := h.m.Open(r.Context(), token(r), r.PathValue("id")); err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"result": "accepted"})
}
func (h *HTTP) simulate(w http.ResponseWriter, r *http.Request) {
	if err := h.m.Simulate(token(r), r.PathValue("id")); err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (h *HTTP) snapshot(w http.ResponseWriter, r *http.Request) {
	b, mime, err := h.m.Snapshot(r.Context(), token(r), r.PathValue("id"))
	if err != nil {
		apiError(w, err)
		return
	}
	if len(b) > 8<<20 || (mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" && !(mime == "image/svg+xml" && h.m.ProviderName() == "demo")) {
		writeJSON(w, 502, map[string]string{"error": "Неподдерживаемый кадр"})
		return
	}
	w.Header().Set("Content-Type", mime)
	_, _ = w.Write(b)
}
func (h *HTTP) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.Contains(r.URL.Path, "..") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(h.dist, filepath.Clean("/"+r.URL.Path))
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		http.ServeFile(w, r, p)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	index := filepath.Join(h.dist, "index.html")
	if _, err := os.Stat(index); err != nil {
		writeJSON(w, 503, map[string]string{"error": "Соберите интерфейс: cd web && npm ci && npm run build"})
		return
	}
	http.ServeFile(w, r, index)
}
