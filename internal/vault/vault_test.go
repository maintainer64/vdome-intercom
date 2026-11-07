package vault

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptionPermissionsWrongKeyAndTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.vault")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	v, err := New(path, key)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"access_token":"test-secret"}`)
	if err := v.Save(plain); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if bytes.Contains(b, []byte("test-secret")) {
		t.Fatal("plaintext persisted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("incorrect file permissions")
	}
	got, err := v.Load()
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatal("roundtrip failed")
	}
	other, _ := New(path, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	if _, err := other.Load(); err == nil {
		t.Fatal("wrong key accepted")
	}
	b[len(b)-1] ^= 1
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Load(); err == nil {
		t.Fatal("tampering accepted")
	}
}

func TestLocalKeySurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "live.vault")
	key := filepath.Join(dir, "data", "vault.key")
	first, err := Local(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Save([]byte("saved-session")); err != nil {
		t.Fatal(err)
	}
	second, err := Local(path, key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Load()
	if err != nil || string(b) != "saved-session" {
		t.Fatal("session did not survive reopen")
	}
	info, _ := os.Stat(key)
	if info.Mode().Perm() != 0600 {
		t.Fatal("local key not private")
	}
}
