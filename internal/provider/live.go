package provider

// Wire names and routes below were recovered from the official VDome 3.0.4r
// APK. See docs/protocol.md for evidence and the remaining runtime limitations.
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"vdome/internal/domain"
	"vdome/internal/sip"
)

const gatewayURL = "https://gateway.vdome.mts.ru"
const residentURL = "https://resident-rest.vdome.mts.ru"
const callbackURL = gatewayURL + "/user-service/api/auth/callback"

type Live struct {
	rateUntil             time.Time
	snapshotMu            sync.Mutex
	snapshots             map[string]*snapshotEntry
	http                  *http.Client
	gateway, resident, ws string
	registrar             string
	deviceID              string
	mu                    sync.Mutex
	accounts              map[string]*liveAccount
	challenges            map[string]loginChallenge
}
type loginChallenge struct {
	phone   string
	expires time.Time
}
type liveAccount struct {
	credential    sip.Credential
	listener      *sip.Listener
	devices       map[string]liveDevice
	streams       map[string]string
	unmapped      map[string]time.Time
	unmappedCalls int
}
type liveDevice struct{ intercomID, cameraID, phone, lock string }
type tokens struct {
	Access  string `json:"accessToken"`
	Refresh string `json:"refreshToken"`
}
type user struct {
	ID   string `json:"id"`
	Auth []struct {
		Credential sip.Credential `json:"credential"`
	} `json:"auth"`
}

func NewLive() *Live {
	return &Live{http: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, gateway: gatewayURL, resident: residentURL, ws: "wss://webrtc.domofon.mts.ru/ws", deviceID: requestUUID(), accounts: map[string]*liveAccount{}, challenges: map[string]loginChallenge{}}
}
func (*Live) DeviceRefreshInterval() time.Duration { return time.Minute }
func (*Live) Name() string                         { return "live" }
func (p *Live) LoginURL() string                   { return ssoURL(p.deviceID) }

func (p *Live) ConfigureSIP(endpoint, registrar string) error {
	if endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("VDOME_SIP_WS must be a wss URL without credentials")
		}
		p.ws = endpoint
	}
	if strings.ContainsAny(registrar, "\r\n\"\\ <>;") {
		return errors.New("invalid VDOME_SIP_REGISTRAR")
	}
	p.registrar = registrar
	return nil
}

// External MTS login stays on MTS. The registered callback cannot be silently
// changed to localhost. The UI imports the callback JSON explicitly instead.
func SSOURL() string {
	return ssoURL(requestUUID())
}
func ssoURL(deviceID string) string {
	// SDK 6.2.0: k2 -> g2/d2 -> i4 encodes the complete DeviceId object
	// using Android Base64.NO_WRAP (standard alphabet, with padding).
	device, _ := json.Marshal(map[string]string{
		"uuid": deviceID, "platform": "Android", "vendor": "local", "model": "VDome Go",
		"name": "VDome local panel", "ip": "", "timezone": "UTC", "locale": "ru",
		"useragent": "vdome-go/0.1", "version": "0.1", "screenSize": "", "systemVersion": runtime.GOOS,
	})
	u, _ := url.Parse("https://login.mts.ru/amserver/oauth2/authorize")
	q := url.Values{"client_id": {"VDome"}, "redirect_uri": {callbackURL}, "response_type": {"code"}, "scope": {"account address sso profile ssom openid phone"}, "device_id": {base64.StdEncoding.EncodeToString(device)}, "state": {deviceID}}
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String()
}

func requestUUID() string {
	id := randomID()
	return id[:8] + "-" + id[8:12] + "-4" + id[13:16] + "-a" + id[17:20] + "-" + id[20:32]
}

