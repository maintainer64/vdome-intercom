package app

import (
	"context"
	"errors"
	"time"
	"vdome/internal/domain"
)

func (m *Manager) StartStream(ctx context.Context, token, id string) (string, error) {
	m.mu.Lock()
	s, err := m.session(token)
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	d, err := device(s, id)
	if err != nil || !d.HasStream {
		m.mu.Unlock()
		return "", errors.New("stream unavailable")
	}
	auth := s.Auth
	m.mu.Unlock()
	p, ok := m.p.(interface {
		StreamSource(context.Context, *domain.Auth, string) (string, error)
	})
	if !ok {
		return "", errors.New("stream unavailable")
	}
	source, err := p.StreamSource(ctx, &auth, id)
	if err != nil {
		return "", err
	}
	streamID, err := m.media.Start(ctx, sessionKey(token), id, source)
	if err != nil {
		return "", err
	}
	// Logout may have happened while the camera was connecting.
	err = m.mediaAuthorized(token)
	if err != nil {
		m.media.Stop(sessionKey(token), streamID)
		return "", err
	}
	return streamID, nil
}

// Media authorization uses immutable expiries, independently of REST polling.
func (m *Manager) mediaAuthorized(token string) error {
	expiry, ok := m.authIndex.Load(sessionKey(token))
	if !ok || !expiry.(time.Time).After(m.now()) {
		return domain.ErrUnauthorized
	}
	return nil
}
func (m *Manager) StreamFile(token, id, file string) ([]byte, string, error) {
	if err := m.mediaAuthorized(token); err != nil {
		return nil, "", err
	}
	return m.media.Read(sessionKey(token), id, file)
}
func (m *Manager) StopStream(token, id string) error {
	if err := m.mediaAuthorized(token); err != nil {
		return err
	}
	m.media.Stop(sessionKey(token), id)
	return nil
}
