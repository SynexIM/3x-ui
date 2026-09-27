package model

import (
	"encoding/base64"
	"testing"
)

// The derivation is a cross-system contract (FORK.md): IPLine sends one raw
// password and every link and core user must agree on the per-inbound key.
func TestShadowsocksClientKey(t *testing.T) {
	aes128 := ShadowsocksClientKey("2022-blake3-aes-128-gcm", "in-ss", "RawPass_0001")
	if raw, err := base64.StdEncoding.DecodeString(aes128); err != nil || len(raw) != 16 {
		t.Fatalf("aes-128 key = %q, want 16 bytes base64", aes128)
	}
	if other := ShadowsocksClientKey("2022-blake3-aes-128-gcm", "in-ss-2", "RawPass_0001"); other == aes128 {
		t.Fatal("two inbounds derived the same key from one password")
	}
	if raw, _ := base64.StdEncoding.DecodeString(ShadowsocksClientKey("2022-blake3-chacha20-poly1305", "in-ss", "x")); len(raw) != 32 {
		t.Fatal("chacha20 key must be 32 bytes")
	}
	valid := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if got := ShadowsocksClientKey("2022-blake3-aes-128-gcm", "in-ss", valid); got != valid {
		t.Fatalf("an already valid key must pass through, got %q", got)
	}
	if got := ShadowsocksClientKey("aes-256-gcm", "in-ss", "RawPass_0001"); got != "RawPass_0001" {
		t.Fatalf("legacy shadowsocks must keep the raw password, got %q", got)
	}
}
