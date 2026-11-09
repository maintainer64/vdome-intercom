package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"vdome/internal/domain"
)

type roundTrip func(*http.Request) (*http.Response, error)

func TestSSOLinkUsesSDKDeviceDescriptor(t *testing.T) {
	p := NewLive()
	u, err := url.Parse(p.LoginURL())
	if err != nil {
		t.Fatal(err)
	}
	b, err := base64.StdEncoding.DecodeString(u.Query().Get("device_id"))
	if err != nil {
		t.Fatal("device_id is not SDK base64 JSON")
	}
	var descriptor map[string]string
	if json.Unmarshal(b, &descriptor) != nil || descriptor["uuid"] != p.deviceID || descriptor["platform"] != "Android" || len(descriptor) != 12 {
		t.Fatalf("invalid SDK device descriptor: %s", b)
	}
	if u.Query().Get("redirect_uri") != callbackURL || u.Query().Get("state") == "" || strings.Contains(u.RawQuery, "+") {
		t.Fatal("invalid OAuth parameters")
	}
}

func (fn roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func reply(status int, body, mime string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {mime}}, Body: io.NopCloser(strings.NewReader(body))}
}
func newTestLive(fn roundTrip) *Live { p := NewLive(); p.http.Transport = fn; return p }

func TestLiveSSORequiresServerConfirmedIdentity(t *testing.T) {
	p := newTestLive(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "gateway.vdome.mts.ru" || r.URL.Path != "/domofon/api/security/v1/users/self" || r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("X-Device-Type") != "ANDROID" {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		return reply(200, `{"list":[{"id":"confirmed-account","auth":[]}]}`, "application/json"), nil
	})
	l, err := p.Login(context.Background(), domain.Credentials{Phone: "untrusted-subject", SSOResponse: `{"keycloakTokenPair":{"accessToken":"fixture-access","refreshToken":"fixture-refresh"}}`})
	if err != nil || l.Auth == nil || l.Auth.Subject != "confirmed-account" {
		t.Fatalf("login=%+v err=%v", l, err)
	}
	if _, err := p.Login(context.Background(), domain.Credentials{SSOResponse: `{"keycloakTokenPair":{"accessToken":"a"}}`}); err == nil {
		t.Fatal("partial tokens accepted")
	}
}
func TestLiveRefreshRotatesTokensAndNeverRetriesUnlock(t *testing.T) {
	opens, refreshes := 0, 0
	p := newTestLive(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/refresh") {
			refreshes++
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			if r.Method != "PUT" || input["refreshToken"] != "old-refresh" {
				t.Fatal("invalid refresh request")
			}
			return reply(200, `{"accessToken":"rotated-access","refreshToken":"rotated-refresh"}`, "application/json"), nil
		}
		opens++
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/door/lock/2") || r.Header.Get("Authorization") != "Bearer rotated-access" {
			t.Fatal("invalid unlock request")
		}
		return reply(401, `{}`, "application/json"), nil
	})
	p.accounts["owner"] = &liveAccount{devices: map[string]liveDevice{"door/2": {intercomID: "door", lock: "2"}}}
	a := &domain.Auth{Subject: "owner", AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Second)}
	if p.Open(context.Background(), a, "door/2", "request") == nil {
		t.Fatal("unconfirmed unlock accepted")
	}
	if opens != 1 || refreshes != 1 || a.RefreshToken != "rotated-refresh" {
		t.Fatalf("opens=%d refreshes=%d", opens, refreshes)
	}
}
func TestLiveDevicePermissionsMultipleLocksAndNoCameraSecrets(t *testing.T) {
	p := newTestLive(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/cameras") {
			return reply(200, `{"list":[{"id":"camera","name":"Yard","rtsp":"rtsp://private:secret@private.example/stream"}]}`, "application/json"), nil
		}
		if r.URL.Query().Get("category") == "barrier" {
			return reply(200, `{"list":[]}`, "application/json"), nil
		}
		return reply(200, `{"list":[{"id":"door","name":"Entrance","phone":"900","can_unlock":true,"monitoring":{"api":true,"sip":false},"camera":{"id":"camera"},"locks":{"items":[{"number":1,"name":"Main","is_active":true},{"number":2,"name":"Service","is_active":false}]}}]}`, "application/json"), nil
	})
	p.accounts["owner"] = &liveAccount{devices: map[string]liveDevice{}}
	a := &domain.Auth{Subject: "owner", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour)}
	d, err := p.Devices(context.Background(), a)
	if err != nil || len(d) != 3 {
		t.Fatalf("devices=%+v err=%v", d, err)
	}
	if !d[0].CanOpen || d[1].CanOpen || d[2].CanOpen || d[0].SIPStatus != "offline" {
		t.Fatalf("incorrect permissions: %+v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "rtsp") {
		t.Fatal("camera credentials exposed")
	}
	if p.Open(context.Background(), a, "foreign", "x") == nil {
		t.Fatal("foreign unlock allowed")
	}
}
func TestLiveRejectsErrorEnvelopesAndRedirects(t *testing.T) {
	for _, body := range []string{`{}`, `{"list":null}`, `{"list":[],"error":{"message":"denied"}}`} {
		p := newTestLive(func(*http.Request) (*http.Response, error) { return reply(200, body, "application/json"), nil })
		p.accounts["owner"] = &liveAccount{}
		if _, err := p.Devices(context.Background(), &domain.Auth{Subject: "owner", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
			t.Fatalf("accepted invalid body %s", body)
		}
	}
	p := newTestLive(func(*http.Request) (*http.Response, error) { return reply(302, "", "text/html"), nil })
	if _, _, _, err := p.raw(context.Background(), "GET", gatewayURL+"/test", "private-token", nil); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestCameraUsesNativePostWebPContract(t *testing.T) {
	p := newTestLive(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Path != "/vdome-smart-facade/devices/api/v1/cameras/camera-1/snapshots" || r.Header.Get("Accept") != "image/webp" || r.ContentLength != 0 || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Fatalf("invalid snapshot contract: method=%s path=%s accept=%s length=%d", r.Method, r.URL.Path, r.Header.Get("Accept"), r.ContentLength)
		}
		return reply(200, "RIFF-test-WEBP", "image/webp"), nil
	})
	p.accounts["owner"] = &liveAccount{devices: map[string]liveDevice{"door": {cameraID: "camera-1"}}}
	b, mime, err := p.Snapshot(context.Background(), &domain.Auth{Subject: "owner", AccessToken: "fixture"}, "door")
	if err != nil || mime != "image/webp" || string(b) != "RIFF-test-WEBP" {
		t.Fatalf("snapshot mime=%s err=%v", mime, err)
	}
}

