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
}
type View struct {
	LoginURL      string                 `json:"loginUrl,omitempty"`
	Authenticated bool                   `json:"authenticated"`
	Provider      string                 `json:"provider"`
	Subject       string                 `json:"subject,omitempty"`
	Persistent    bool                   `json:"persistent"`
	Connected     bool                   `json:"connected"`
	CallReady     bool                   `json:"callReady"`
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
			s.RefreshFailures++
			delay := 15 * time.Second << min(s.RefreshFailures-1, 3)
			s.NextRefresh = now.Add(delay)
			var limited *domain.RateLimitError
			if errors.As(err, &limited) && limited.Until.After(s.NextRefresh) {
				s.NextRefresh = limited.Until
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
		s.LastSync = now.UTC()
	}
	if !s.Connected {
		return nil
	}
	calls, err := m.p.Calls(ctx, &s.Auth)
	if err != nil {
		s.CallReady = false
		return err
	}
	s.CallReady = true
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
		if c.ID == "" || c.Kind != "incoming" || c.At.IsZero() || c.At.After(now.Add(2*time.Second)) || now.Sub(c.At) > 30*time.Second || !c.At.After(r.ArmedAt) {
			continue
		}
		if r.Mode != domain.Next && r.Mode != domain.Always {
			continue
		}
		d, err := device(s, c.DeviceID)
		if err != nil || !d.CanOpen || d.APIStatus != "online" {
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
