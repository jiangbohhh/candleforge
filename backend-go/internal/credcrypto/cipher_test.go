package credcrypto

import (
	"encoding/hex"
	"strings"
	"testing"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := testCipher(t)
	for _, pt := range []string{"", "hello", "api-secret-xyz-🔑", strings.Repeat("a", 1000)} {
		ct, err := c.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		if !strings.HasPrefix(ct, prefix) {
			t.Fatalf("ciphertext missing prefix: %q", ct)
		}
		if pt != "" && strings.Contains(ct, pt) {
			t.Fatalf("plaintext leaked into ciphertext: %q", ct)
		}
		got, err := c.Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got != pt {
			t.Fatalf("round-trip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestEncryptNonceUnique(t *testing.T) {
	c := testCipher(t)
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonce reuse)")
	}
}

func TestNilCipherPassthrough(t *testing.T) {
	var c *Cipher
	ct, err := c.Encrypt("plain")
	if err != nil || ct != "plain" {
		t.Fatalf("nil Encrypt: got %q err %v", ct, err)
	}
	pt, err := c.Decrypt("plain")
	if err != nil || pt != "plain" {
		t.Fatalf("nil Decrypt legacy: got %q err %v", pt, err)
	}
}

func TestDecryptLegacyPlaintext(t *testing.T) {
	c := testCipher(t)
	// 无前缀的历史明文应原样返回（灰度迁移）。
	got, err := c.Decrypt("legacy-plaintext")
	if err != nil || got != "legacy-plaintext" {
		t.Fatalf("legacy passthrough: got %q err %v", got, err)
	}
}

func TestDecryptEncryptedWithoutKeyFails(t *testing.T) {
	var c *Cipher
	if _, err := c.Decrypt(prefix + "abc"); err == nil {
		t.Fatal("expected error decrypting ciphertext without a key")
	}
}

func TestFromString(t *testing.T) {
	if c, err := FromString(""); err != nil || c != nil {
		t.Fatalf("empty key should yield (nil,nil): c=%v err=%v", c, err)
	}
	hexKey := hex.EncodeToString(make([]byte, 32))
	if c, err := FromString(hexKey); err != nil || c == nil {
		t.Fatalf("64-hex key: c=%v err=%v", c, err)
	}
	if _, err := FromString("too-short"); err == nil {
		t.Fatal("expected error for short/invalid key")
	}
}
