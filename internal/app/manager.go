package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	"vdome/internal/domain"
	"vdome/internal/media"
)

type Storage interface {
	Load() ([]byte, error)
	Save([]byte) error
}
type Session struct {
	NextRefresh     time.Time              `json:"-"`
	RefreshFailures int                    `json:"-"`
	Auth            domain.Auth            `json:"auth"`
	ExpiresAt       time.Time              `json:"expiresAt"`
	Rules           map[string]domain.Rule `json:"rules"`
	Seen            map[string]time.Time   `json:"seen"`
	Audit           []domain.Audit         `json:"audit"`
	Devices         []domain.Device        `json:"-"`
	LastSync        time.Time              `json:"-"`
	Connected       bool                   `json:"-"`
	CallReady       bool                   `json:"-"`
	Ready           bool                   `json:"-"`
	NeedsLogin      bool                   `json:"-"`
	SyncError       string                 `json:"-"`
	IncomingCalls   []domain.IncomingCall  `json:"-"`
}
type View struct {
	LoginURL      string                 `json:"loginUrl,omitempty"`
	Authenticated bool                   `json:"authenticated"`
	Provider      string                 `json:"provider"`
	Subject       string                 `json:"subject,omitempty"`
	Persistent    bool                   `json:"persistent"`
	Connected     bool                   `json:"connected"`
	CallReady     bool                   `json:"callReady"`
	NeedsLogin    bool                   `json:"needsLogin"`
	SyncError     string                 `json:"syncError,omitempty"`
	CallStatus    domain.CallStatus      `json:"callStatus"`
	IncomingCalls []domain.IncomingCall  `json:"incomingCalls"`
	LastSync      time.Time              `json:"lastSync"`
	Devices       []domain.Device        `json:"devices"`
	Rules         map[string]domain.Rule `json:"rules"`
	Audit         []domain.Audit         `json:"audit"`
}

type Manager struct {
	authIndex sync.Map // session hash -> immutable expiry; media reads never wait on network polling
	media     *media.Manager
	mu        sync.Mutex
	p         domain.Provider
	store     Storage
	sessions  map[string]*Session
	now       func() time.Time
}

func New(p domain.Provider, store Storage) (*Manager, error) {
	m := &Manager{p: p, store: store, sessions: map[string]*Session{}, now: time.Now, media: media.New()}
	if store != nil {
		b, err := store.Load()
		if err != nil {
			return nil, fmt.Errorf("load vault: %w", err)
		}
		if len(b) != 0 {
			if err := json.Unmarshal(b, &m.sessions); err != nil {
				return nil, err
			}
		}
		for id, s := range m.sessions {
			if s == nil || s.Auth.Subject == "" || s.Rules == nil || s.Seen == nil {
				return nil, errors.New("invalid session vault")
			}
			if !s.ExpiresAt.After(m.now()) {
				delete(m.sessions, id)
			}
		}
	}
	for id, s := range m.sessions {
		m.authIndex.Store(id, s.ExpiresAt)
	}
	return m, nil
}
func (m *Manager) ProviderName() string { return m.p.Name() }

// ConnectionReport is safe for logs: no subjects, tokens, SIP identities or URLs.
func (m *Manager) ConnectionReport() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	type report struct {
		Connected  bool              `json:"connected"`
		CallsReady bool              `json:"callsReady"`
		NeedsLogin bool              `json:"needsLogin"`
		Calls      domain.CallStatus `json:"calls"`
	}
	keys := make([]string, 0, len(m.sessions))
	for key := range m.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	reports := make([]report, 0, len(keys))
	for _, key := range keys {
		s := m.sessions[key]
		r := report{Connected: s.Connected, CallsReady: s.CallReady, NeedsLogin: s.NeedsLogin}
		if p, ok := m.p.(interface {
			CallStatus(string) domain.CallStatus
		}); ok {
			r.Calls = p.CallStatus(s.Auth.Subject)
		}
		// Keep routine heartbeat/renewal churn out of diagnostic logs.
		r.Calls.RegisteredAt = time.Time{}
		r.Calls.Pongs = 0
		if r.Calls.State == "registered" && r.Calls.SIPStatus == 401 {
			r.Calls.SIPStatus = 200
		}
		reports = append(reports, r)
	}
	b, _ := json.Marshal(reports)
	return string(b)
}
func sessionKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func newID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (m *Manager) save() error {
	if m.store == nil {
		return nil
	}
	b, err := json.Marshal(m.sessions)
	if err != nil {
		return err
	}
	return m.store.Save(b)
}
func (m *Manager) session(token string) (*Session, error) {
	s := m.sessions[sessionKey(token)]
	if s == nil || !s.ExpiresAt.After(m.now()) {
		return nil, domain.ErrUnauthorized
	}
	return s, nil
}

