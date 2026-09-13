package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// ErrNoSecretKey is returned when no encryption key was supplied.
var ErrNoSecretKey = errors.New("NABIZ_SECRET_KEY is not set: diagnostics tokens cannot be stored")

// Sealer encrypts the applications' diagnostics tokens.
//
// These tokens are what let someone take another system's process memory. If
// the database leaks through a backup, a query that ends up in a log, or a
// wrong permission, having them sitting in plain text is not acceptable.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer builds an encrypter from a key string. It returns nil when the key
// is empty: the caller must then refuse to store tokens.
func NewSealer(key string) (*Sealer, error) {
	if key == "" {
		return nil, ErrNoSecretKey
	}
	// The key can be free text; it is reduced to a fixed length.
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plain text and returns base64.
func (s *Sealer) Seal(plaintext string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts the ciphertext.
func (s *Sealer) Open(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("could not decode the token: %w", err)
	}
	if len(raw) < s.aead.NonceSize() {
		return "", errors.New("the encrypted token is invalid")
	}
	nonce, ciphertext := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	plain, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// This is where a changed key blows up; the message has to say so.
		return "", fmt.Errorf("could not open the token (NABIZ_SECRET_KEY may have changed): %w", err)
	}
	return string(plain), nil
}
