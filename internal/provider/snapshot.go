package provider

import (
	"context"
	"net/url"
	"time"
	"vdome/internal/domain"
)

type snapshotEntry struct {
	done    chan struct{}
	expires time.Time
	data    []byte
	mime    string
	err     error
}

// Coalesce duplicate cards and StrictMode mounts into one upstream request.
// A cancelled browser fetch must not cancel the shared request for other cards.
func (p *Live) snapshot(ctx context.Context, a *domain.Auth, cameraID string) ([]byte, string, error) {
	key := a.Subject + "\x00" + cameraID
	p.snapshotMu.Lock()
	if p.snapshots == nil {
		p.snapshots = map[string]*snapshotEntry{}
	}
	now := time.Now()
	for k, e := range p.snapshots {
		if !e.expires.IsZero() && !e.expires.After(now) {
			delete(p.snapshots, k)
		}
	}
	entry := p.snapshots[key]
	if entry == nil {
		if len(p.snapshots) >= 256 {
			p.snapshotMu.Unlock()
			return nil, "", &domain.RateLimitError{Until: now.Add(10 * time.Second)}
		}
		entry = &snapshotEntry{done: make(chan struct{})}
		p.snapshots[key] = entry
		accessToken := a.AccessToken
		go func() {
			requestCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			b, mime, _, err := p.raw(requestCtx, "POST", p.gateway+"/vdome-smart-facade/devices/api/v1/cameras/"+url.PathEscape(cameraID)+"/snapshots", accessToken, nil)
			p.snapshotMu.Lock()
			entry.data, entry.mime, entry.err = b, mime, err
			entry.expires = time.Now().Add(10 * time.Second)
			close(entry.done)
			p.snapshotMu.Unlock()
		}()
	}
	p.snapshotMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-entry.done:
		return entry.data, entry.mime, entry.err
	}
}