func (m *Manager) Login(ctx context.Context, c domain.Credentials) (challenge, token string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.p.Login(ctx, c)
	if err != nil {
		return "", "", err
	}
	if l.Auth == nil {
		if l.Challenge == "" {
			return "", "", errors.New("empty login response")
		}
		return l.Challenge, "", nil
	}
	if l.Auth.Subject == "" || l.Auth.AccessToken == "" {
		return "", "", errors.New("invalid auth response")
	}
	// One active app session per provider account prevents two independent
	// workers from opening the same door for the same call.
	old := make(map[string]*Session, len(m.sessions))
	for k, v := range m.sessions {
		old[k] = v
	}
	for id, s := range m.sessions {
		if s.Auth.Subject == l.Auth.Subject || !s.ExpiresAt.After(m.now()) {
			delete(m.sessions, id)
		}
	}
	if len(m.sessions) >= 16 {
		m.sessions = old
		return "", "", errors.New("too many sessions")
	}
	token = newID()
	m.sessions[sessionKey(token)] = &Session{Auth: *l.Auth, ExpiresAt: m.now().Add(30 * 24 * time.Hour), Rules: map[string]domain.Rule{}, Seen: map[string]time.Time{}, Audit: []domain.Audit{}, Devices: []domain.Device{}}
	if err := m.save(); err != nil {
		m.sessions = old
		return "", "", err
	}
	for id, previous := range old {
		if m.sessions[id] == nil {
			m.authIndex.Delete(id)
			m.media.StopOwner(id)
			m.disconnect(previous.Auth.Subject)
		}
	}
	m.authIndex.Store(sessionKey(token), m.sessions[sessionKey(token)].ExpiresAt)
	return "", token, nil
}

func (m *Manager) Logout(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := sessionKey(token)
	old := m.sessions[id]
	delete(m.sessions, id)
	if err := m.save(); err != nil {
		if old != nil {
			m.sessions[id] = old
		}
		return err
	}
	m.authIndex.Delete(id)
	if old != nil {
		m.disconnect(old.Auth.Subject)
		m.media.StopOwner(id)
	}
	return nil
}

// Reauthenticate updates credentials for the same account without discarding
// its rules or audit trail. A full logout/login still starts with all rules off.
func (m *Manager) Reauthenticate(ctx context.Context, token string, c domain.Credentials) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(token)
	if err != nil {
		return err
	}
	login, err := m.p.Login(ctx, c)
	if err != nil {
		return err
	}
	if login.Auth == nil || login.Auth.Subject != s.Auth.Subject || login.Auth.AccessToken == "" {
		if login.Auth != nil && login.Auth.Subject != s.Auth.Subject {
			inUse := false
			for _, other := range m.sessions {
				if other.Auth.Subject == login.Auth.Subject {
					inUse = true
					break
				}
			}
			if !inUse {
				m.disconnect(login.Auth.Subject)
			}
		}
		return errors.New("reauthentication account mismatch")
	}
	old := s.Auth
	s.Auth = *login.Auth
	if err := m.save(); err != nil {
		s.Auth = old
		return err
	}
	m.disconnect(s.Auth.Subject)
	m.media.StopOwner(sessionKey(token))
	s.NextRefresh = time.Time{}
	s.RefreshFailures = 0
	s.Ready, s.Connected, s.CallReady, s.NeedsLogin = false, false, false, false
	s.SyncError = ""
	s.IncomingCalls = nil
	return nil
}
func (m *Manager) View(token string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := View{Provider: m.p.Name(), Persistent: m.store != nil, Devices: []domain.Device{}, Rules: map[string]domain.Rule{}, Audit: []domain.Audit{}}
	if login, ok := m.p.(interface{ LoginURL() string }); ok {
		v.LoginURL = login.LoginURL()
	}
	s, err := m.session(token)
	if err != nil {
		return v, err
	}
	v.Authenticated = true
	v.Subject = s.Auth.Subject
	v.Connected = s.Connected
	v.CallReady = s.CallReady
	v.NeedsLogin = s.NeedsLogin
	v.SyncError = s.SyncError
	v.IncomingCalls = append([]domain.IncomingCall{}, s.IncomingCalls...)
	if p, ok := m.p.(interface {
		CallStatus(string) domain.CallStatus
	}); ok {
		v.CallStatus = p.CallStatus(s.Auth.Subject)
	}
	v.LastSync = s.LastSync
	v.Devices = append(v.Devices, s.Devices...)
	v.Audit = append(v.Audit, s.Audit...)
	for k, rule := range s.Rules {
		v.Rules[k] = rule
	}
	return v, nil
}

