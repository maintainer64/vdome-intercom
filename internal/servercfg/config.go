package servercfg

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

type Config struct{ Addr, Origin, CertFile, KeyFile string }

func Read(getenv func(string) string) (Config, error) {
	c := Config{Addr: getenv("VDOME_ADDR"), Origin: getenv("VDOME_ORIGIN"), CertFile: getenv("VDOME_TLS_CERT_FILE"), KeyFile: getenv("VDOME_TLS_KEY_FILE")}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return c, errors.New("VDOME_TLS_CERT_FILE и VDOME_TLS_KEY_FILE должны быть заданы вместе")
	}
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8080"
	}
	if c.Origin == "" {
		scheme := "http"
		if c.TLS() {
			scheme = "https"
		}
		c.Origin = scheme + "://127.0.0.1:8080"
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return c, errors.New("VDOME_ORIGIN должен быть http(s)://хост[:порт] без пути")
	}
	if c.TLS() && u.Scheme != "https" {
		return c, errors.New("для прямого TLS задайте VDOME_ORIGIN=https://ваш-домен")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return c, errors.New("VDOME_ADDR должен содержать хост:порт")
	}
	return c, nil
}
func (c Config) TLS() bool { return c.CertFile != "" && c.KeyFile != "" }

// Probe uses the internal listener, including when a reverse proxy terminates
// HTTPS. Direct TLS trusts the configured certificate and verifies its name.
func (c Config) Probe(ctx context.Context) error {
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	scheme := "http"
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	if c.TLS() {
		scheme = "https"
		pem, err := os.ReadFile(c.CertFile)
		if err != nil {
			return fmt.Errorf("read TLS certificate: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("invalid TLS certificate PEM")
		}
		origin, _ := url.Parse(c.Origin)
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: origin.Hostname()}
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", scheme+"://"+net.JoinHostPort(host, port)+"/healthz", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: HTTP %d", response.StatusCode)
	}
	return nil
}
