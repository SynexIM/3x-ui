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
			PirBps:    100_000_000,
			CirBps:    20_000_000,
			CbsBytes:  50_000_000,
			ConnLimit: 4,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	compiled := inbound.GenXrayInboundConfig()
	var settings struct {
		Accounts []struct {
			User                string `json:"user"`
			Pass                string `json:"pass"`
			BandwidthBps        uint64 `json:"bandwidth_bps"`
			CommittedBps        uint64 `json:"committed_bps"`
			CommittedBurstBytes uint64 `json:"committed_burst_bytes"`
			ConnLimit           uint32 `json:"conn_limit"`
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
	if account.BandwidthBps != 100_000_000 ||
		account.CommittedBps != 20_000_000 ||
		account.CommittedBurstBytes != 50_000_000 ||
		account.ConnLimit != 4 {
		t.Fatalf("compiled limits = %#v", account)
	}
	if inbound.ShareAddrStrategy != "custom" || inbound.ShareAddr != "proxy.example.com" {
		t.Fatalf("share address = %s/%s", inbound.ShareAddrStrategy, inbound.ShareAddr)
	}
	deliveryInbound, err := deliveryInboundFor(DeclarativeInbound{
		Tag:            "group-mixed",
		Protocol:       "mixed",
		ListenPort:     62789,
		ShareAddr:      DeclarativeShareAddress{Strategy: "custom", Host: "proxy.example.com", Port: 443},
		Settings:       map[string]any{},
		StreamSettings: map[string]any{},
		Clients: []DeclarativeClient{{
			Email:    "line-001@line.ipvelo.invalid",
			UUID:     "11111111-1111-1111-1111-111111111111",
			Password: &password,
			PirBps:   100_000_000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if deliveryInbound.Port != 443 {
		t.Fatalf("delivery port = %d, want 443", deliveryInbound.Port)
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

func TestDeclarativeRequestAllowsUnlimitedRateTemplate(t *testing.T) {
	request := &DeclarativeApplyRequest{Revision: 1}
	request.Config.Inbounds = []DeclarativeInbound{{
		Tag:        "entry",
		Protocol:   "vless",
		ListenPort: 443,
		ShareAddr:  DeclarativeShareAddress{Strategy: "custom", Host: "edge.example.com", Port: 443},
		Clients: []DeclarativeClient{{
			Email: "line@example.invalid",
			UUID:  "11111111-1111-1111-1111-111111111111",
		}},
	}}
	if err := validateDeclarativeRequest(request); err != nil {
		t.Fatalf("zero PIR/CIR/CBS must mean unlimited: %v", err)
	}

	request.Config.Inbounds[0].Clients[0].CirBps = 1
	if err := validateDeclarativeRequest(request); err == nil {
		t.Fatal("CIR without PIR must be rejected")
	}
}

func TestDeclarativeRevisionIsAContentIdentityNotASequence(t *testing.T) {
	olderNumericValue := &DeclarativeApplyRequest{Revision: 10}
	newerNumericValue := &DeclarativeApplyRequest{Revision: 20}
	if err := validateDeclarativeRequest(olderNumericValue); err != nil {
		t.Fatalf("lower content revision must remain valid: %v", err)
	}
	if err := validateDeclarativeRequest(newerNumericValue); err != nil {
		t.Fatalf("higher content revision must remain valid: %v", err)
	}
}

func TestDeclarativeTemplateKeepsPanelControlPath(t *testing.T) {
	template, err := (&DeclarativeProvisioningService{}).buildTemplate(DeclarativeNodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal([]byte(template), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) == 0 || config.Inbounds[0].Tag != "api" {
		t.Fatalf("panel API inbound was removed: %#v", config.Inbounds)
	}
	if len(config.Routing.Rules) == 0 || !isApiRule(config.Routing.Rules[0]) {
		t.Fatalf("panel API routing rule was removed: %#v", config.Routing.Rules)
	}
}

// vision 只在 raw/tcp 上合法。面板以前无条件给所有 vless 客户端写上它，
// 于是 ws/grpc 上的线路配置看起来是绿的、xray 也起得来，客户却永远握手不过。
func TestDeclarativeVlessFlowComesFromTheSender(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flow    *string
		network string
		want    string
	}{
		{name: "ws 上下发端说不要 flow", flow: strPtr(""), network: "ws", want: ""},
		{name: "tcp 上下发端要 vision", flow: strPtr("xtls-rprx-vision"), network: "tcp", want: "xtls-rprx-vision"},
		{name: "旧下发端不发 flow 时保持原行为", flow: nil, network: "tcp", want: "xtls-rprx-vision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inbound, err := modelInboundFor(DeclarativeInbound{
				Tag:            "g-vless",
				Protocol:       "vless",
				ListenPort:     31006,
				ShareAddr:      DeclarativeShareAddress{Strategy: "custom", Host: "h", Port: 31006},
				Settings:       map[string]any{},
				StreamSettings: map[string]any{"network": tc.network},
				Clients: []DeclarativeClient{{
					Email: "l1@x.invalid",
					UUID:  "11111111-1111-1111-1111-111111111111",
					Flow:  tc.flow,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			var settings struct {
				Clients []struct {
					Flow string `json:"flow"`
				} `json:"clients"`
			}
			if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
				t.Fatal(err)
			}
			if len(settings.Clients) != 1 {
				t.Fatalf("clients = %d, want 1", len(settings.Clients))
			}
			if settings.Clients[0].Flow != tc.want {
				t.Fatalf("flow = %q, want %q", settings.Clients[0].Flow, tc.want)
			}
		})
	}
}

func strPtr(value string) *string { return &value }
