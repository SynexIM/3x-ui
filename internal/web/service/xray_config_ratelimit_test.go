package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const (
	testPIR       = uint64(100_000_000) // 100 Mbps
	testCIR       = uint64(20_000_000)  // 20 Mbps
	testCBS       = uint64(50_000_000)  // 50 MB
	testConnLimit = uint32(4)
)

func limitedClient(email, id string) model.Client {
	return model.Client{
		Email: email, ID: id, Password: id, Auth: id, Enable: true,
		BandwidthBps: testPIR, CommittedBps: testCIR, CommittedBurstBytes: testCBS,
		ConnLimit: testConnLimit,
		RateUnit:  "Mbps", BurstUnit: "MB",
	}
}

func seedInbound(t *testing.T, protocol model.Protocol, tag string, port int, settings string, clients []model.Client) {
	t.Helper()
	db := database.GetDB()
	in := &model.Inbound{Tag: tag, Enable: true, Port: port, Protocol: protocol, Settings: settings}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("create %s inbound: %v", protocol, err)
	}
	if err := (&ClientService{}).SyncInbound(nil, in.Id, clients); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}
}

func emittedSettings(t *testing.T, tag string) map[string]any {
	t.Helper()
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag != tag {
			continue
		}
		out := (&model.Inbound{
			Protocol:       model.Protocol(cfg.InboundConfigs[i].Protocol),
			Settings:       string(cfg.InboundConfigs[i].Settings),
			StreamSettings: "{}",
		}).GenXrayInboundConfig()
		var s map[string]any
		if err := json.Unmarshal([]byte(out.Settings), &s); err != nil {
			t.Fatalf("unmarshal emitted settings: %v", err)
		}
		return s
	}
	t.Fatalf("inbound %q not found in generated config", tag)
	return nil
}

func assertLimits(t *testing.T, where string, obj map[string]any) {
	t.Helper()
	want := map[string]float64{
		"bandwidth_bps":         float64(testPIR),
		"committed_bps":         float64(testCIR),
		"committed_burst_bytes": float64(testCBS),
		"conn_limit":            float64(testConnLimit),
	}
	for key, expect := range want {
		got, ok := obj[key]
		if !ok {
			t.Errorf("%s: %s missing from the generated config — the panel shows a limit that xray never sees", where, key)
			continue
		}
		if got != expect {
			t.Errorf("%s: %s = %v, want %v", where, key, got, expect)
		}
	}
	for _, key := range []string{"rateUnit", "burstUnit"} {
		if _, leaked := obj[key]; leaked {
			t.Errorf("%s: display-only %q leaked into the xray config", where, key)
		}
	}
}

// Every multi-client protocol must carry the three limit keys into config.json.
// A protocol missing here means an operator sets a limit that silently does nothing.
func TestGeneratedConfigCarriesRateLimitsForEveryProtocol(t *testing.T) {
	setupSettingTestDB(t)

	cases := []struct {
		name     string
		protocol model.Protocol
		port     int
		settings string
		// key holding the user objects in the emitted settings
		usersKey string
	}{
		{"vless", model.VLESS, 44101, `{"clients":[],"decryption":"none"}`, "clients"},
		{"vmess", model.VMESS, 44102, `{"clients":[]}`, "clients"},
		{"trojan", model.Trojan, 44103, `{"clients":[]}`, "clients"},
		{"shadowsocks", model.Shadowsocks, 44104, `{"clients":[],"method":"2022-blake3-aes-128-gcm","password":"IdG0eY+zbGDpTEBGKcCSXpuMXNiPUFcbZTHDWbBGb5w="}`, "clients"},
		{"hysteria", model.Hysteria, 44105, `{"clients":[],"version":2}`, "clients"},
		{"mixed", model.Mixed, 44106, `{"clients":[]}`, "accounts"},
		{"http", model.HTTP, 44107, `{"clients":[],"allowTransparent":false}`, "accounts"},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tag := "limits-" + c.name
			id := "0000000" + string(rune('0'+i)) + "-0000-0000-0000-000000000000"
			seedInbound(t, c.protocol, tag, c.port, c.settings, []model.Client{limitedClient("lim-"+c.name+"@x", id)})

			settings := emittedSettings(t, tag)
			users, ok := settings[c.usersKey].([]any)
			if !ok || len(users) != 1 {
				t.Fatalf("%s: expected one entry under %q, got %#v", c.name, c.usersKey, settings[c.usersKey])
			}
			obj, ok := users[0].(map[string]any)
			if !ok {
				t.Fatalf("%s: user entry must be an object, got %T", c.name, users[0])
			}
			assertLimits(t, c.name, obj)
		})
	}
}