func (m *Manager) disconnect(subject string) {
	if p, ok := m.p.(interface{ Disconnect(string) }); ok {
		p.Disconnect(subject)
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		m.disconnect(s.Auth.Subject)
	}
	m.media.Close()
}
func device(s *Session, id string) (domain.Device, error) {
	for _, d := range s.Devices {
		if d.ID == id {
			return d, nil
		}
	}
	return domain.Device{}, errors.New("device does not belong to this account")
}
func addAudit(s *Session, a domain.Audit) {
	s.Audit = append([]domain.Audit{a}, s.Audit...)
	if len(s.Audit) > 100 {
		s.Audit = s.Audit[:100]
	}
}

func (m *Manager) SetMode(token, id string, mode domain.Mode) error {
	if mode != domain.Off && mode != domain.Next && mode != domain.Always {
		return errors.New("unknown mode")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(token)
	if err != nil {
		return err
	}
	d, err := device(s, id)
	if err != nil {
		return err
	}
	if mode != domain.Off && (!s.Ready || !s.Connected || !s.CallReady || !d.CanOpen || d.APIStatus != "online") {
		return errors.New("device unavailable")
	}
	old, existed := s.Rules[id]
	r := domain.Rule{Mode: mode, ArmedAt: m.now().UTC(), LastAttempt: old.LastAttempt}
	s.Rules[id] = r
	if err := m.save(); err != nil {
		if existed {
			s.Rules[id] = old
		} else {
			delete(s.Rules, id)
		}
		return err
	}
	return nil
}

func (m *Manager) Open(ctx context.Context, token, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(token)
	if err != nil {
		return err
	}
	d, err := device(s, id)
	if err != nil {
		return err
	}
	if !s.Connected || !d.CanOpen || d.APIStatus != "online" {
		return errors.New("device unavailable")
	}
	r := s.Rules[id]
	now := m.now().UTC()
	if !r.LastAttempt.IsZero() && now.Sub(r.LastAttempt) < 10*time.Second {
		return errors.New("cooldown: подождите 10 секунд между командами")
	}
	old := r
	r.LastAttempt = now
	s.Rules[id] = r
	if err := m.save(); err != nil {
		s.Rules[id] = old
		return err
	}
	err = m.p.Open(ctx, &s.Auth, id, newID())
	result := "accepted"
	if err != nil {
		result = "unconfirmed"
	}
	addAudit(s, domain.Audit{At: now, DeviceID: id, Action: "manual", Result: result})
	if saveErr := m.save(); saveErr != nil {
		return saveErr
	}
	return err
}

func (m *Manager) Simulate(token, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.session(token)
	if err != nil {
		return err
	}
	demo, ok := m.p.(interface {
		Simulate(*domain.Auth, string) error
	})
	if !ok {
		return errors.New("only available in demo mode")
	}
	if _, err := device(s, id); err != nil {
		return err
	}
	return demo.Simulate(&s.Auth, id)
}

func (m *Manager) Snapshot(ctx context.Context, token, id string) ([]byte, string, error) {
	m.mu.Lock()
	s, err := m.session(token)
	if err != nil {
		m.mu.Unlock()
		return nil, "", err
	}
	d, err := device(s, id)
	if err != nil || !d.HasCamera {
		m.mu.Unlock()
		return nil, "", errors.New("camera unavailable")
	}
	auth := s.Auth
	m.mu.Unlock()
	return m.p.Snapshot(ctx, &auth, id)
}

// Poll keeps automation alive when the browser is closed. The first successful
// poll after a start/login drains the backlog without opening anything.
func (m *Manager) Poll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var failures []error
	for id, s := range m.sessions {
		if !s.ExpiresAt.After(m.now()) {
			m.disconnect(s.Auth.Subject)
			m.media.StopOwner(id)
			delete(m.sessions, id)
			m.authIndex.Delete(id)
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := m.poll(pollCtx, s)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	if err := m.save(); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (m *Manager) poll(ctx context.Context, s *Session) error {
	if s.NeedsLogin {
		return nil
	}
	interval := time.Duration(0)
	if p, ok := m.p.(interface{ DeviceRefreshInterval() time.Duration }); ok {
		interval = p.DeviceRefreshInterval()
	}
	now := m.now()
	refreshToken := s.RefreshFailures == 0 && !s.Auth.ExpiresAt.IsZero() && !s.Auth.ExpiresAt.After(now.Add(30*time.Second))
	if !now.Before(s.NextRefresh) || refreshToken {
		devices, err := m.p.Devices(ctx, &s.Auth)
		if err != nil {
			s.Connected = false
			s.CallReady = false
			s.IncomingCalls = nil
			s.SyncError = "VDome недоступен; повторим подключение автоматически."
			if errors.Is(err, domain.ErrUnauthorized) {
				s.NeedsLogin = true
				s.SyncError = "МТС отклонил сессию. Обновите вход через МТС ID."
				m.disconnect(s.Auth.Subject)
				m.media.StopOwner(m.sessionID(s))
			}
			s.RefreshFailures++
			delay := 15 * time.Second << min(s.RefreshFailures-1, 3)
			s.NextRefresh = now.Add(delay)
			var limited *domain.RateLimitError
			if errors.As(err, &limited) && limited.Until.After(s.NextRefresh) {
				s.NextRefresh = limited.Until
				s.SyncError = limited.Error()
			}
			return err
		}
		if devices == nil {
			devices = []domain.Device{}
		}
		s.Devices = devices
		s.RefreshFailures = 0
		s.NextRefresh = now.Add(interval)
		s.Connected = true
		s.SyncError = ""
		s.LastSync = now.UTC()
	}
	if !s.Connected {
		return nil
	}
	calls, err := m.p.Calls(ctx, &s.Auth)
	if err != nil {
		s.CallReady = false
		s.IncomingCalls = nil
		return err
	}
	s.CallReady = true
	s.IncomingCalls = []domain.IncomingCall{}
	for _, c := range calls {
		if _, err := device(s, c.DeviceID); err == nil && c.ID != "" && c.Kind == "incoming" && !c.Ended && !c.At.IsZero() && !c.At.After(now.Add(2*time.Second)) && now.Sub(c.At) <= 30*time.Second {
			s.IncomingCalls = append(s.IncomingCalls, domain.IncomingCall{DeviceID: c.DeviceID, At: c.At})
		}
	}
	if !s.Ready {
		for _, c := range calls {
			s.Seen[c.DeviceID+"\x00"+c.ID] = m.now()
		}
		s.Ready = true
		return nil
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].At.Before(calls[j].At) })
	for _, c := range calls {
		key := c.DeviceID + "\x00" + c.ID
		if _, ok := s.Seen[key]; ok {
			continue
		}
		now := m.now().UTC()
		s.Seen[key] = now
		r := s.Rules[c.DeviceID]
		if c.ID == "" || c.Kind != "incoming" || c.At.IsZero() || c.At.After(now.Add(2*time.Second)) || now.Sub(c.At) > 30*time.Second {
			continue
		}
		d, err := device(s, c.DeviceID)
		if err != nil {
			continue
		}
		addAudit(s, domain.Audit{At: c.At, DeviceID: c.DeviceID, CallID: c.ID, Action: "incoming", Result: "received"})
		if !c.At.After(r.ArmedAt) {
			continue
		}
		if r.Mode != domain.Next && r.Mode != domain.Always {
			continue
		}
		if !d.CanOpen || d.APIStatus != "online" {
			continue
		}
		if !r.LastAttempt.IsZero() && now.Sub(r.LastAttempt) < 10*time.Second {
			addAudit(s, domain.Audit{At: now, DeviceID: c.DeviceID, CallID: c.ID, Action: "auto", Result: "cooldown"})
			continue
		}
		old := r
		if r.Mode == domain.Next {
			r.Mode = domain.Off
		}
		r.LastAttempt = now
		s.Rules[c.DeviceID] = r
		// Claim and consume BEFORE I/O. Never retry an ambiguous unlock result.
		if err := m.save(); err != nil {
			s.Rules[c.DeviceID] = old
			return err
		}
		err = m.p.Open(ctx, &s.Auth, c.DeviceID, c.ID)
		result := "accepted"
		if err != nil {
			result = "unconfirmed"
		}
		addAudit(s, domain.Audit{At: now, DeviceID: c.DeviceID, CallID: c.ID, Action: "auto", Result: result})
		if err := m.save(); err != nil {
			return err
		}
	}
	for id, at := range s.Seen {
		if m.now().Sub(at) > 24*time.Hour {
			delete(s.Seen, id)
		}
	}
	return nil
}

// Caller holds m.mu; sessions are bounded to sixteen entries.
func (m *Manager) sessionID(target *Session) string {
	for id, s := range m.sessions {
		if s == target {
			return id
		}
	}
	return ""
}
