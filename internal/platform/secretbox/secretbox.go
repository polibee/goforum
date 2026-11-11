// Package secretbox encrypts small values at rest with AES-256-GCM.
//
// The site key comes from APP_KEY / GF_SECRET_KEY. A base64-encoded 32-byte
// key is used directly; any other non-empty string is hashed to 32 bytes so
// older installations with a passphrase-style key keep working.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const Prefix = "v1:"

var ErrKeyMissing = errors.New("encryption key is not configured")

func IsEncrypted(value string) bool { return strings.HasPrefix(value, Prefix) }

func Encrypt(key string, plaintext []byte) (string, error) {
	derived, err := deriveKey(key)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return Prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func Decrypt(key, encoded string) ([]byte, error) {
	if !IsEncrypted(encoded) {
		return nil, errors.New("value is not encrypted")
	}
	derived, err := deriveKey(key)
	if err != nil {
		return nil, err
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, Prefix))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

func deriveKey(key string) ([]byte, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrKeyMissing
	}
	if decoded, err := base64.StdEncoding.DecodeString(key); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	sum := sha256.Sum256([]byte(key))
	return sum[:], nil
}
