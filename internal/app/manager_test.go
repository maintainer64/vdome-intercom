package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"vdome/internal/domain"
)

type fakeProvider struct {
	calls              []domain.Call
	opens              int
	failOpen, failPoll bool
}

func (*fakeProvider) Name() string { return "test" }
func (*fakeProvider) Login(_ context.Context, c domain.Credentials) (domain.Login, error) {
	return domain.Login{Auth: &domain.Auth{Subject: c.Phone, AccessToken: "secret"}}, nil
}
func (f *fakeProvider) Devices(context.Context, *domain.Auth) ([]domain.Device, error) {
	if f.failPoll {
		return nil, errors.New("network")
	}
	return []domain.Device{{ID: "door", APIStatus: "online", CanOpen: true}}, nil
}
func (f *fakeProvider) Calls(context.Context, *domain.Auth) ([]domain.Call, error) {
	return append([]domain.Call{}, f.calls...), nil
}
func (f *fakeProvider) Open(context.Context, *domain.Auth, string, string) error {
	f.opens++
	if f.failOpen {
		return errors.New("connection reset after request sent")
	}
	return nil
}
func (*fakeProvider) Snapshot(context.Context, *domain.Auth, string) ([]byte, string, error) {
	return nil, "", nil
}

type memoryStore struct {
	data []byte
	fail bool
}

func (s *memoryStore) Load() ([]byte, error) { return s.data, nil }
func (s *memoryStore) Save(data []byte) error {
	if s.fail {
		return errors.New("disk full")
	}
	s.data = append([]byte{}, data...)
	return nil
}