func (p *Live) raw(ctx context.Context, method, endpoint, token string, body any) ([]byte, string, int, error) {
	p.mu.Lock()
	until := p.rateUntil
	p.mu.Unlock()
	if time.Now().Before(until) {
		return nil, "", 429, &domain.RateLimitError{Until: until}
	}

	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, "", 0, err
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, input)
	if err != nil {
		return nil, "", 0, errors.New("invalid provider request")
	}
	req.Header.Set("Accept", "application/json")
	if strings.HasSuffix(req.URL.Path, "/snapshots") {
		req.Header.Set("Accept", "image/webp")
		// CoilModule and CameraSnapshotSaver use POST with an empty body.
		if input == nil {
			req.Body = http.NoBody
			req.ContentLength = 0
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Device-Type", "ANDROID")
	req.Header.Set("Request-Id", requestUUID())
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, "", 0, errors.New("VDome: connection failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(b) > 8<<20 {
		return nil, "", resp.StatusCode, errors.New("VDome: invalid response size")
	}
	if resp.StatusCode == 429 {
		until := time.Now().Add(time.Minute)
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			until = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil && date.After(time.Now()) {
			until = date
		}
		p.mu.Lock()
		if until.After(p.rateUntil) {
			p.rateUntil = until
		}
		until = p.rateUntil
		p.mu.Unlock()
		return nil, "", 429, &domain.RateLimitError{Until: until}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, "", resp.StatusCode, domain.ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", resp.StatusCode, fmt.Errorf("VDome: HTTP %d", resp.StatusCode)
	}
	return b, strings.Split(resp.Header.Get("Content-Type"), ";")[0], resp.StatusCode, nil
}
func (p *Live) refresh(ctx context.Context, a *domain.Auth) error {
	if a.RefreshToken == "" {
		return domain.ErrUnauthorized
	}
	b, _, status, err := p.raw(ctx, "PUT", p.gateway+"/user-service/api/v2/auth/refresh", "", map[string]string{"refreshToken": a.RefreshToken})
	if err != nil {
		// HTTP 400 also means the refresh request was rejected; require a new login.
		// Keep response bodies out of both logs and browser diagnostics.
		if status == 400 || status == 401 || status == 403 {
			return domain.ErrUnauthorized
		}
		return err
	}
	var pair tokens
	if err := json.Unmarshal(b, &pair); err != nil || pair.Access == "" || pair.Refresh == "" {
		return errors.New("VDome: invalid refresh response")
	}
	a.AccessToken = pair.Access
	a.RefreshToken = pair.Refresh
	a.ExpiresAt = expiry(pair.Access)
	return nil
}
func expiry(token string) time.Time {
	// JWT payload is a scheduling hint ONLY. Identity is confirmed via /self.
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		b, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if json.Unmarshal(b, &claims) == nil && claims.Exp > 0 {
			return time.Unix(claims.Exp, 0)
		}
	}
	return time.Now().Add(5 * time.Minute)
}
func (p *Live) request(ctx context.Context, a *domain.Auth, method, path string, body any) ([]byte, string, error) {
	if !a.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
		if err := p.refresh(ctx, a); err != nil {
			return nil, "", err
		}
	}
	b, mime, status, err := p.raw(ctx, method, p.gateway+path, a.AccessToken, body)
	// GET can be retried after a definite 401; unlock POST is NEVER replayed.
	if status == 401 && method == "GET" {
		if err := p.refresh(ctx, a); err != nil {
			return nil, "", err
		}
		b, mime, _, err = p.raw(ctx, method, p.gateway+path, a.AccessToken, body)
	}
	return b, mime, err
}
func unwrap(b []byte, target any) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return errors.New("VDome: invalid JSON")
	}
	if len(envelope.Errors) > 0 && string(envelope.Errors) != "null" && string(envelope.Errors) != "{}" {
		return errors.New("VDome rejected authentication")
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return errors.New("VDome: missing result")
	}
	return json.Unmarshal(envelope.Result, target)
}

