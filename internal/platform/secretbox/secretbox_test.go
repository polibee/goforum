package secretbox

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	key := "test-secret-key"
	plaintext := []byte(`"smtp-password"`)
	encoded, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !IsEncrypted(encoded) || strings.Contains(encoded, "smtp-password") {
		t.Fatalf("ciphertext leaks plaintext: %q", encoded)
	}
	decoded, err := Decrypt(key, encoded)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(decoded, plaintext) {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestWrongKeyFails(t *testing.T) {
	encoded, err := Encrypt("key-one", []byte("value"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := Decrypt("key-two", encoded); err == nil {
		t.Fatal("wrong key decrypted")
	}
}

func TestMissingKey(t *testing.T) {
	if _, err := Encrypt("", []byte("value")); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := Decrypt("", "v1:abc"); err == nil {
		t.Fatal("empty key decrypted")
	}
}

func TestUnencryptedValueRejected(t *testing.T) {
	if _, err := Decrypt("key", "plain"); err == nil {
		t.Fatal("plain value decrypted")
	}
}

func TestBase64Key(t *testing.T) {
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	encoded, err := Encrypt(key, []byte("value"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := Decrypt(key, encoded); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
}
