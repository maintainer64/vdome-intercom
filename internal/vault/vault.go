package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
)

type Vault struct {
	Path string
	aead cipher.AEAD
}

var aad = []byte("vdome-vault-v1")

// Local keeps a generated key in an owner-only file for unattended local use.
// An environment-managed key can still be supplied via New instead.
func Local(path, keyPath string) (*Vault, error) {
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		f, createErr := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(createErr) {
			key, err = os.ReadFile(keyPath)
		} else if createErr != nil {
			return nil, createErr
		} else {
			_, err = f.Write(key)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("local vault key must have permissions 0600")
	}
	return New(path, base64.StdEncoding.EncodeToString(key))
}

func New(path, key string) (*Vault, error) {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(b) != 32 {
		return nil, errors.New("VDOME_VAULT_KEY должен содержать 32 случайных байта в base64")
	}
	block, err := aes.NewCipher(b)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{Path: path, aead: aead}, nil
}

func (v *Vault) Load() ([]byte, error) {
	b, err := os.ReadFile(v.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n := v.aead.NonceSize()
	if len(b) < n {
		return nil, errors.New("повреждённое хранилище")
	}
	return v.aead.Open(nil, b[:n], b[n:], aad)
}

func (v *Vault) Save(plain []byte) error {
	if err := os.MkdirAll(filepath.Dir(v.Path), 0700); err != nil {
		return err
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	b := v.aead.Seal(nonce, nonce, plain, aad)
	f, err := os.CreateTemp(filepath.Dir(v.Path), ".vault-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), v.Path)
}
