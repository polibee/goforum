package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	t.Setenv(backupKeyEnv, "test-key-for-unit-tests-only")
	key, err := resolveKey("", false)
	if err != nil {
		t.Fatalf("resolve key: %v", err)
	}
	if key == nil {
		t.Fatal("key should not be nil")
	}
	return key
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "database.sql")
	original := bytes.Repeat([]byte("INSERT INTO users VALUES (1,'admin');\n"), 5000)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	key := testKey(t)
	if err := encryptFile(path, key); err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("INSERT INTO users")) {
		t.Fatal("plaintext still visible after encryption")
	}
	if !bytes.HasPrefix(raw, magic) {
		t.Fatal("missing magic header")
	}

	plain, err := DecryptFile(path, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(plain, original) {
		t.Fatalf("round trip mismatch: got %d bytes want %d", len(plain), len(original))
	}
}

// A large file spans many chunks; every chunk must decrypt in order.
func TestEncryptMultiChunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	original := make([]byte, 300*1024)
	for i := range original {
		original[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	key := testKey(t)
	if err := encryptFile(path, key); err != nil {
		t.Fatal(err)
	}
	plain, err := DecryptFile(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, original) {
		t.Fatal("multi-chunk round trip mismatch")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(backupKeyEnv, "primary-key")
	key, err := resolveKey("", false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "db.sql")
	if err := os.WriteFile(path, []byte("secret data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := encryptFile(path, key); err != nil {
		t.Fatal(err)
	}
	// A different passphrase must yield a different key and fail to decrypt.
	wrong, err := resolveKey("a-different-passphrase", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptFile(path, wrong); err == nil {
		t.Fatal("decrypt with the wrong key must fail")
	}
}

// Tampering with the ciphertext must be detected by the AEAD tag.
func TestDecryptTamperedFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sql")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	key := testKey(t)
	if err := encryptFile(path, key); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	raw[len(raw)-10] ^= 0xFF // corrupt the tail
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptFile(path, key); err == nil {
		t.Fatal("tampered backup must not decrypt")
	}
}

func TestResolveKeyRequiresKeyByDefault(t *testing.T) {
	t.Setenv(backupKeyEnv, "")
	if _, err := resolveKey("", false); err == nil {
		t.Fatal("missing key must be an error when plaintext is not allowed")
	}
}

func TestResolveKeyAllowsPlaintext(t *testing.T) {
	t.Setenv(backupKeyEnv, "")
	key, err := resolveKey("", true)
	if err != nil {
		t.Fatalf("plaintext should be allowed: %v", err)
	}
	if key != nil {
		t.Fatal("plaintext mode should return a nil key")
	}
}

func TestGenerateKeyIsRandom(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("generated keys should differ")
	}
	if len(a) < 40 {
		t.Fatalf("key looks too short: %q", a)
	}
}

// Encrypting the same content twice must produce different ciphertext (new nonce).
func TestEncryptIsNondeterministic(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	var outputs [][]byte
	for i := 0; i < 2; i++ {
		p := filepath.Join(dir, "f"+string(rune('a'+i)))
		if err := os.WriteFile(p, []byte("same content"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := encryptFile(p, key); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(p)
		outputs = append(outputs, raw)
	}
	if bytes.Equal(outputs[0], outputs[1]) {
		t.Fatal("two encryptions produced identical output; nonce is not random")
	}
}
