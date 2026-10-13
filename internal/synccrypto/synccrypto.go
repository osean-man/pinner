// Package synccrypto holds the end-to-end encryption used by pinner sync.
//
// Every key is derived from one 32-byte secret that never leaves the user's devices.
// The server only ever sees the user ID, a bearer token, opaque record IDs and ciphertext.
package synccrypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	SecretSize   = 32
	padBlock     = 256
	secretPrefix = "PINR-"
	version      = "pinner/v1"
)

var (
	ErrBadSecret = errors.New("invalid sync secret")
	ErrDecrypt   = errors.New("record failed authentication: it was tampered with, corrupted or encrypted with a different key")
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random secret.
func GenerateSecret() ([]byte, error) {
	s := make([]byte, SecretSize)
	if _, err := io.ReadFull(rand.Reader, s); err != nil {
		return nil, err
	}
	return s, nil
}

// EncodeSecret formats a secret for display as PINR-XXXX-XXXX-..., with a checksum to catch typos.
func EncodeSecret(secret []byte) string {
	sum := sha256.Sum256(secret)
	enc := b32.EncodeToString(append(append([]byte{}, secret...), sum[:2]...))
	var groups []string
	for len(enc) > 0 {
		n := min(4, len(enc))
		groups = append(groups, enc[:n])
		enc = enc[n:]
	}
	return secretPrefix + strings.Join(groups, "-")
}

// DecodeSecret parses a secret produced by EncodeSecret. It ignores case, spaces and dashes.
func DecodeSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", " ", "").Replace(s)
	// Only strip the prefix when it is present in addition to the payload, since "PINR" is valid base32.
	if prefix := strings.TrimSuffix(secretPrefix, "-"); len(s) == b32.EncodedLen(SecretSize+2)+len(prefix) {
		s = strings.TrimPrefix(s, prefix)
	}
	raw, err := b32.DecodeString(s)
	if err != nil || len(raw) != SecretSize+2 {
		return nil, ErrBadSecret
	}
	secret, check := raw[:SecretSize], raw[SecretSize:]
	sum := sha256.Sum256(secret)
	if !hmac.Equal(check, sum[:2]) {
		return nil, fmt.Errorf("%w: checksum mismatch (typo?)", ErrBadSecret)
	}
	return secret, nil
}

// Keys are the values derived from a secret.
type Keys struct {
	UserID    string // public identifier of the account
	AuthToken string // bearer token; the server stores only its hash
	encKey    []byte
	ridKey    []byte
}

func derive(secret []byte, info string) []byte {
	out := make([]byte, 32)
	r := hkdf.New(sha256.New, secret, nil, []byte(version+"/"+info))
	if _, err := io.ReadFull(r, out); err != nil {
		panic(err) // only fails if more than 255*32 bytes are requested
	}
	return out
}

// DeriveKeys derives the account's identifiers and keys from its secret.
func DeriveKeys(secret []byte) (Keys, error) {
	if len(secret) != SecretSize {
		return Keys{}, ErrBadSecret
	}
	return Keys{
		UserID:    hex.EncodeToString(derive(secret, "user-id")),
		AuthToken: hex.EncodeToString(derive(secret, "auth-token")),
		encKey:    derive(secret, "enc-key"),
		ridKey:    derive(secret, "record-id"),
	}, nil
}

// RecordID maps a pin's UUID to the opaque identifier the server stores it under.
func (k Keys) RecordID(pinUUID string) string {
	m := hmac.New(sha256.New, k.ridKey)
	m.Write([]byte(pinUUID))
	return hex.EncodeToString(m.Sum(nil))
}

func (k Keys) aad(recordID string) []byte {
	return []byte(version + "|" + k.UserID + "|" + recordID)
}

// Seal pads and encrypts plaintext, binding the ciphertext to this account and record ID.
func (k Keys) Seal(recordID string, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.encKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+padBlock+aead.Overhead())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, pad(plaintext), k.aad(recordID)), nil
}

// Open authenticates and decrypts a blob produced by Seal for the same record ID.
func (k Keys) Open(recordID string, blob []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.encKey)
	if err != nil {
		return nil, err
	}
	if len(blob) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrDecrypt
	}
	padded, err := aead.Open(nil, blob[:aead.NonceSize()], blob[aead.NonceSize():], k.aad(recordID))
	if err != nil {
		return nil, ErrDecrypt
	}
	plain, err := unpad(padded)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

// pad prefixes the length and zero-fills to a multiple of padBlock, so ciphertext sizes don't reveal command lengths.
func pad(p []byte) []byte {
	n := 4 + len(p)
	size := (n + padBlock - 1) / padBlock * padBlock
	out := make([]byte, size)
	binary.BigEndian.PutUint32(out, uint32(len(p)))
	copy(out[4:], p)
	return out
}

func unpad(p []byte) ([]byte, error) {
	if len(p) < 4 {
		return nil, ErrDecrypt
	}
	n := binary.BigEndian.Uint32(p)
	if int(n) > len(p)-4 {
		return nil, ErrDecrypt
	}
	return p[4 : 4+n], nil
}
