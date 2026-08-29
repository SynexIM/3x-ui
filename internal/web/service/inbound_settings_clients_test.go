package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestParseInboundDraftClientsIgnoresProtocolScalarFields(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		want     string
	}{
		{
			name: "vless scalar settings",
			settings: `{
				"clients": [{"email": "alice@example.test", "id": "11111111-1111-1111-1111-111111111111", "limitIp": 2}],
				"decryption": "none",
				"encryption": "none",
				"fallbacks": []
			}`,
			want: "alice@example.test",
		},
		{
			name: "hysteria scalar settings",
			settings: `{
				"clients": [{"email": "bob@example.test", "password": "secret"}],
				"version": 2,
				"ignoreClientBandwidth": false
			}`,
			want: "bob@example.test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients, err := ParseInboundDraftClients(tt.settings)
			if err != nil {
				t.Fatalf("ParseInboundDraftClients: %v", err)
			}
			if len(clients) != 1 || clients[0].Email != tt.want {
				t.Fatalf("clients = %+v, want one client with email %s", clients, tt.want)
			}
		})
	}
}

func TestParseInboundDraftClientsRejectsEmptyOrNullSettings(t *testing.T) {
	for _, settings := range []string{"", "   ", "null", " \n null \t "} {
		t.Run(settings, func(t *testing.T) {
			clients, err := ParseInboundDraftClients(settings)
			if err == nil {
				t.Fatalf("ParseInboundDraftClients(%q) error = nil, want error", settings)
			}
			if clients != nil {
				t.Fatalf("clients = %+v, want nil", clients)
			}
		})
	}
}

func TestParseAndStripInboundDraftClientsKeepsOnlyProtocolSettings(t *testing.T) {
	clients, persisted, err := ParseAndStripInboundDraftClients(`{
		"clients": [{"email": "alice@example.test", "id": "11111111-1111-1111-1111-111111111111"}],
		"peers": [{"publicKey": "legacy-peer"}],
		"decryption": "none"
	}`)
	if err != nil {
		t.Fatalf("ParseAndStripInboundDraftClients: %v", err)
	}
	if len(clients) != 1 || clients[0].Email != "alice@example.test" {
		t.Fatalf("draft clients = %+v", clients)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(persisted), &settings); err != nil {
		t.Fatalf("decode persisted settings: %v", err)
	}
	if _, exists := settings["clients"]; exists {
		t.Fatalf("persisted settings retained clients: %s", persisted)
	}
	if _, exists := settings["peers"]; exists {
		t.Fatalf("persisted settings retained peers: %s", persisted)
	}
	if settings["decryption"] != "none" {
		t.Fatalf("protocol settings = %#v", settings)
	}
}

func TestInjectNormalizedClientsBuildsRuntimeClone(t *testing.T) {
	settings := map[string]any{"decryption": "none"}
	if err := injectNormalizedClients(settings, model.VLESS, []model.Client{{Email: "live@example.test", ID: "11111111-1111-1111-1111-111111111111", Enable: true}}); err != nil {
		t.Fatal(err)
	}
	clients, ok := settings["clients"].([]any)
	if !ok || len(clients) != 1 {
		t.Fatalf("runtime clients = %#v", settings["clients"])
	}
	if _, persisted := settings["peers"]; persisted {
		t.Fatalf("unexpected peers in vless clone: %#v", settings)
	}
}

func TestGetClientsIgnoresProtocolScalarFields(t *testing.T) {
	inbound := &model.Inbound{
		Settings: `{
			"clients": [{"email": "alice@example.test", "id": "11111111-1111-1111-1111-111111111111"}],
			"decryption": "none",
			"encryption": "none",
			"fallbacks": []
		}`,
	}

	clients, err := ParseInboundDraftClients(inbound.Settings)
	if err != nil {
		t.Fatalf("ParseInboundDraftClients: %v", err)
	}
	if len(clients) != 1 || clients[0].Email != "alice@example.test" {
		t.Fatalf("clients = %+v, want alice@example.test", clients)
	}
}

func TestSearchClientTrafficIgnoresProtocolScalarFields(t *testing.T) {
	setupBulkDB(t)
	db := database.GetDB()

	client := model.Client{
		Email:  "alice@example.test",
		ID:     "11111111-1111-1111-1111-111111111111",
		SubID:  "sub-alice",
		Enable: true,
	}
	inbound := &model.Inbound{
		UserId:   1,
		Tag:      "vless-scalar",
		Enable:   true,
		Port:     43001,
		Protocol: model.VLESS,
		Settings: `{
			"clients": [{"email": "alice@example.test", "id": "11111111-1111-1111-1111-111111111111", "subId": "sub-alice", "enable": true}],
			"decryption": "none",
			"encryption": "none",
			"fallbacks": []
		}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	seedNormalizedInbound(t, inbound, []model.Client{client})

	traffic, err := (&InboundService{}).SearchClientTraffic(client.ID)
	if err != nil {
		t.Fatalf("SearchClientTraffic: %v", err)
	}
	if traffic.Email != client.Email || traffic.InboundId != inbound.Id {
		t.Fatalf("traffic = %+v, want email %s inbound %d", traffic, client.Email, inbound.Id)
	}
}