// Blank means unlimited: a client with no limits must not gain zero-valued keys,
// which xray would read as "limited to 0" if the semantics ever changed.
func TestGeneratedConfigOmitsAbsentRateLimits(t *testing.T) {
	setupSettingTestDB(t)
	seedInbound(t, model.VLESS, "nolimits", 44201, `{"clients":[],"decryption":"none"}`,
		[]model.Client{{Email: "free@x", ID: "99999999-9999-9999-9999-999999999999", Enable: true}})

	settings := emittedSettings(t, "nolimits")
	users := settings["clients"].([]any)
	obj := users[0].(map[string]any)
	for _, key := range model.ClientRateLimitKeys {
		if _, present := obj[key]; present {
			t.Errorf("unlimited client carries %q into the config: %#v", key, obj)
		}
	}
}

// The limits must survive the DB round-trip, or the form saves and the next
// reload shows blank.
func TestRateLimitsRoundTripThroughDatabase(t *testing.T) {
	setupSettingTestDB(t)
	seedInbound(t, model.VLESS, "roundtrip", 44301, `{"clients":[],"decryption":"none"}`,
		[]model.Client{limitedClient("rt@x", "88888888-8888-8888-8888-888888888888")})

	clients, err := (&ClientService{}).ListForInbound(nil, 1)
	if err != nil {
		t.Fatalf("ListForInbound: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("expected one client, got %d", len(clients))
	}
	c := clients[0]
	if c.BandwidthBps != testPIR || c.CommittedBps != testCIR || c.CommittedBurstBytes != testCBS || c.ConnLimit != testConnLimit {
		t.Errorf("limits lost in the DB round-trip: %d/%d/%d/%d", c.BandwidthBps, c.CommittedBps, c.CommittedBurstBytes, c.ConnLimit)
	}
	if c.RateUnit != "Mbps" || c.BurstUnit != "MB" {
		t.Errorf("display units lost: rate=%q burst=%q — the form would reopen with a different number", c.RateUnit, c.BurstUnit)
	}
}

// Clearing a limit must clear it. Merge rules that only overwrite non-zero
// values would keep the old cap forever.
func TestClearingRateLimitsPersists(t *testing.T) {
	setupSettingTestDB(t)
	seedInbound(t, model.VLESS, "clearable", 44401, `{"clients":[],"decryption":"none"}`,
		[]model.Client{limitedClient("clear@x", "77777777-7777-7777-7777-777777777777")})

	cleared := limitedClient("clear@x", "77777777-7777-7777-7777-777777777777")
	cleared.BandwidthBps, cleared.CommittedBps, cleared.CommittedBurstBytes = 0, 0, 0
	cleared.ConnLimit = 0
	if err := (&ClientService{}).SyncInbound(nil, 1, []model.Client{cleared}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}

	clients, err := (&ClientService{}).ListForInbound(nil, 1)
	if err != nil {
		t.Fatalf("ListForInbound: %v", err)
	}
	if got := clients[0].BandwidthBps; got != 0 {
		t.Errorf("bandwidth_bps still %d after clearing — the operator cannot un-limit a line", got)
	}
	if got := clients[0].CommittedBps; got != 0 {
		t.Errorf("committed_bps still %d after clearing", got)
	}
	if got := clients[0].ConnLimit; got != 0 {
		t.Errorf("conn_limit still %d after clearing", got)
	}
}
