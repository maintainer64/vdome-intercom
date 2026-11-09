package domain

import (
	"context"
	"errors"
	"time"
)

var ErrUnconfigured = errors.New("протокол VDome ещё не подтверждён: требуется IPA и запись запросов своего аккаунта")
var ErrUnauthorized = errors.New("сессия истекла: войдите снова")

type RateLimitError struct{ Until time.Time }

func (*RateLimitError) Error() string {
	return "VDome ограничил частоту запросов; ожидаем перед повтором"
}

type Credentials struct {
	Phone       string `json:"phone"`
	Code        string `json:"code,omitempty"`
	Challenge   string `json:"challenge,omitempty"`
	SSOResponse string `json:"ssoResponse,omitempty"`
}

// Secrets never cross the browser API. Only the encrypted vault stores them.
type Auth struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Subject      string    `json:"subject"`
}

type Login struct {
	Auth      *Auth
	Challenge string
}

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	APIStatus string `json:"apiStatus"`
	SIPStatus string `json:"sipStatus"`
	DoorState string `json:"doorState"`
	CanOpen   bool   `json:"canOpen"`
	HasCamera bool   `json:"hasCamera"`
	HasStream bool   `json:"hasStream"`
}

type Call struct {
	ID       string    `json:"id"`
	DeviceID string    `json:"deviceId"`
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Ended    bool      `json:"ended,omitempty"`
}

// CallStatus contains only operational metadata; never SIP identities or frames.
type CallStatus struct {
	State           string    `json:"state"`
	Reason          string    `json:"reason,omitempty"`
	HTTPStatus      int       `json:"httpStatus,omitempty"`
	SIPStatus       int       `json:"sipStatus,omitempty"`
	WSCloseCode     int       `json:"wsCloseCode,omitempty"`
	RefreshSeconds  int       `json:"refreshSeconds,omitempty"`
	Pongs           uint64    `json:"pongs"`
	SIPPongs        uint64    `json:"sipPongs"`
	RegisteredAt    time.Time `json:"registeredAt"`
	LastInviteAt    time.Time `json:"lastInviteAt"`
	ReceivedInvites uint64    `json:"receivedInvites"`
	AcceptedInvites uint64    `json:"acceptedInvites"`
	RejectedInvites uint64    `json:"rejectedInvites"`
	UnmappedCalls   int       `json:"unmappedCalls"`
}

type IncomingCall struct {
	DeviceID string    `json:"deviceId"`
	At       time.Time `json:"at"`
}

type Mode string

const (
	Off    Mode = "off"
	Next   Mode = "next"
	Always Mode = "always"
)

type Rule struct {
	Mode        Mode      `json:"mode"`
	ArmedAt     time.Time `json:"armedAt"`
	LastAttempt time.Time `json:"lastAttempt,omitempty"`
}

type Audit struct {
	At       time.Time `json:"at"`
	DeviceID string    `json:"deviceId"`
	CallID   string    `json:"callId,omitempty"`
	Action   string    `json:"action"`
	Result   string    `json:"result"`
}

// A Provider must scope EVERY resource to Auth.Subject. ListCalls returns
// recent incoming call events, not call history entries or open-door events.
// Open must NOT retry ambiguous transport failures and must validate the ack.
// A live implementation must refresh tokens before expiry and replace *Auth.
// Implementations must never log credentials, tokens or camera URLs.
type Provider interface {
	Name() string
	Login(context.Context, Credentials) (Login, error)
	Devices(context.Context, *Auth) ([]Device, error)
	Calls(context.Context, *Auth) ([]Call, error)
	Open(context.Context, *Auth, string, string) error
	Snapshot(context.Context, *Auth, string) ([]byte, string, error)
}