func TestRateLimitHonorsRetryAfterWithoutMoreNetworkRequests(t *testing.T) {
	calls := 0
	p := newTestLive(func(*http.Request) (*http.Response, error) {
		calls++
		r := reply(429, "private response body", "text/plain")
		r.Header.Set("Retry-After", "120")
		return r, nil
	})
	for range 3 {
		_, _, status, err := p.raw(context.Background(), "GET", gatewayURL+"/test", "private-token", nil)
		var limited *domain.RateLimitError
		if status != 429 || !errors.As(err, &limited) || time.Until(limited.Until) < 119*time.Second {
			t.Fatal("missing provider cooldown")
		}
	}
	if calls != 1 {
		t.Fatalf("rate-limited requests hit network %d times", calls)
	}
}
func TestDuplicateCameraCardsShareOneSnapshotRequest(t *testing.T) {
	var calls atomic.Int32
	p := newTestLive(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return reply(200, "frame", "image/webp"), nil
	})
	p.accounts["owner"] = &liveAccount{devices: map[string]liveDevice{"door": {cameraID: "same-camera"}, "camera/card": {cameraID: "same-camera"}}}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "door"
			if i%2 == 0 {
				id = "camera/card"
			}
			b, _, err := p.Snapshot(context.Background(), &domain.Auth{Subject: "owner", AccessToken: "fixture"}, id)
			if err != nil || string(b) != "frame" {
				t.Error("snapshot failed")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate snapshots: %d", calls.Load())
	}
}

func TestRejectedRefreshRequiresLoginWithoutExposingResponse(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		p := newTestLive(func(*http.Request) (*http.Response, error) {
			return reply(status, `{"error":"private-account-token"}`, "application/json"), nil
		})
		a := &domain.Auth{AccessToken: "old", RefreshToken: "rejected"}
		if err := p.refresh(context.Background(), a); !errors.Is(err, domain.ErrUnauthorized) || strings.Contains(err.Error(), "private") {
			t.Fatalf("status=%d error=%v", status, err)
		}
	}
}
