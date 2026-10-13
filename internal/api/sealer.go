package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// sealKeyInfo binds the derived key to this one use, so the same secret
	// could key something else in future without the two keys being equal.
	sealKeyInfo = "gitlab-achievements/session-tokens/v1"
	// sealPrefix versions the sealed format, so a later change of cipher
	// can tell the two apart instead of failing to decrypt.
	sealPrefix = "v1."
	// sealKeyBytes is AES-256's key size.
	sealKeyBytes = 32
)

// errUnsealable reports a stored credential that cannot be decrypted: it was
// sealed under a different secret, written before tokens were encrypted at
// all, or tampered with. All three mean the session is unusable.
var errUnsealable = errors.New("stored credential cannot be decrypted")

// sealer encrypts the GitLab tokens a session holds, so a copy of the
// database (a backup, a dump attached to a ticket) does not hand out
// working credentials for everyone logged in.
//
// It is AES-256-GCM under a key derived from a secret the operator supplies
// (SESSION_SECRET, or failing that the webhook secret, which every
// deployment already has and keeps out of the database). Each ciphertext is
// bound to the session it belongs to through GCM's additional data, so a
// sealed token copied onto another session's row does not decrypt there.
type sealer struct {
	aead cipher.AEAD
}

// newSealer derives a sealer's key from secret.
func newSealer(secret string) (*sealer, error) {
	if secret == "" {
		return nil, errors.New("a secret is required to encrypt session tokens")
	}

	key, err := hkdf.Key(sha256.New, []byte(secret), nil, sealKeyInfo, sealKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to derive the session encryption key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to build the session cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to build the session cipher: %w", err)
	}

	return &sealer{aead: aead}, nil
}

// seal encrypts plaintext for the row identified by binding. An empty
// plaintext seals to the empty string, so an absent refresh token stays
// recognizably absent.
func (s *sealer) seal(plaintext, binding string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	nonce := make([]byte, s.aead.NonceSize())

	_, err := rand.Read(nonce)
	if err != nil {
		return "", fmt.Errorf("failed to generate a nonce: %w", err)
	}

	sealed := s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(binding))

	return sealPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open decrypts what seal produced for the same binding.
func (s *sealer) open(sealed, binding string) (string, error) {
	if sealed == "" {
		return "", nil
	}

	encoded, ok := strings.CutPrefix(sealed, sealPrefix)
	if !ok {
		return "", errUnsealable
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", errUnsealable
	}

	nonce, ciphertext := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]

	plaintext, err := s.aead.Open(nil, nonce, ciphertext, []byte(binding))
	if err != nil {
		return "", errUnsealable
	}

	return string(plaintext), nil
}