func (p *Live) Login(ctx context.Context, c domain.Credentials) (domain.Login, error) {
	var pair tokens
	if c.SSOResponse != "" {
		var response struct {
			Pair tokens `json:"keycloakTokenPair"`
		}
		if err := json.Unmarshal([]byte(c.SSOResponse), &response); err != nil {
			return domain.Login{}, errors.New("invalid SSO callback JSON")
		}
		pair = response.Pair
	} else if c.Challenge == "" {
		if !phonePattern.MatchString(c.Phone) {
			return domain.Login{}, errors.New("invalid phone")
		}
		// Legacy SMS route remains in AuthApi; current UI uses SSO. Its live
		// availability is explicitly labelled as unverified in our UI.
		b, _, _, err := p.raw(ctx, "POST", p.resident+"/user-service/api/v2/auth/init", "", map[string]string{"phone": c.Phone})
		if err != nil {
			return domain.Login{}, err
		}
		var init struct {
			Status string `json:"status"`
		}
		if err := unwrap(b, &init); err != nil {
			return domain.Login{}, err
		}
		id := randomID()
		p.mu.Lock()
		defer p.mu.Unlock()
		for k, v := range p.challenges {
			if time.Now().After(v.expires) {
				delete(p.challenges, k)
			}
		}
		if len(p.challenges) >= 100 {
			return domain.Login{}, errors.New("too many login challenges")
		}
		p.challenges[id] = loginChallenge{c.Phone, time.Now().Add(5 * time.Minute)}
		return domain.Login{Challenge: id}, nil
	} else {
		p.mu.Lock()
		ch, ok := p.challenges[c.Challenge]
		p.mu.Unlock()
		if !ok || time.Now().After(ch.expires) || ch.phone != c.Phone || len(c.Code) < 4 || len(c.Code) > 8 {
			return domain.Login{}, errors.New("invalid SMS challenge")
		}
		b, _, _, err := p.raw(ctx, "POST", p.resident+"/user-service/api/v2/auth/login", "", map[string]string{"phone": ch.phone, "code": c.Code})
		if err != nil {
			return domain.Login{}, err
		}
		if err := unwrap(b, &pair); err != nil {
			return domain.Login{}, err
		}
		p.mu.Lock()
		delete(p.challenges, c.Challenge)
		p.mu.Unlock()
	}
	if pair.Access == "" || pair.Refresh == "" {
		return domain.Login{}, errors.New("missing VDome tokens")
	}
	a := &domain.Auth{AccessToken: pair.Access, RefreshToken: pair.Refresh, ExpiresAt: expiry(pair.Access)}
	u, err := p.self(ctx, a)
	if err != nil {
		return domain.Login{}, err
	}
	a.Subject = u.ID
	return domain.Login{Auth: a}, nil
}
func (p *Live) self(ctx context.Context, a *domain.Auth) (user, error) {
	b, _, err := p.request(ctx, a, "GET", "/domofon/api/security/v1/users/self", nil)
	if err != nil {
		return user{}, err
	}
	var response struct {
		List  []user          `json:"list"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &response) != nil || len(response.List) != 1 || response.List[0].ID == "" || hasError(response.Error) {
		return user{}, errors.New("VDome: invalid self response")
	}
	u := response.List[0]
	if a.Subject != "" && u.ID != a.Subject {
		return user{}, errors.New("VDome: account changed")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.accounts[u.ID] == nil {
		p.accounts[u.ID] = &liveAccount{devices: map[string]liveDevice{}}
	}
	if len(u.Auth) > 0 {
		p.accounts[u.ID].credential = u.Auth[0].Credential
		p.accounts[u.ID].credential.Registrar = p.registrar
	}
	return u, nil
}
func hasError(b json.RawMessage) bool { return len(b) > 0 && string(b) != "null" && string(b) != "{}" }
func status(b *bool) string {
	if b == nil {
		return "unknown"
	}
	if *b {
		return "online"
	}
	return "offline"
}

type intercom struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	CanUnlock *bool  `json:"can_unlock"`
	Online    *bool  `json:"is_online"`
	Camera    struct {
		ID string `json:"id"`
	} `json:"camera"`
	Device struct {
		Building struct {
			Address struct {
				Text string `json:"text"`
			} `json:"address"`
		} `json:"building"`
	} `json:"device"`
	Monitoring struct {
		API *bool `json:"api"`
		SIP *bool `json:"sip"`
	} `json:"monitoring"`
	Locks struct {
		Items []struct {
			Number    int    `json:"number"`
			Name      string `json:"name"`
			Active    *bool  `json:"is_active"`
			Permanent *bool  `json:"is_permanently_open"`
		} `json:"items"`
	} `json:"locks"`
}

func (p *Live) Devices(ctx context.Context, a *domain.Auth) ([]domain.Device, error) {
	p.mu.Lock()
	account := p.accounts[a.Subject]
	p.mu.Unlock()
	if account == nil {
		if _, err := p.self(ctx, a); err != nil {
			return nil, err
		}
	}
	var all []intercom
	for _, category := range []string{"intercom", "barrier"} {
		b, _, err := p.request(ctx, a, "GET", "/domofon/api/intercom/v2/intercoms?limit=1000&category="+category, nil)
		if err != nil {
			return nil, err
		}
		var response struct {
			List  *[]intercom     `json:"list"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(b, &response) != nil || response.List == nil || hasError(response.Error) {
			return nil, errors.New("VDome: invalid device list")
		}
		all = append(all, (*response.List)...)
	}
	devices := []domain.Device{}
	mapping := map[string]liveDevice{}
	for _, d := range all {
		if d.ID == "" {
			continue
		}
		api := d.Monitoring.API
		if api == nil {
			api = d.Online
		}
		base := domain.Device{ID: d.ID, Name: d.Name, Address: d.Device.Building.Address.Text, APIStatus: status(api), SIPStatus: status(d.Monitoring.SIP), DoorState: "unknown", CanOpen: d.CanUnlock != nil && *d.CanUnlock, HasCamera: d.Camera.ID != ""}
		if base.Name == "" {
			base.Name = "Домофон"
		}
		if len(d.Locks.Items) > 1 {
			for _, lock := range d.Locks.Items {
				item := base
				item.ID = d.ID + "/" + strconv.Itoa(lock.Number)
				item.Name += " · " + lock.Name
				item.CanOpen = item.CanOpen && lock.Active != nil && *lock.Active
				// Configuration is not a physical door-contact reading.
				devices = append(devices, item)
				mapping[item.ID] = liveDevice{d.ID, d.Camera.ID, d.Phone, strconv.Itoa(lock.Number)}
			}
		} else {
			devices = append(devices, base)
			mapping[d.ID] = liveDevice{intercomID: d.ID, cameraID: d.Camera.ID, phone: d.Phone}
		}
	}
	// Camera credentials/RTSP URLs stay server-side; expose only opaque IDs.
	b, _, err := p.request(ctx, a, "GET", "/domofon/api/intercom/v1/cameras?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	var cameras struct {
		List *[]struct {
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			RTSP    string          `json:"rtsp"`
			Streams json.RawMessage `json:"streams"`
			ToWatch json.RawMessage `json:"streamsToWatch"`
		} `json:"list"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &cameras) != nil || cameras.List == nil || hasError(cameras.Error) {
		return nil, errors.New("VDome: invalid camera list")
	}
	streamURLs := map[string]string{}
	for _, c := range *cameras.List {
		if c.ID != "" {
			id := "camera/" + c.ID
			devices = append(devices, domain.Device{ID: id, Name: c.Name, APIStatus: "unknown", SIPStatus: "unknown", DoorState: "unknown", HasCamera: true})
			mapping[id] = liveDevice{cameraID: c.ID}
			streamURLs[c.ID] = chooseStream(streamMap(c.ToWatch), streamMap(c.Streams), c.RTSP)
		}
	}
	p.mu.Lock()
	p.accounts[a.Subject].devices = mapping
	p.accounts[a.Subject].streams = streamURLs
	p.mu.Unlock()
	for i := range devices {
		devices[i].HasStream = streamURLs[mapping[devices[i].ID].cameraID] != ""
	}
	return devices, nil
}

func streamMap(raw json.RawMessage) map[string]string {
	var result map[string]string
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return result
}
func chooseStream(watch, streams map[string]string, rtsp string) string {
	for _, candidates := range []map[string]string{watch, streams} {
		for _, key := range []string{"rtsp", "hls", "https", "http"} {
			if value := candidates[key]; value != "" {
				return value
			}
		}
	}
	return rtsp
}

// StreamSource is only consumed by the server-side relay, never JSON-encoded.
func (p *Live) StreamSource(_ context.Context, a *domain.Auth, id string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	account := p.accounts[a.Subject]
	if account == nil {
		return "", domain.ErrUnauthorized
	}
	device, ok := account.devices[id]
	if !ok || device.cameraID == "" {
		return "", errors.New("camera not in this account")
	}
	source := account.streams[device.cameraID]
	if source == "" {
		return "", errors.New("camera stream unavailable")
	}
	return source, nil
}
func (p *Live) Calls(_ context.Context, a *domain.Auth) ([]domain.Call, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	account := p.accounts[a.Subject]
	if account == nil {
		return nil, errors.New("missing SIP account")
	}
	if account.listener == nil {
		account.listener = sip.New(account.credential, p.ws)
	}
	events, ready := account.listener.Events()
	if !ready {
		return nil, errors.New("SIP connection not registered")
	}
	calls := []domain.Call{}
	for id, at := range account.unmapped {
		if time.Since(at) > 30*time.Second {
			delete(account.unmapped, id)
		}
	}
	for _, event := range events {
		// SIP caller is mapped only to this account's returned devices. Every
		// explicit lock can have its own rule; unmapped callers never open.
		matched := false
		for id, d := range account.devices {
			if d.intercomID != "" && sip.Identity(d.phone) == event.From && event.From != "" {
				matched = true
				calls = append(calls, domain.Call{ID: event.ID, DeviceID: id, Kind: "incoming", At: event.At, Ended: event.Ended})
			}
		}
		if !matched {
			if account.unmapped == nil {
				account.unmapped = map[string]time.Time{}
			}
			if _, seen := account.unmapped[event.ID]; !seen {
				account.unmapped[event.ID] = event.At
				account.unmappedCalls++
			}
		}
	}
	return calls, nil
}
func (p *Live) CallStatus(subject string) domain.CallStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[subject]
	if a == nil || a.listener == nil {
		return domain.CallStatus{State: "waiting"}
	}
	status := a.listener.Status()
	status.UnmappedCalls = a.unmappedCalls
	return status
}
func (p *Live) Open(ctx context.Context, a *domain.Auth, id, requestID string) error {
	p.mu.Lock()
	account := p.accounts[a.Subject]
	var d liveDevice
	if account != nil {
		d = account.devices[id]
	}
	p.mu.Unlock()
	if d.intercomID == "" {
		return errors.New("door not in this account")
	}
	path := "/vdome-smart-facade/devices/api/v1/intercoms/" + url.PathEscape(d.intercomID) + "/lock"
	if d.lock != "" {
		path += "/" + url.PathEscape(d.lock)
	}
	b, _, err := p.request(ctx, a, "POST", path, map[string]string{"action": "open"})
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) > 0 {
		var reply struct {
			Error   json.RawMessage `json:"error"`
			Errors  json.RawMessage `json:"errors"`
			Success *bool           `json:"success"`
		}
		if json.Unmarshal(b, &reply) != nil || hasError(reply.Error) || hasError(reply.Errors) || (reply.Success != nil && !*reply.Success) {
			return errors.New("VDome rejected unlock")
		}
	}
	return nil
}
func (p *Live) Snapshot(ctx context.Context, a *domain.Auth, id string) ([]byte, string, error) {
	p.mu.Lock()
	account := p.accounts[a.Subject]
	var d liveDevice
	if account != nil {
		d = account.devices[id]
	}
	p.mu.Unlock()
	if d.cameraID == "" {
		return nil, "", errors.New("camera not in this account")
	}
	return p.snapshot(ctx, a, d.cameraID)
}
func (p *Live) Disconnect(subject string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if account := p.accounts[subject]; account != nil && account.listener != nil {
		account.listener.Close()
	}
	delete(p.accounts, subject)
}
