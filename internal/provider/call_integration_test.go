package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"vdome/internal/app"
	"vdome/internal/domain"

	"github.com/gorilla/websocket"
)

// This fixture runs the real Go listener, provider and automation together.
// Every HTTP/WS endpoint is local, including the unlock target.
func TestLiveSIPShortCallReachesAutomationAndUnknownCallerDoesNot(t *testing.T) {
	var opens atomic.Int32
	send := make(chan string)
	sent := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	up := websocket.Upgrader{Subprotocols: []string{"sip"}, CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		lines := []string{"SIP/2.0 200 OK"}
		for _, line := range strings.Split(string(raw), "\r\n") {
			name, _, _ := strings.Cut(line, ":")
			switch strings.ToLower(name) {
			case "via", "from", "to", "call-id", "cseq":
				lines = append(lines, line)
			}
		}
		lines = append(lines, "Expires: 600", "Content-Length: 0", "", "")
		if conn.WriteMessage(websocket.TextMessage, []byte(strings.Join(lines, "\r\n"))) != nil {
			return
		}
		for {
			select {
			case caller := <-send:
				invite := fmt.Sprintf("INVITE sip:resident@fixture.test SIP/2.0\r\nVia: SIP/2.0/WSS door.invalid;branch=z9hG4bKfixture\r\nFrom: <sip:%s@fixture.test>;tag=caller\r\nTo: <sip:apartment-alias@fixture.test>\r\nCall-ID: %s-call\r\nCSeq: 1 INVITE\r\nContent-Length: 0\r\n\r\n", caller, caller)
				if err := conn.WriteMessage(websocket.TextMessage, []byte(invite)); err != nil {
					sent <- err
					return
				}
				if _, _, err := conn.ReadMessage(); err != nil {
					sent <- err
					return
				}
				cancel := strings.Replace(strings.Replace(invite, "INVITE sip:", "CANCEL sip:", 1), "1 INVITE", "1 CANCEL", 1)
				if err := conn.WriteMessage(websocket.TextMessage, []byte(cancel)); err != nil {
					sent <- err
					return
				}
				_, _, err := conn.ReadMessage()
				sent <- err
			case <-stop:
				return
			}
		}
	})
	mux.HandleFunc("GET /domofon/api/security/v1/users/self", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"list":[{"id":"owner","auth":[{"credential":{"sip":"resident","secret":"fixture-password","domain":"fixture.test"}}]}]}`)
	})
	mux.HandleFunc("GET /domofon/api/intercom/v2/intercoms", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category") == "barrier" {
			fmt.Fprint(w, `{"list":[]}`)
			return
		}
		fmt.Fprint(w, `{"list":[{"id":"door","phone":"known-door","name":"Fixture door","can_unlock":true,"monitoring":{"api":true,"sip":true}}]}`)
	})
	mux.HandleFunc("GET /domofon/api/intercom/v1/cameras", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"list":[]}`) })
	mux.HandleFunc("POST /vdome-smart-facade/devices/api/v1/intercoms/door/lock", func(w http.ResponseWriter, r *http.Request) { opens.Add(1); fmt.Fprint(w, `{"success":true}`) })
	server := httptest.NewServer(mux)
	defer server.Close()
	p := NewLive()
	p.gateway = server.URL
	p.ws = "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	m, err := app.New(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	_, token, err := m.Login(context.Background(), domain.Credentials{SSOResponse: `{"keycloakTokenPair":{"accessToken":"fixture-access","refreshToken":"fixture-refresh"}}`})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_ = m.Poll(context.Background())
		view, _ := m.View(token)
		if view.CallReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SIP did not register")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.SetMode(token, "door", domain.Next); err != nil {
		t.Fatal(err)
	}
	transmit := func(caller string) {
		t.Helper()
		select {
		case send <- caller:
		case <-time.After(time.Second):
			t.Fatal("fixture disconnected")
		}
		select {
		case err := <-sent:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("fixture call timed out")
		}
	}
	transmit("known-door") // INVITE and CANCEL both complete before the next poll.
	for range 2 {
		if err := m.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	view, _ := m.View(token)
	if opens.Load() != 1 || view.Rules["door"].Mode != domain.Off || len(view.Audit) != 2 || len(view.IncomingCalls) != 0 {
		t.Fatalf("short call was lost or duplicated: opens=%d audit=%d incoming=%d", opens.Load(), len(view.Audit), len(view.IncomingCalls))
	}
	transmit("unknown-door")
	if err := m.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, _ = m.View(token)
	if opens.Load() != 1 || view.CallStatus.UnmappedCalls != 1 || view.CallStatus.AcceptedInvites != 2 {
		t.Fatalf("unknown caller handling: opens=%d status=%+v", opens.Load(), view.CallStatus)
	}
	report := m.ConnectionReport()
	for _, private := range []string{"owner", "resident", "fixture-password", "fixture-access", "known-door", "unknown-door"} {
		if strings.Contains(report, private) {
			t.Fatal("private data in diagnostics")
		}
	}
}
