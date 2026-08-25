package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSelfSignedCert puts a usable certificate/key pair on disk. xray-core's
// TLS config builder reads the files while building the inbound, so a template
// carrying a path to nothing would be skipped by the missing-file tolerance in
// checkTemplateInbounds instead of actually being validated.
func writeSelfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "entry.line.invalid"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"entry.line.invalid"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "entry.pem")
	keyPath = filepath.Join(dir, "entry.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// fiveProtocolConfig is the shape a line template has: one account holding a
// client in every inbound, one egress, one routing rule.
func fiveProtocolConfig(t *testing.T) DeclarativeNodeConfig {
	t.Helper()
	certPath, keyPath := writeSelfSignedCert(t)
	// SS2022 keys are base64 of 16 bytes (aes-128-gcm), per NFR-10 the line
	// never uses the legacy AEAD ciphers.
	inboundKey := "MTIzNDU2Nzg5MGFiY2RlZg=="
	clientKey := "YWJjZGVmMTIzNDU2Nzg5MA=="
	password := "line-shared-secret"
	client := func(extra *string) DeclarativeClient {
		return DeclarativeClient{
			Email:    "line-001@line.invalid",
			UUID:     "11111111-1111-1111-1111-111111111111",
			Password: extra,
			PirBps:   100_000_000,
			CirBps:   20_000_000,
			CbsBytes: 50_000_000,
		}
	}
	share := func(port int) DeclarativeShareAddress {
		return DeclarativeShareAddress{Strategy: "custom", Host: "entry.line.invalid", Port: port}
	}
	config := DeclarativeNodeConfig{
		NodeBandwidthBps: 480_000_000,
		Inbounds: []DeclarativeInbound{
			{
				Tag: "line-vless", Protocol: "vless", ListenPort: 30800, ShareAddr: share(30800),
				Settings:       map[string]any{},
				StreamSettings: map[string]any{"network": "tcp"},
				Clients:        []DeclarativeClient{client(nil)},
			},
			{
				Tag: "line-vmess", Protocol: "vmess", ListenPort: 30801, ShareAddr: share(30801),
				Settings:       map[string]any{},
				StreamSettings: map[string]any{"network": "tcp"},
				Clients:        []DeclarativeClient{client(nil)},
			},
			{
				Tag: "line-trojan", Protocol: "trojan", ListenPort: 30802, ShareAddr: share(30802),
				Settings:       map[string]any{},
				StreamSettings: map[string]any{"network": "tcp"},
				Clients:        []DeclarativeClient{client(&password)},
			},
			{
				Tag: "line-ss", Protocol: "shadowsocks", ListenPort: 30803, ShareAddr: share(30803),
				Settings:       map[string]any{"method": "2022-blake3-aes-128-gcm", "password": inboundKey},
				StreamSettings: map[string]any{"network": "tcp"},
				Clients:        []DeclarativeClient{client(&clientKey)},
			},
			{
				Tag: "line-hysteria", Protocol: "hysteria", ListenPort: 30804, ShareAddr: share(30804),
				Settings: map[string]any{},
				StreamSettings: map[string]any{
					"security": "tls",
					"tlsSettings": map[string]any{
						"serverName":   "entry.line.invalid",
						"certificates": []any{map[string]any{"certificateFile": certPath, "keyFile": keyPath}},
					},
				},
				Clients: []DeclarativeClient{client(&password)},
			},
		},
		Outbounds: []DeclarativeOutbound{{
			Tag: "egress-hk-1", Protocol: "socks",
			Server: DeclarativeSocksServer{Host: "10.0.0.1", Port: 1080},
		}},
	}
	config.Routing.Rules = []DeclarativeRule{{
		AccountEmail: "line-001@line.invalid", OutboundTag: "egress-hk-1",
	}}
	return config
}

// The whole point of the whitelist entry: a template carrying all five
// protocols of a line has to be accepted, and the compiled result has to be
// something xray-core's own loader builds. Anything less and the template is
// 422 in one piece, which is what kept the caller from provisioning at
// all.
func TestFiveProtocolTemplateWithHysteriaIsAccepted(t *testing.T) {
	config := fiveProtocolConfig(t)

	if err := validateDeclarativeRequest(&DeclarativeApplyRequest{Revision: 1, Config: config}); err != nil {
		t.Fatalf("a five-protocol line template must validate: %v", err)
	}

	template, err := (&DeclarativeProvisioningService{}).buildTemplate(config)
	if err != nil {
		t.Fatalf("build template: %v", err)
	}
	// Inbounds only: the default template's freedom outbound blocks
	// geoip:private, and geoip.dat ships next to the xray binary rather than
	// with the source, so the outbound half of CheckXrayConfig cannot run here.
	// checkTemplateInbounds is the half this feature touches.
	if err := checkTemplateInbounds(template); err != nil {
		t.Fatalf("xray-core must build every inbound of the line: %v", err)
	}
}

// Hysteria2 identifies an account by its auth token, not by a UUID, and the
// transport half has its own version and settings block. Getting any of these
// wrong is only visible when the core starts — or, for a missing
// hysteriaSettings, as a panic inside the listener.
func TestHysteriaInboundCompilesToTheShapeXrayReads(t *testing.T) {
	config := fiveProtocolConfig(t)
	inbound, err := modelInboundFor(config.Inbounds[4])
	if err != nil {
		t.Fatal(err)
	}
	compiled := inbound.GenXrayInboundConfig()

	var settings struct {
		Version int `json:"version"`
		Clients []struct {
			Email               string `json:"email"`
			Auth                string `json:"auth"`
			BandwidthBps        uint64 `json:"bandwidth_bps"`
			CommittedBps        uint64 `json:"committed_bps"`
			CommittedBurstBytes uint64 `json:"committed_burst_bytes"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(compiled.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Version != 2 {
		t.Fatalf("settings.version = %d, xray-core only builds 2", settings.Version)
	}
	if len(settings.Clients) != 1 {
		t.Fatalf("clients = %d, want 1", len(settings.Clients))
	}
	got := settings.Clients[0]
	if got.Auth != "line-shared-secret" {
		t.Fatalf("auth = %q; without it the account cannot connect at all", got.Auth)
	}
	if got.Email != "line-001@line.invalid" ||
		got.BandwidthBps != 100_000_000 || got.CommittedBps != 20_000_000 || got.CommittedBurstBytes != 50_000_000 {
		t.Fatalf("per-client limits did not survive the compile: %#v", got)
	}

	var stream struct {
		Network          string `json:"network"`
		Security         string `json:"security"`
		HysteriaSettings *struct {
			Version int `json:"version"`
		} `json:"hysteriaSettings"`
	}
	if err := json.Unmarshal(compiled.StreamSettings, &stream); err != nil {
		t.Fatal(err)
	}
	if stream.Network != "hysteria" {
		t.Fatalf("network = %q; on any other transport this is not a hysteria listener", stream.Network)
	}
	if stream.Security != "tls" {
		t.Fatalf("security = %q, want tls", stream.Security)
	}
	if stream.HysteriaSettings == nil || stream.HysteriaSettings.Version != 2 {
		t.Fatalf("hysteriaSettings = %#v; Listen type-asserts it and panics when absent", stream.HysteriaSettings)
	}
}

// The refusals are the ones the core cannot make for us: it builds both of
// these happily and then fails to listen.
func TestHysteriaInboundRequiresTLSAndAnAuthToken(t *testing.T) {
	t.Run("without tls", func(t *testing.T) {
		config := fiveProtocolConfig(t)
		delete(config.Inbounds[4].StreamSettings, "security")
		err := validateDeclarativeRequest(&DeclarativeApplyRequest{Revision: 1, Config: config})
		if err == nil || !strings.Contains(err.Error(), "tls") {
			t.Fatalf("hysteria without tls must be refused; got %v", err)
		}
	})

	t.Run("without a password", func(t *testing.T) {
		config := fiveProtocolConfig(t)
		config.Inbounds[4].Clients[0].Password = nil
		if _, err := modelInboundFor(config.Inbounds[4]); err == nil {
			t.Fatal("a hysteria client with no password has no auth token and could never connect")
		}
	})
}
