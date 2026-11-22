package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt("sk-upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "sk-upstream-secret" {
		t.Fatal("ciphertext equals plaintext")
	}
	dec, err := box.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "sk-upstream-secret" {
		t.Fatalf("round trip = %q", dec)
	}
	if _, err := box.Decrypt("AAAA"); err == nil {
		t.Fatal("expected decrypt error")
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(k1) != string(k2) {
		t.Fatal("key was not stable across loads")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v", info.Mode().Perm())
	}
}
