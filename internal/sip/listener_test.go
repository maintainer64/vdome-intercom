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
		if len(events) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("CANCEL did not clear live call")
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
