package service

import (
	"strings"
	"testing"
)

// inboundTemplate wraps one inbound in the smallest template CheckXrayConfig
// accepts, so a failure can only come from the inbound itself.
func inboundTemplate(inbound string) string {
	return `{"inbounds":[` + inbound + `],"outbounds":[{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[]}}`
}

// The panel used to validate outbounds and routing through xray-core's own
// loader but never inbounds, so a config the core would refuse was saved
// happily and only failed at startup — after the previous config was gone.
func TestCheckXrayConfigRejectsInboundsXrayRefuses(t *testing.T) {
	cases := []struct {
		name    string
		inbound string
	}{
		{
			// xray requires decryption on every VLESS inbound.
			name:    "vless without decryption",
			inbound: `{"tag":"in-vless","listen":"127.0.0.1","port":30101,"protocol":"vless","settings":{"clients":[{"id":"8f1f0d0e-2e3a-4f6b-9c2d-1a2b3c4d5e6f"}]}}`,
		},
		{
			name:    "shadowsocks with an unsupported cipher",
			inbound: `{"tag":"in-ss","listen":"127.0.0.1","port":30103,"protocol":"shadowsocks","settings":{"method":"rot13-please","password":"hunter2"}}`,
		},
		{
			name:    "protocol xray has never heard of",
			inbound: `{"tag":"in-huh","listen":"127.0.0.1","port":30104,"protocol":"telepathy","settings":{}}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := (&XraySettingService{}).CheckXrayConfig(inboundTemplate(c.inbound))
			if err == nil {
				t.Fatal("an inbound xray-core refuses must not pass validation")
			}
			// The control plane acts on this text, so it has to name the inbound.
			if !strings.Contains(err.Error(), "in-") {
				t.Fatalf("error should name the rejected inbound, got %q", err.Error())
			}
		})
	}
}

// The validation must not become a second, stricter gate that refuses configs
// the core would happily run.
func TestCheckXrayConfigAcceptsInboundsXrayBuilds(t *testing.T) {
	cases := []struct {
		name    string
		inbound string
	}{
		{
			name:    "vless with decryption",
			inbound: `{"tag":"ok-vless","listen":"127.0.0.1","port":30201,"protocol":"vless","settings":{"clients":[{"id":"8f1f0d0e-2e3a-4f6b-9c2d-1a2b3c4d5e6f"}],"decryption":"none"}}`,
		},
		{
			name:    "shadowsocks 2022",
			inbound: `{"tag":"ok-ss","listen":"127.0.0.1","port":30202,"protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"aGVsbG93b3JsZGhlbGxvd28=","clients":[]}}`,
		},
		{
			name:    "the internal api inbound the panel seeds itself",
			inbound: `{"tag":"api","listen":"127.0.0.1","port":62789,"protocol":"tunnel","settings":{"rewriteAddress":"127.0.0.1"}}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := (&XraySettingService{}).CheckXrayConfig(inboundTemplate(c.inbound)); err != nil {
				t.Fatalf("xray-core builds this inbound, validation must accept it: %v", err)
			}
		})
	}
}

// mtproto is served by the bundled mtg-multi sidecar, not by xray, so
// xray-core has no config id for it and must not be asked to judge it.
func TestCheckXrayConfigSkipsMtproto(t *testing.T) {
	inbound := `{"tag":"in-mtproto","listen":"127.0.0.1","port":30301,"protocol":"mtproto","settings":{"secret":"ee00112233445566778899aabbccddeeff"}}`
	if err := (&XraySettingService{}).CheckXrayConfig(inboundTemplate(inbound)); err != nil {
		t.Fatalf("mtproto is not an xray protocol and must be skipped: %v", err)
	}
}

// A certificate that has not been issued yet says nothing about whether the
// inbound is well formed, and a genuinely wrong path is still caught when the
// core refuses to start.
func TestCheckXrayConfigToleratesAnAbsentCertificate(t *testing.T) {
	inbound := `{"tag":"in-tls","listen":"127.0.0.1","port":30401,"protocol":"vless",
		"settings":{"clients":[],"decryption":"none"},
		"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"certificates":[
			{"certificateFile":"/does/not/exist/fullchain.pem","keyFile":"/does/not/exist/privkey.pem"}]}}}`
	if err := (&XraySettingService{}).CheckXrayConfig(inboundTemplate(inbound)); err != nil {
		t.Fatalf("an inbound waiting on its certificate must not be rejected: %v", err)
	}
}

// The default template the panel ships must survive its own validation,
// otherwise a fresh install cannot save any setting at all.
func TestCheckXrayConfigAcceptsTheShippedDefaultTemplate(t *testing.T) {
	err := (&XraySettingService{}).CheckXrayConfig(xrayTemplateConfig)
	if err == nil {
		return
	}
	// The default outbounds route through geoip:private, and outbound
	// validation (unlike the inbound side) fails outright when the dat files
	// are not next to the binary — which is the normal state of a dev checkout.
	if mentionsMissingGeoAsset(err) {
		t.Skipf("geo data files not available, cannot judge the default template: %v", err)
	}
	t.Fatalf("the shipped default template must validate: %v", err)
}
