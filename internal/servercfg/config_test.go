package servercfg

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestTLSConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name             string
		env              map[string]string
		wantTLS, wantErr bool
	}{
		{"plain HTTP", nil, false, false},
		{"TLS auto origin", map[string]string{"VDOME_TLS_CERT_FILE": "cert.pem", "VDOME_TLS_KEY_FILE": "key.pem"}, true, false},
		{"reverse proxy", map[string]string{"VDOME_ORIGIN": "https://intercom.example"}, false, false},
		{"missing key", map[string]string{"VDOME_TLS_CERT_FILE": "cert.pem"}, false, true},
		{"missing cert", map[string]string{"VDOME_TLS_KEY_FILE": "key.pem"}, false, true},
		{"insecure origin for TLS", map[string]string{"VDOME_TLS_CERT_FILE": "cert.pem", "VDOME_TLS_KEY_FILE": "key.pem", "VDOME_ORIGIN": "http://localhost:8080"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Read(func(key string) string { return tc.env[key] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if err == nil && c.TLS() != tc.wantTLS {
				t.Fatal("wrong transport")
			}
		})
	}
}
func TestHealthProbeVerifiesConfiguredCertificateAndName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Error("wrong health path")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	cert := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(server.URL)
	c := Config{Addr: u.Host, Origin: server.URL, CertFile: cert, KeyFile: "provided-by-server"}
	if err := c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Origin = "https://wrong.example"
	if err := c.Probe(context.Background()); err == nil {
		t.Fatal("accepted mismatched TLS name")
	}
}
func TestHealthProbeBehindHTTPSProxyUsesHTTPListener(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	if err := (Config{Addr: u.Host, Origin: "https://intercom.example"}).Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
}
