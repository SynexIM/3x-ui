package service

import (
	"encoding/json"
	"testing"
)

func TestDeclarativeInboundCompilesSharedIdentityAndBandwidth(t *testing.T) {
	password := "line-secret"
	inbound, err := modelInboundFor(DeclarativeInbound{
		Tag:        "group-mixed",
		Protocol:   "mixed",
		ListenPort: 62789,
		ShareAddr: DeclarativeShareAddress{
			Strategy: "custom",
			Host:     "proxy.example.com",
			Port:     443,
		},
		Settings:       map[string]any{},
		StreamSettings: map[string]any{"network": "tcp"},
		Clients: []DeclarativeClient{{
			Email:     "line-001@line.ipvelo.invalid",
			UUID:      "11111111-1111-1111-1111-111111111111",
			Password:  &password,
			LimitMbps: 20,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	compiled := inbound.GenXrayInboundConfig()
	var settings struct {
		Accounts []struct {
			User         string `json:"user"`
			Pass         string `json:"pass"`
			BandwidthBps uint64 `json:"bandwidth_bps"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(compiled.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(settings.Accounts))
	}
	account := settings.Accounts[0]
	if account.User != "line-001@line.ipvelo.invalid" || account.Pass != password {
		t.Fatalf("compiled account = %#v", account)
	}
	if account.BandwidthBps != 20_000_000 {
		t.Fatalf("bandwidth_bps = %d, want 20000000", account.BandwidthBps)
	}
	if inbound.ShareAddrStrategy != "custom" || inbound.ShareAddr != "proxy.example.com" {
		t.Fatalf("share address = %s/%s", inbound.ShareAddrStrategy, inbound.ShareAddr)
	}
}

func TestDeclarativeRequestRejectsDanglingRouteAndRevisionConflictInputs(t *testing.T) {
	request := &DeclarativeApplyRequest{Revision: 1}
	request.Config.Routing.Rules = []DeclarativeRule{{
		AccountEmail: "line-001@line.ipvelo.invalid",
		OutboundTag:  "missing",
	}}
	if err := validateDeclarativeRequest(request); err == nil {
		t.Fatal("dangling route should be rejected")
	}

	request.Revision = 0
	request.Config.Routing.Rules = nil
	if err := validateDeclarativeRequest(request); err == nil {
		t.Fatal("non-positive revision should be rejected")
	}
}
