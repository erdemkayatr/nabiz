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

// ErrNoSecretKey, şifreleme anahtarı verilmediğinde döner.
var ErrNoSecretKey = errors.New("NABIZ_SECRET_KEY tanımlı değil: tanılama jetonu saklanamaz")

// Sealer, uygulamaların tanılama jetonlarını şifreler.
//
// Bu jetonlar başka sistemlerin süreç belleğini almaya yarıyor. Veritabanı
// bir yedekten, bir loga düşen sorgudan ya da yanlış bir izinden sızarsa,
// jetonların düz metin durması kabul edilemez.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer, anahtar dizesinden şifreleyici kurar. Anahtar boşsa nil döner:
// çağıran, jeton saklamayı reddetmelidir.
func NewSealer(key string) (*Sealer, error) {
	if key == "" {
		return nil, ErrNoSecretKey
	}
	// Anahtar serbest metin olabilir; sabit uzunluğa indiriyoruz.
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

// Seal, düz metni şifreler ve base64 döndürür.
func (s *Sealer) Seal(plaintext string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open, şifreli metni çözer.
func (s *Sealer) Open(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("jeton çözümlenemedi: %w", err)
	}
	if len(raw) < s.aead.NonceSize() {
		return "", errors.New("şifreli jeton geçersiz")
	}
	nonce, ciphertext := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	plain, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Anahtar değiştiyse burası patlar; mesaj bunu açıkça söylemeli.
		return "", fmt.Errorf("jeton açılamadı (NABIZ_SECRET_KEY değişmiş olabilir): %w", err)
	}
	return string(plain), nil
}
