// Package sip implements the registration and incoming-call subset of the
// Callibry SIP-over-WebSocket protocol recovered from VDome. It does not answer
// calls, negotiate media, or read historical calls as live unlock triggers.
package sip

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Credential struct {
	Registrar string `json:"-"`
	Domain    string `json:"domain"`
	Secret    string `json:"secret"`
	Username  string `json:"sip"`
}
type Event struct {
	ID, From string
	At       time.Time
}
type Listener struct {
	mu     sync.Mutex
	ready  bool
	events map[string]Event
	cancel context.CancelFunc
}

func New(credential Credential, endpoint string) *Listener {
	ctx, cancel := context.WithCancel(context.Background())
	l := &Listener{events: map[string]Event{}, cancel: cancel}
	go l.run(ctx, credential, endpoint)
	return l
}
func (l *Listener) Close() { l.cancel() }
func (l *Listener) Events() ([]Event, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := []Event{}
	for id, e := range l.events {
		if time.Since(e.At) > 30*time.Second {
			delete(l.events, id)
		} else {
			result = append(result, e)
		}
	}
	return result, l.ready
}
func (l *Listener) setReady(value bool) {
	l.mu.Lock()
	l.ready = value
	if !value {
		l.events = map[string]Event{}
	}
	l.mu.Unlock()
}
func safe(s string) bool { return s != "" && !strings.ContainsAny(s, "\r\n\x00\"\\ <>;") }
func (l *Listener) run(ctx context.Context, credential Credential, endpoint string) {
	if !safe(credential.Username) || !safe(credential.Domain) || credential.Secret == "" {
		return
	}
	delay := time.Second
	for ctx.Err() == nil {
		_ = l.connect(ctx, credential, endpoint)
		l.setReady(false)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

type readResult struct {
	data []byte
	err  error
}

func random() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (l *Listener) connect(ctx context.Context, credential Credential, endpoint string) error {
	d := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second, Subprotocols: []string{"sip"}}
	conn, response, err := d.DialContext(ctx, endpoint, http.Header{"Origin": {""}})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return errors.New("SIP websocket connection failed")
	}
	defer conn.Close()
	if conn.Subprotocol() != "sip" {
		return errors.New("unexpected websocket subprotocol")
	}
	conn.SetReadLimit(64 << 10)
	_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(70 * time.Second)) })
	readCtx, stop := context.WithCancel(ctx)
	defer stop()
	reads := make(chan readResult, 1)
	go func() {
		for {
			_, b, err := conn.ReadMessage()
			select {
			case reads <- readResult{b, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	reg := registration{credential: credential, callID: random(), tag: random()[:12], contact: random()[:12] + ".invalid"}
	write := func(message string) error {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteMessage(websocket.TextMessage, []byte(message))
	}
	if err := write(reg.request("")); err != nil {
		return err
	}
	renew := time.NewTimer(12 * time.Second)
	defer renew.Stop()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	registered := false
	challenges := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return err
			}
		case <-renew.C:
			if !registered {
				return errors.New("SIP registration timed out")
			}
			registered = false
			l.setReady(false)
			challenges = 0
			if err := write(reg.request("")); err != nil {
				return err
			}
			renew.Reset(12 * time.Second)
		case received := <-reads:
			if received.err != nil {
				return errors.New("SIP connection closed")
			}
			if strings.TrimSpace(string(received.data)) == "" {
				continue
			}
			message, err := parse(string(received.data))
			if err != nil {
				return err
			}
			_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
			if strings.HasPrefix(message.first, "SIP/2.0 ") {
				if message.one("call-id") != reg.callID || message.one("cseq") != fmt.Sprintf("%d REGISTER", reg.sequence) || !strings.Contains(message.one("via"), "branch="+reg.branch) {
					continue
				}
				if strings.HasPrefix(message.first, "SIP/2.0 401 ") {
					challenges++
					if challenges > 2 {
						return errors.New("SIP authentication failed")
					}
					auth, err := digest(credential, message.one("www-authenticate"))
					if err != nil {
						return err
					}
					if err := write(reg.request(auth)); err != nil {
						return err
					}
				} else if strings.HasPrefix(message.first, "SIP/2.0 200 ") {
					registered = true
					l.setReady(true)
					if !renew.Stop() {
						select {
						case <-renew.C:
						default:
						}
					}
					renew.Reset(5 * time.Minute)
				} else if !strings.HasPrefix(message.first, "SIP/2.0 1") {
					return errors.New("SIP registration rejected")
				}
				continue
			}
			method := strings.Fields(message.first)[0]
			id := message.one("call-id")
			switch method {
			case "OPTIONS":
				if err := write(message.response("405 Method Not Allowed")); err != nil {
					return err
				}
			case "INVITE":
				if !registered || id == "" || len(id) > 256 || Identity(message.one("to")) != credential.Username {
					continue
				}
				l.mu.Lock()
				if len(l.events) < 128 {
					if _, exists := l.events[id]; !exists {
						l.events[id] = Event{ID: id, From: Identity(message.one("from")), At: time.Now().UTC()}
					}
				}
				l.mu.Unlock()
				if err := write(message.response("100 Trying")); err != nil {
					return err
				}
			case "CANCEL", "BYE":
				l.mu.Lock()
				delete(l.events, id)
				l.mu.Unlock()
				if err := write(message.response("200 OK")); err != nil {
					return err
				}
			}
		}
	}
}

