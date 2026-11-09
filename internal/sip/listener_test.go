package sip

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSIPRegisterInviteRetransmitAndCancel(t *testing.T) {
	ready := make(chan struct{})
	sendCancel := make(chan struct{})
	fail := make(chan error, 1)
	u := websocket.Upgrader{Subprotocols: []string{"sip"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := u.Upgrade(w, r, nil)
		if err != nil {
			fail <- err
			return
		}
		defer c.Close()
		_, raw, err := c.ReadMessage()
		if err != nil {
			fail <- err
			return
		}
		reg, err := parse(string(raw))
		if err != nil {
			fail <- err
			return
		}
		if !strings.HasPrefix(reg.one("via"), "SIP/2.0/WS ") || !strings.Contains(reg.one("contact"), ";transport=ws>") {
			fail <- fmt.Errorf("registration transport does not match WebSocket")
			return
		}
		challenge := strings.Replace(reg.response("401 Unauthorized"), "Content-Length: 0", `WWW-Authenticate: Digest realm="fixture",nonce="abcd",algorithm=MD5`+"\r\nContent-Length: 0", 1)
		if err := c.WriteMessage(websocket.TextMessage, []byte(challenge)); err != nil {
			fail <- err
			return
		}
		_, raw, err = c.ReadMessage()
		if err != nil {
			fail <- err
			return
		}
		reg, err = parse(string(raw))
		if err != nil {
			fail <- err
			return
		}
		if !strings.HasPrefix(reg.one("authorization"), "Digest ") || strings.Contains(reg.one("authorization"), "password") {
			fail <- fmt.Errorf("invalid digest authentication")
			return
		}
		if err := c.WriteMessage(websocket.TextMessage, []byte(reg.response("200 OK"))); err != nil {
			fail <- err
			return
		}
		if err := c.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second)); err != nil {
			fail <- err
			return
		}
		if err := c.WriteMessage(websocket.TextMessage, []byte("\r\n\r\n")); err != nil {
			fail <- err
			return
		}
		if _, pong, err := c.ReadMessage(); err != nil || string(pong) != "\r\n" {
			fail <- fmt.Errorf("SIP keepalive not answered")
			return
		}
		invite := "INVITE sip:resident@fixture.test SIP/2.0\r\nVia: SIP/2.0/TCP fixture;branch=z9hG4bK123\r\nFrom: <sip:door@fixture.test>;tag=caller\r\nTo: <sip:resident@fixture.test>\r\nCall-ID: live-call\r\nCSeq: 1 INVITE\r\nContent-Length: 0\r\n\r\n"
		for range 2 {
			if err := c.WriteMessage(websocket.TextMessage, []byte(invite)); err != nil {
				fail <- err
				return
			}
			if _, _, err := c.ReadMessage(); err != nil {
				fail <- err
				return
			}
		}
		close(ready)
		<-sendCancel
		cancel := strings.Replace(strings.Replace(invite, "INVITE sip:", "CANCEL sip:", 1), "1 INVITE", "1 CANCEL", 1)
		_ = c.WriteMessage(websocket.TextMessage, []byte(cancel))
		_, _, _ = c.ReadMessage()
	}))
	defer server.Close()
	defer func() {
		select {
		case <-sendCancel:
		default:
			close(sendCancel)
		}
	}()
	l := New(Credential{Domain: "fixture.test", Username: "resident", Secret: "password"}, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer l.Close()
	select {
	case <-ready:
	case err := <-fail:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("SIP fixture timed out")
	}
	events, registered := l.Events()
	if !registered || len(events) != 1 || events[0].From != "door" || events[0].ID != "live-call" {
		t.Fatalf("events=%+v registered=%v", events, registered)
	}
	close(sendCancel)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events, _ = l.Events()
		if len(events) == 1 && events[0].Ended {
			status := l.Status()
			if status.ReceivedInvites != 2 || status.AcceptedInvites != 1 || status.RejectedInvites != 0 || status.Pongs == 0 {
				t.Fatalf("unexpected counters: %+v", status)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("CANCEL did not end the call while retaining its occurrence")
}

func TestRegistrationTransportsAndNegotiatedExpiry(t *testing.T) {
	for _, transport := range []string{"WS", "WSS"} {
		r := registration{credential: Credential{Username: "resident", Domain: "fixture.test"}, transport: transport, contact: "our.invalid"}
		m, err := parse(r.request(""))
		if err != nil || !strings.HasPrefix(m.one("via"), "SIP/2.0/"+transport+" ") || !strings.Contains(m.one("contact"), ";transport="+strings.ToLower(transport)+">") {
			t.Fatal("incorrect WebSocket registration transport")
		}
	}
	for _, tc := range []struct {
		expires, contact string
		want             time.Duration
		invalid          bool
	}{
		{"", "", 480 * time.Second, false},
		{"60", "", 48 * time.Second, false},
		{"600", "<sip:resident@our.invalid;transport=wss>;expires=30", 24 * time.Second, false},
		{"600", "<sip:resident@other.invalid>;expires=1", 480 * time.Second, false},
		{"0", "", 0, true},
		{"invalid", "", 0, true},
	} {
		m := message{headers: map[string][]string{"expires": {tc.expires}, "contact": {tc.contact}}}
		got, err := registrationRefresh(m, "our.invalid")
		if (err != nil) != tc.invalid || got != tc.want {
			t.Fatalf("expires=%q: got %s, err=%v", tc.expires, got, err)
		}
	}
}

func TestConnectionDiagnosticsDoNotExposeServerBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private-token-and-account", 403) }))
	defer server.Close()
	l := New(Credential{Username: "u", Domain: "fixture.test", Secret: "password"}, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer l.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s := l.Status()
		if s.State == "retrying" {
			if s.HTTPStatus != 403 || s.Reason != "websocket_failed" {
				t.Fatalf("unexpected diagnostics: %+v", s)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("missing failed connection diagnostics")
}
func TestSIPRejectsAmbiguousHeadersAndUnsupportedDigest(t *testing.T) {
	if _, err := parse("INVITE sip:u@d SIP/2.0\r\nFrom: <sip:a@d>\r\nFrom: <sip:b@d>\r\nTo: <sip:u@d>\r\nVia: SIP/2.0/TCP d\r\nCall-ID: a\r\nCSeq: 1 INVITE\r\n\r\n"); err == nil {
		t.Fatal("ambiguous origin accepted")
	}
	for _, challenge := range []string{"Basic", `Digest realm="x",nonce="n",algorithm=SHA-512`, "Digest realm=\"x\",nonce=\"n\r\nInjected: yes\",algorithm=MD5"} {
		if _, err := digest(Credential{Username: "u", Secret: "p", Domain: "d"}, challenge); err == nil {
			t.Fatal("unsupported or malformed challenge accepted")
		}
	}
}
