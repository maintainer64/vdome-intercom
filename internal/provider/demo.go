package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
	"vdome/internal/domain"
)

// Demo is isolated from all external networks. The code is deliberately public.
type Demo struct {
	mu         sync.Mutex
	challenges map[string]string
	calls      map[string][]domain.Call
	opens      map[string]int
}

func NewDemo() *Demo {
	return &Demo{challenges: map[string]string{}, calls: map[string][]domain.Call{}, opens: map[string]int{}}
}
func (*Demo) Name() string { return "demo" }
func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

var phonePattern = regexp.MustCompile(`^\+?[0-9]{10,15}$`)

func (d *Demo) Login(_ context.Context, c domain.Credentials) (domain.Login, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.Challenge == "" {
		if !phonePattern.MatchString(c.Phone) {
			return domain.Login{}, errors.New("введите номер телефона: от 10 до 15 цифр")
		}
		if len(d.challenges) >= 200 {
			d.challenges = map[string]string{}
		}
		id := randomID()
		d.challenges[id] = c.Phone
		return domain.Login{Challenge: id}, nil
	}
	phone, ok := d.challenges[c.Challenge]
	if !ok || c.Code != "000000" {
		return domain.Login{}, errors.New("демо-код: 000000; при необходимости запросите новый код")
	}
	delete(d.challenges, c.Challenge)
	return domain.Login{Auth: &domain.Auth{Subject: phone, AccessToken: randomID(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}}, nil
}

func (d *Demo) Devices(_ context.Context, a *domain.Auth) ([]domain.Device, error) {
	return []domain.Device{
		{ID: "entrance", Name: "Подъезд", Address: "Демонстрационный дом · подъезд 1", APIStatus: "online", SIPStatus: "online", DoorState: "unknown", CanOpen: true, HasCamera: true},
		{ID: "gate", Name: "Калитка", Address: "Демонстрационный дом · двор", APIStatus: "offline", SIPStatus: "offline", DoorState: "unknown", CanOpen: false, HasCamera: false},
	}, nil
}
func (d *Demo) Calls(_ context.Context, a *domain.Auth) ([]domain.Call, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]domain.Call{}, d.calls[a.Subject]...), nil
}
func (d *Demo) Simulate(a *domain.Auth, device string) error {
	if device != "entrance" {
		return errors.New("домофон недоступен")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	calls := append(d.calls[a.Subject], domain.Call{ID: randomID(), DeviceID: device, Kind: "incoming", At: time.Now().UTC()})
	if len(calls) > 100 {
		calls = calls[len(calls)-100:]
	}
	d.calls[a.Subject] = calls
	return nil
}
func (d *Demo) Open(_ context.Context, a *domain.Auth, device, requestID string) error {
	if device != "entrance" {
		return errors.New("домофон недоступен")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opens[a.Subject]++
	return nil
}
func (d *Demo) Snapshot(_ context.Context, a *domain.Auth, device string) ([]byte, string, error) {
	if device != "entrance" {
		return nil, "", errors.New("камера недоступна")
	}
	// Generated fixture, never an actual camera stream.
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="960" height="540" viewBox="0 0 960 540"><rect width="960" height="540" fill="#e5e7eb"/><path d="M0 420L250 230 400 360 650 170 960 420" fill="#bac3cf"/><rect x="350" y="130" width="260" height="410" fill="#8995a5"/><rect x="395" y="190" width="170" height="350" fill="#344054"/><circle cx="535" cy="365" r="8" fill="#f9fafb"/><text x="24" y="40" font-family="sans-serif" font-size="24">ДЕМО · Синтетическое изображение</text><text x="24" y="510" font-family="monospace" font-size="20">%s UTC</text></svg>`, time.Now().UTC().Format("2006-01-02 15:04:05"))
	return []byte(svg), "image/svg+xml", nil
}
