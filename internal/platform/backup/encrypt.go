package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
)

// Backup files contain the full database: password hashes, provider secrets and
// every private message. They are encrypted at rest with AES-256-GCM so a
// leaked backup directory is not a data breach on its own.
//
// The key comes from GF_BACKUP_KEY (base64, 32 bytes) or
// security.backup_key in the config. When neither is set, Run refuses to write
// a backup unless encryption is explicitly disabled, because silently writing
// plaintext would defeat the point.
const backupKeyEnv = "GF_BACKUP_KEY"

// magic prefix identifies an encrypted backup payload.
var magic = []byte("GFBK1")

// errNoKey is returned when a key is required but not configured.
var errNoKey = errors.New("backup encryption key is not configured")

// resolveKey returns the 32-byte AES key, or nil when encryption is disabled.
func resolveKey(configured string, allowPlaintext bool) ([]byte, error) {
	// An explicit value always wins so callers can derive a different key in
	// the same process (tests, key rotation).
	raw := configured
	if raw == "" {
		raw = os.Getenv(backupKeyEnv)
	}
	if raw == "" {
		if allowPlaintext {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: set %s (openssl rand -base64 32) or enable allow_plaintext", errNoKey, backupKeyEnv)
	}
	sum := sha256.Sum256([]byte(raw))
	// A hash keeps the key valid for any passphrase length. A dedicated random
	// 32-byte value is still preferable.
	return sum[:], nil
}

// encryptFile replaces path with its encrypted form and returns the new size.
// The layout is: magic || nonce || ciphertext+tag.
func encryptFile(path string, key []byte) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}

	tmp := path + ".enc"
	dst, err := os.Create(tmp)
	if err != nil {
		_ = src.Close()
		return err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	if _, err := dst.Write(magic); err != nil {
		return err
	}
	if _, err := dst.Write(nonce); err != nil {
		return err
	}
	writer := &gcmWriter{w: dst, gcm: gcm, nonce: nonce}
	_, copyErr := io.Copy(writer, src)
	closeErr := writer.close()
	// Close both handles before touching the files: Windows refuses to unlink
	// or rename a file that is still open.
	_ = src.Close()
	dbErr := dst.Close()
	if copyErr != nil {
		return fmt.Errorf("encrypt: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if dbErr != nil {
		return dbErr
	}
	// Replace the plaintext file so no unencrypted copy remains.
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// gcmWriter encrypts a stream in chunks. Each chunk is sealed with the same
// nonce prefix plus a counter, which keeps the code simple while still
// authenticating every byte.
type gcmWriter struct {
	w      io.Writer
	gcm    cipher.AEAD
	nonce  []byte
	buf    []byte
	seq    uint32
	closed bool
}

const chunkSize = 64 * 1024

func (g *gcmWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := len(p)
		if n > chunkSize {
			n = chunkSize
		}
		g.buf = append(g.buf[:0], p[:n]...)
		if err := g.flushChunk(); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (g *gcmWriter) flushChunk() error {
	// Derive a per-chunk nonce from the base nonce and a sequence number so the
	// same key+nonce pair is never reused for two different plaintexts.
	chunkNonce := make([]byte, len(g.nonce))
	copy(chunkNonce, g.nonce)
	chunkNonce[len(chunkNonce)-4] ^= byte(g.seq >> 24)
	chunkNonce[len(chunkNonce)-3] ^= byte(g.seq >> 16)
	chunkNonce[len(chunkNonce)-2] ^= byte(g.seq >> 8)
	chunkNonce[len(chunkNonce)-1] ^= byte(g.seq)
	g.seq++

	sealed := g.gcm.Seal(nil, chunkNonce, g.buf, nil)
	// Length prefix lets the reader split the stream back into chunks.
	var hdr [4]byte
	hdr[0] = byte(len(sealed) >> 24)
	hdr[1] = byte(len(sealed) >> 16)
	hdr[2] = byte(len(sealed) >> 8)
	hdr[3] = byte(len(sealed))
	if _, err := g.w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := g.w.Write(sealed)
	return err
}

func (g *gcmWriter) close() error {
	if g.closed {
		return nil
	}
	g.closed = true
	// Signal end of stream with a zero length chunk.
	_, err := g.w.Write([]byte{0, 0, 0, 0})
	return err
}

// DecryptFile restores an encrypted backup produced by encryptFile.
func DecryptFile(path string, key []byte) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < len(magic) || string(raw[:len(magic)]) != string(magic) {
		return nil, errors.New("not an encrypted GoForum backup")
	}
	raw = raw[len(magic):]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("truncated backup")
	}
	nonce := raw[:gcm.NonceSize()]
	raw = raw[gcm.NonceSize():]

	var out []byte
	var seq uint32
	for {
		if len(raw) < 4 {
			return nil, errors.New("truncated chunk header")
		}
		size := int(raw[0])<<24 | int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
		raw = raw[4:]
		if size == 0 {
			return out, nil
		}
		if len(raw) < size {
			return nil, errors.New("truncated chunk body")
		}
		chunkNonce := make([]byte, len(nonce))
		copy(chunkNonce, nonce)
		chunkNonce[len(chunkNonce)-4] ^= byte(seq >> 24)
		chunkNonce[len(chunkNonce)-3] ^= byte(seq >> 16)
		chunkNonce[len(chunkNonce)-2] ^= byte(seq >> 8)
		chunkNonce[len(chunkNonce)-1] ^= byte(seq)
		seq++
		plain, err := gcm.Open(nil, chunkNonce, raw[:size], nil)
		if err != nil {
			return nil, fmt.Errorf("decrypt chunk %d: %w", seq-1, err)
		}
		out = append(out, plain...)
		raw = raw[size:]
	}
}

// GenerateKey returns a new base64-encoded 32-byte key for GF_BACKUP_KEY.
func GenerateKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// KeyFromEnv resolves the key from GF_BACKUP_KEY for the restore command.
func KeyFromEnv() ([]byte, error) {
	return resolveKey(os.Getenv(backupKeyEnv), false)
}