func setup(t *testing.T) (*Manager, *fakeProvider, string, *time.Time) {
	t.Helper()
	f := &fakeProvider{}
	m, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	_, token, err := m.Login(context.Background(), domain.Credentials{Phone: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	return m, f, token, &now
}
func arm(t *testing.T, m *Manager, token string, mode domain.Mode) {
	t.Helper()
	if err := m.SetMode(token, "door", mode); err != nil {
		t.Fatal(err)
	}
}
func call(f *fakeProvider, now time.Time, id string) {
	f.calls = append(f.calls, domain.Call{ID: id, DeviceID: "door", Kind: "incoming", At: now})
}
func poll(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNextConsumesOnceAndDeduplicates(t *testing.T) {
	m, f, token, now := setup(t)
	arm(t, m, token, domain.Next)
	*now = now.Add(time.Second)
	call(f, *now, "a")
	poll(t, m)
	*now = now.Add(11 * time.Second)
	call(f, *now, "b")
	poll(t, m)
	v, _ := m.View(token)
	if f.opens != 1 || v.Rules["door"].Mode != domain.Off {
		t.Fatalf("opens=%d rule=%+v", f.opens, v.Rules["door"])
	}
}
func TestAlwaysOpensEachNewCallWithCooldown(t *testing.T) {
	m, f, token, now := setup(t)
	arm(t, m, token, domain.Always)
	*now = now.Add(time.Second)
	call(f, *now, "a")
	poll(t, m)
	call(f, *now, "burst")
	poll(t, m)
	*now = now.Add(11 * time.Second)
	call(f, *now, "b")
	poll(t, m)
	poll(t, m)
	if f.opens != 2 {
		t.Fatalf("opens=%d", f.opens)
	}
}
func TestIgnoreStaleFutureOutgoingForeignAndPreArmCalls(t *testing.T) {
	m, f, token, now := setup(t)
	arm(t, m, token, domain.Always)
	*now = now.Add(2 * time.Second)
	f.calls = []domain.Call{
		{ID: "stale", DeviceID: "door", Kind: "incoming", At: now.Add(-31 * time.Second)},
		{ID: "future", DeviceID: "door", Kind: "incoming", At: now.Add(10 * time.Second)},
		{ID: "outgoing", DeviceID: "door", Kind: "outgoing", At: *now},
		{ID: "foreign", DeviceID: "other", Kind: "incoming", At: *now},
		{ID: "before-arm", DeviceID: "door", Kind: "incoming", At: now.Add(-3 * time.Second)},
		{ID: "zero", DeviceID: "door", Kind: "incoming"},
		{DeviceID: "door", Kind: "incoming", At: *now},
	}
	poll(t, m)
	if f.opens != 0 {
		t.Fatalf("opens=%d", f.opens)
	}
}
func TestAmbiguousOpenNeverRetriesAndConsumesNext(t *testing.T) {
	m, f, token, now := setup(t)
	f.failOpen = true
	arm(t, m, token, domain.Next)
	*now = now.Add(time.Second)
	call(f, *now, "a")
	poll(t, m)
	*now = now.Add(12 * time.Second)
	poll(t, m)
	v, _ := m.View(token)
	if f.opens != 1 || v.Rules["door"].Mode != domain.Off || v.Audit[0].Result != "unconfirmed" {
		t.Fatalf("opens=%d view=%+v", f.opens, v)
	}
}
func TestNoAutoOpenWhenStorageFails(t *testing.T) {
	m, f, token, now := setup(t)
	store := &memoryStore{}
	m.store = store
	arm(t, m, token, domain.Always)
	store.fail = true
	*now = now.Add(time.Second)
	call(f, *now, "a")
	if m.Poll(context.Background()) == nil {
		t.Fatal("expected storage error")
	}
	if f.opens != 0 {
		t.Fatal("opened without durable claim")
	}
}
func TestRestartDrainsBacklogAndPreservesAlways(t *testing.T) {
	m, f, token, now := setup(t)
	store := &memoryStore{}
	m.store = store
	arm(t, m, token, domain.Always)
	*now = now.Add(time.Second)
	call(f, *now, "during-restart")
	restarted, err := New(f, store)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = m.now
	poll(t, restarted)
	if f.opens != 0 {
		t.Fatal("opened backlog")
	}
	*now = now.Add(time.Second)
	call(f, *now, "new")
	poll(t, restarted)
	if f.opens != 1 {
		t.Fatalf("opens=%d", f.opens)
	}
}
func TestNetworkFailureDoesNotOpenOrConsumeNext(t *testing.T) {
	m, f, token, now := setup(t)
	arm(t, m, token, domain.Next)
	f.failPoll = true
	*now = now.Add(time.Second)
	call(f, *now, "a")
	if m.Poll(context.Background()) == nil {
		t.Fatal("expected network error")
	}
	v, _ := m.View(token)
	if f.opens != 0 || v.Connected || v.Rules["door"].Mode != domain.Next {
		t.Fatal("invalid failure state")
	}
}
func TestConcurrentPollsDoNotOpenTwice(t *testing.T) {
	m, f, token, now := setup(t)
	arm(t, m, token, domain.Always)
	*now = now.Add(time.Second)
	call(f, *now, "a")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.Poll(context.Background()) }()
	}
	wg.Wait()
	if f.opens != 1 {
		t.Fatalf("opens=%d", f.opens)
	}
}
func TestReloginRevokesOldSessionAndRules(t *testing.T) {
	m, _, token, _ := setup(t)
	arm(t, m, token, domain.Always)
	_, fresh, err := m.Login(context.Background(), domain.Credentials{Phone: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.View(token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("old session still active")
	}
	v, err := m.View(fresh)
	if err != nil || len(v.Rules) != 0 {
		t.Fatal("rules survived reauthentication")
	}
}
func TestUnauthorizedAndForeignDevice(t *testing.T) {
	m, _, token, _ := setup(t)
	if m.SetMode("random", "door", domain.Always) == nil {
		t.Fatal("anonymous mode update")
	}
	if m.SetMode(token, "someone-elses-door", domain.Always) == nil {
		t.Fatal("foreign device mode update")
	}
	if m.Open(context.Background(), token, "someone-elses-door") == nil {
		t.Fatal("foreign open")
	}
	if err := m.Logout(token); err != nil {
		t.Fatal(err)
	}
	if _, err := m.View(token); err == nil {
		t.Fatal("logout did not revoke session")
	}
}
func TestManualAndAutoShareCooldown(t *testing.T) {
	m, f, token, now := setup(t)
	arm(t, m, token, domain.Always)
	if err := m.Open(context.Background(), token, "door"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	call(f, *now, "a")
	poll(t, m)
	if f.opens != 1 {
		t.Fatal("manual/auto burst was not rate limited")
	}
}
func TestAccountsDoNotShareRules(t *testing.T) {
	m, f, first, now := setup(t)
	arm(t, m, first, domain.Always)
	_, second, err := m.Login(context.Background(), domain.Credentials{Phone: "another-user"})
	if err != nil {
		t.Fatal(err)
	}
	poll(t, m)
	*now = now.Add(time.Second)
	call(f, *now, "a")
	poll(t, m)
	v, _ := m.View(second)
	if f.opens != 1 || len(v.Rules) != 0 || len(v.Audit) != 0 {
		t.Fatal("account state leaked")
	}
}

type cachedProvider struct {
	*fakeProvider
	refreshes int
}

func (*cachedProvider) DeviceRefreshInterval() time.Duration { return time.Minute }
func (p *cachedProvider) Devices(ctx context.Context, a *domain.Auth) ([]domain.Device, error) {
	p.refreshes++
	return p.fakeProvider.Devices(ctx, a)
}
func TestDevicePollingUsesCacheAndFailureBackoff(t *testing.T) {
	p := &cachedProvider{fakeProvider: &fakeProvider{}}
	m, err := New(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	now := time.Now()
	m.now = func() time.Time { return now }
	_, token, err := m.Login(context.Background(), domain.Credentials{Phone: "user"})
	if err != nil {
		t.Fatal(err)
	}
	poll(t, m)
	if err := m.SetMode(token, "door", domain.Always); err != nil {
		t.Fatal(err)
	}
	// Calls still flow while device metadata is cached.
	now = now.Add(3 * time.Second)
	call(p.fakeProvider, now, "new")
	poll(t, m)
	for range 18 {
		now = now.Add(3 * time.Second)
		poll(t, m)
	}
	if p.refreshes != 1 || p.opens != 1 {
		t.Fatalf("refreshes=%d opens=%d", p.refreshes, p.opens)
	}
	now = now.Add(3 * time.Second)
	p.failPoll = true
	if m.Poll(context.Background()) == nil {
		t.Fatal("expected upstream failure")
	}
	for range 4 {
		now = now.Add(3 * time.Second)
		poll(t, m)
	}
	if p.refreshes != 2 {
		t.Fatalf("retried during backoff: %d", p.refreshes)
	}
	now = now.Add(3 * time.Second)
	p.failPoll = false
	poll(t, m)
	if p.refreshes != 3 {
		t.Fatalf("did not recover after backoff: %d", p.refreshes)
	}
}
func TestMediaAuthorizationDoesNotWaitForPollingAndRevokes(t *testing.T) {
	m, _, token, _ := setup(t)
	defer m.Close()
	m.mu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := m.StreamFile(token, "absent", "index.m3u8"); done <- err }()
	select {
	case err := <-done:
		if errors.Is(err, domain.ErrUnauthorized) {
			t.Error("valid session denied")
		}
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("media waits for network poll lock")
	}
	m.mu.Unlock()
	if err := m.Logout(token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.StreamFile(token, "absent", "index.m3u8"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("logout did not revoke media")
	}
}