type registration struct {
	credential                   Credential
	callID, tag, contact, branch string
	sequence                     int
}

func (r *registration) request(auth string) string {
	r.sequence++
	r.branch = "z9hG4bK" + random()
	uri := "sip:" + r.credential.Username + "@" + r.credential.Domain
	lines := []string{"REGISTER " + uri + " SIP/2.0", "Via: SIP/2.0/TCP " + r.contact + ";branch=" + r.branch + ";rport", "Max-Forwards: 70", "From: <" + uri + ">;tag=" + r.tag, "To: <" + uri + ">", "Call-ID: " + r.callID, fmt.Sprintf("CSeq: %d REGISTER", r.sequence), "Contact: <sip:" + r.credential.Username + "@" + r.contact + ";transport=tcp>", "Expires: 600", "User-Agent: vdome-go/0.1"}
	if auth != "" {
		lines = append(lines, "Authorization: "+auth)
	}
	return strings.Join(append(lines, "Content-Length: 0", "", ""), "\r\n")
}

type message struct {
	first   string
	headers map[string][]string
}

func (m message) one(name string) string {
	v := m.headers[name]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}
func parse(raw string) (message, error) {
	m := message{headers: map[string][]string{}}
	lines := strings.Split(strings.SplitN(raw, "\r\n\r\n", 2)[0], "\r\n")
	if len(lines) < 2 || len(strings.Fields(lines[0])) < 3 {
		return m, errors.New("malformed SIP start line")
	}
	m.first = lines[0]
	aliases := map[string]string{"v": "via", "f": "from", "t": "to", "i": "call-id", "l": "content-length"}
	last := ""
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if last == "" {
				return m, errors.New("invalid SIP continuation")
			}
			values := m.headers[last]
			values[len(values)-1] += " " + strings.TrimSpace(line)
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return m, errors.New("malformed SIP header")
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if alias := aliases[name]; alias != "" {
			name = alias
		}
		m.headers[name] = append(m.headers[name], strings.TrimSpace(value))
		last = name
	}
	for _, name := range []string{"from", "to", "call-id", "cseq"} {
		if len(m.headers[name]) != 1 {
			return m, errors.New("missing or ambiguous SIP header")
		}
	}
	if len(m.headers["via"]) == 0 {
		return m, errors.New("missing SIP Via")
	}
	return m, nil
}
func (m message) response(status string) string {
	lines := []string{"SIP/2.0 " + status}
	for _, value := range m.headers["via"] {
		lines = append(lines, "Via: "+value)
	}
	for _, name := range []string{"from", "to", "call-id", "cseq"} {
		lines = append(lines, name+": "+m.one(name))
	}
	return strings.Join(append(lines, "Content-Length: 0", "", ""), "\r\n")
}

var identityPattern = regexp.MustCompile(`(?i)sips?:([^@;>\s]+)@`)

func Identity(value string) string {
	if m := identityPattern.FindStringSubmatch(value); len(m) == 2 {
		return m[1]
	}
	return strings.TrimSpace(value)
}

var authPattern = regexp.MustCompile(`([a-zA-Z]+)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

func digest(c Credential, challenge string) (string, error) {
	if !strings.HasPrefix(challenge, "Digest ") {
		return "", errors.New("unsupported SIP authentication")
	}
	params := map[string]string{}
	for _, match := range authPattern.FindAllStringSubmatch(challenge, -1) {
		v := match[2]
		if v == "" {
			v = match[3]
		}
		params[strings.ToLower(match[1])] = v
	}
	if params["realm"] == "" || params["nonce"] == "" || strings.ContainsAny(params["realm"]+params["nonce"], "\r\n\"\\") {
		return "", errors.New("invalid SIP challenge")
	}
	if algorithm := strings.ToUpper(params["algorithm"]); algorithm != "" && algorithm != "MD5" {
		return "", errors.New("unsupported SIP digest algorithm")
	}
	// MD5 is mandated by this remote protocol, not used for local storage.
	hash := func(s string) string { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	registrar := c.Registrar
	if registrar == "" {
		registrar = c.Domain
	}
	if !safe(registrar) {
		return "", errors.New("invalid SIP registrar")
	}
	uri := "sip:" + registrar
	ha1 := hash(c.Username + ":" + params["realm"] + ":" + c.Secret)
	ha2 := hash("REGISTER:" + uri)
	response := hash(ha1 + ":" + params["nonce"] + ":" + ha2)
	extra := ""
	if qop := params["qop"]; qop != "" {
		found := false
		for _, v := range strings.Split(qop, ",") {
			if strings.TrimSpace(v) == "auth" {
				found = true
			}
		}
		if !found {
			return "", errors.New("unsupported SIP qop")
		}
		cnonce := random()
		response = hash(ha1 + ":" + params["nonce"] + ":00000001:" + cnonce + ":auth:" + ha2)
		extra = ",qop=auth,nc=00000001,cnonce=" + strconv.Quote(cnonce)
	}
	return "Digest algorithm=MD5,username=" + strconv.Quote(c.Username) + ",realm=" + strconv.Quote(params["realm"]) + ",nonce=" + strconv.Quote(params["nonce"]) + ",uri=" + strconv.Quote(uri) + ",response=" + strconv.Quote(response) + extra, nil
}
