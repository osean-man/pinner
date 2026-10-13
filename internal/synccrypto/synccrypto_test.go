package synccrypto

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newKeys(t *testing.T) ([]byte, Keys) {
	t.Helper()
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	k, err := DeriveKeys(secret)
	if err != nil {
		t.Fatal(err)
	}
	return secret, k
}

func TestSecretEncodingRoundTripAndTypoDetection(t *testing.T) {
	secret, _ := newKeys(t)
	enc := EncodeSecret(secret)
	if !strings.HasPrefix(enc, "PINR-") {
		t.Fatalf("missing prefix: %s", enc)
	}
	for _, variant := range []string{enc, strings.ToLower(enc), " " + strings.ReplaceAll(enc, "-", " ") + " ", strings.TrimPrefix(enc, "PINR-")} {
		got, err := DecodeSecret(variant)
		if err != nil || !bytes.Equal(got, secret) {
			t.Fatalf("DecodeSecret(%q) = %x, %v", variant, got, err)
		}
	}

	// Change one character: the checksum must catch it.
	b := []byte(enc)
	i := len(b) - 3
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	if _, err := DecodeSecret(string(b)); !errors.Is(err, ErrBadSecret) {
		t.Fatalf("typo not detected: %v", err)
	}
	if _, err := DecodeSecret("PINR-ABCD"); !errors.Is(err, ErrBadSecret) {
		t.Fatalf("short secret accepted: %v", err)
	}
}

func TestDerivationIsDeterministicAndSeparated(t *testing.T) {
	secret, k1 := newKeys(t)
	k2, _ := DeriveKeys(secret)
	if k1.UserID != k2.UserID || k1.AuthToken != k2.AuthToken || k1.RecordID("x") != k2.RecordID("x") {
		t.Fatal("same secret must derive the same keys on every device")
	}
	vals := map[string]bool{k1.UserID: true, k1.AuthToken: true}
	if len(vals) != 2 || bytes.Equal(k1.encKey, k1.ridKey) {
		t.Fatal("derived values must be distinct")
	}
	_, other := newKeys(t)
	if other.UserID == k1.UserID || other.RecordID("x") == k1.RecordID("x") {
		t.Fatal("different secrets must not collide")
	}
	if _, err := DeriveKeys([]byte("short")); err == nil {
		t.Fatal("short secret should be rejected")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	_, k := newKeys(t)
	rid := k.RecordID("pin-1")
	for _, msg := range [][]byte{nil, []byte("ls -la"), bytes.Repeat([]byte("x"), 1000)} {
		blob, err := k.Seal(rid, msg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := k.Open(rid, blob)
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("round trip of %d bytes failed: %v", len(msg), err)
		}
		if bytes.Contains(blob, []byte("ls -la")) && len(msg) > 0 {
			t.Fatal("plaintext visible in ciphertext")
		}
	}
}

func TestSealHidesExactLength(t *testing.T) {
	_, k := newKeys(t)
	rid := k.RecordID("pin")
	a, _ := k.Seal(rid, []byte("ls"))
	b, _ := k.Seal(rid, bytes.Repeat([]byte("y"), 200))
	if len(a) != len(b) {
		t.Fatalf("2-byte and 200-byte commands produced different sizes: %d vs %d", len(a), len(b))
	}
	c, _ := k.Seal(rid, bytes.Repeat([]byte("y"), 300))
	if len(c) == len(a) {
		t.Fatal("expected a larger bucket for 300 bytes")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	secret, k := newKeys(t)
	ridA, ridB := k.RecordID("pin-a"), k.RecordID("pin-b")
	blob, err := k.Seal(ridA, []byte(`{"command":"echo safe"}`))
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < len(blob); i += 7 {
		mut := append([]byte{}, blob...)
		mut[i] ^= 0x01
		if _, err := k.Open(ridA, mut); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("flipping byte %d was not detected", i)
		}
	}
	if _, err := k.Open(ridB, blob); !errors.Is(err, ErrDecrypt) {
		t.Fatal("blob moved to another record slot was accepted")
	}
	if _, err := k.Open(ridA, blob[:len(blob)-1]); !errors.Is(err, ErrDecrypt) {
		t.Fatal("truncated blob was accepted")
	}
	if _, err := k.Open(ridA, nil); !errors.Is(err, ErrDecrypt) {
		t.Fatal("empty blob was accepted")
	}

	// Another account with the same record ID cannot read or forge.
	_, other := newKeys(t)
	if _, err := other.Open(ridA, blob); !errors.Is(err, ErrDecrypt) {
		t.Fatal("another account's key opened the blob")
	}
	// Same encryption key, different user ID in the AAD (e.g. the server moved it between accounts).
	moved := k
	moved.UserID = other.UserID
	if _, err := moved.Open(ridA, blob); !errors.Is(err, ErrDecrypt) {
		t.Fatal("blob moved to another account was accepted")
	}
	_ = secret
}
