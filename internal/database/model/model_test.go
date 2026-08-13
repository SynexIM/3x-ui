package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInboundMarshalJSONNestsObjectFields(t *testing.T) {
	in := Inbound{
		Id:             7,
		Protocol:       VLESS,
		Port:           443,
		Settings:       `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp"}`,
		Sniffing:       `{"enabled":true}`,
	}
	out, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, field := range []string{"settings", "streamSettings", "sniffing"} {
		if _, ok := parsed[field].(map[string]any); !ok {
			t.Errorf("expected %s to marshal as a JSON object, got %T", field, parsed[field])
		}
	}
	if strings.Contains(string(out), `"settings":"`) {
		t.Errorf("settings should not be emitted as a JSON string: %s", out)
	}
}

func TestInboundMarshalJSONEmptyFieldsBecomeNull(t *testing.T) {
	in := Inbound{Id: 1, Protocol: VLESS}
	out, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, field := range []string{"settings", "streamSettings", "sniffing"} {
		if parsed[field] != nil {
			t.Errorf("expected %s to be null, got %v", field, parsed[field])
		}
	}
}

func TestInboundUnmarshalJSONAcceptsBothShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "nested objects (modern)",
			body: `{"id":1,"settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"tcp"},"sniffing":{"enabled":true}}`,
		},
		{
			name: "JSON-encoded strings (legacy)",
			body: `{"id":1,"settings":"{\"clients\":[],\"decryption\":\"none\"}","streamSettings":"{\"network\":\"tcp\"}","sniffing":"{\"enabled\":true}"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var in Inbound
			if err := json.Unmarshal([]byte(tc.body), &in); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if !strings.Contains(in.Settings, `"decryption":"none"`) {
				t.Errorf("Settings not normalised: %q", in.Settings)
			}
			if !strings.Contains(in.StreamSettings, `"network":"tcp"`) {
				t.Errorf("StreamSettings not normalised: %q", in.StreamSettings)
			}
			if !strings.Contains(in.Sniffing, `"enabled":true`) {
				t.Errorf("Sniffing not normalised: %q", in.Sniffing)
			}
		})
	}
}

func TestInboundMarshalJSONInvalidTextFallsBackToString(t *testing.T) {
	in := Inbound{Id: 1, Settings: "not json at all"}
	out, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if !strings.Contains(string(out), `"settings":"not json at all"`) {
		t.Errorf("expected invalid settings text to be wrapped as a JSON string, got %s", out)
	}
}

func TestClientRecordMarshalJSONNestsReverse(t *testing.T) {
	rec := ClientRecord{Id: 1, Email: "alice@example.com", Reverse: `{"tag":"vless-in"}`}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	obj, ok := parsed["reverse"].(map[string]any)
	if !ok {
		t.Fatalf("expected reverse to marshal as a JSON object, got %T", parsed["reverse"])
	}
	if obj["tag"] != "vless-in" {
		t.Errorf("expected tag to be preserved, got %v", obj["tag"])
	}
}

func TestClientRecordMarshalJSONEmptyReverseIsNull(t *testing.T) {
	rec := ClientRecord{Id: 1, Email: "alice@example.com"}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed["reverse"] != nil {
		t.Errorf("expected reverse to be null, got %v", parsed["reverse"])
	}
}

func TestClientRecordUnmarshalJSONAcceptsBothShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "nested object", body: `{"id":1,"reverse":{"tag":"vless-in"}}`},
		{name: "legacy string", body: `{"id":1,"reverse":"{\"tag\":\"vless-in\"}"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec ClientRecord
			if err := json.Unmarshal([]byte(tc.body), &rec); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if !strings.Contains(rec.Reverse, `"tag":"vless-in"`) {
				t.Errorf("Reverse not normalised: %q", rec.Reverse)
			}
		})
	}
}

func TestInboundClientIpsMarshalJSONNestsArray(t *testing.T) {
	row := InboundClientIps{Id: 1, ClientEmail: "alice@example.com", Ips: `[{"ip":"1.2.3.4","timestamp":1700000000}]`}
	out, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	arr, ok := parsed["ips"].([]any)
	if !ok {
		t.Fatalf("expected ips to marshal as a JSON array, got %T", parsed["ips"])
	}
	if len(arr) != 1 {
		t.Errorf("expected 1 entry, got %d", len(arr))
	}
}

func TestInboundClientIpsUnmarshalJSONAcceptsBothShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "nested array", body: `{"id":1,"ips":[{"ip":"1.2.3.4","timestamp":1}]}`},
		{name: "legacy string", body: `{"id":1,"ips":"[{\"ip\":\"1.2.3.4\",\"timestamp\":1}]"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var row InboundClientIps
			if err := json.Unmarshal([]byte(tc.body), &row); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if !strings.Contains(row.Ips, `"ip":"1.2.3.4"`) {
				t.Errorf("Ips not normalised: %q", row.Ips)
			}
		})
	}
}

func TestStripInboundXhttpClientFields_RemovesClientOnlyKnobs(t *testing.T) {
	stream := `{
		"network": "xhttp",
		"security": "reality",
		"xhttpSettings": {
			"path": "/app",
			"host": "example.com",
			"mode": "stream-one",
			"xmux": { "maxConcurrency": "16-32" },
			"downloadSettings": { "network": "xhttp" },
			"scMinPostsIntervalMs": "20-40",
			"uplinkChunkSize": 4096,
			"noGRPCHeader": true
		}
	}`
	out, changed := StripInboundXhttpClientFields(stream)
	if !changed {
		t.Fatal("expected client-only xhttp fields to be stripped")
	}
	if strings.Contains(out, `"xmux"`) {
		t.Fatalf("xmux should be removed from xray config stream: %s", out)
	}
	for _, key := range []string{"downloadSettings", "scMinPostsIntervalMs", "uplinkChunkSize", "noGRPCHeader"} {
		if strings.Contains(out, `"`+key+`"`) {
			t.Fatalf("%s should be removed from xray config stream: %s", key, out)
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	xhttp := parsed["xhttpSettings"].(map[string]any)
	if xhttp["path"] != "/app" || xhttp["host"] != "example.com" {
		t.Fatalf("server fields must survive: %#v", xhttp)
	}
}

func TestStripInboundXhttpClientFields_UnchangedWithoutClientFields(t *testing.T) {
	stream := `{"network":"xhttp","xhttpSettings":{"path":"/app","mode":"stream-one"}}`
	out, changed := StripInboundXhttpClientFields(stream)
	if changed {
		t.Fatalf("expected no change, got: %s", out)
	}
	if out != stream {
		t.Fatalf("unchanged stream must be returned verbatim")
	}
}

func TestStripInboundXhttpClientFields_NonXhttpPassthrough(t *testing.T) {
	stream := `{"network":"ws","wsSettings":{"path":"/"}}`
	out, changed := StripInboundXhttpClientFields(stream)
	if changed || out != stream {
		t.Fatalf("non-xhttp stream must pass through unchanged, got changed=%v out=%s", changed, out)
	}
}

func TestGenXrayInboundConfig_OmitsInboundXmuxButDbRowUnchanged(t *testing.T) {
	stream := `{
		"network": "xhttp",
		"xhttpSettings": {
			"path": "/app",
			"mode": "stream-one",
			"xmux": { "maxConcurrency": "16-32", "hMaxRequestTimes": "600-900" }
		}
	}`
	in := Inbound{
		Protocol:       VLESS,
		Port:           443,
		Listen:         "0.0.0.0",
		Tag:            "in-xhttp",
		Settings:       `{"clients":[],"decryption":"none"}`,
		StreamSettings: stream,
	}
	cfg := in.GenXrayInboundConfig()
	if strings.Contains(string(cfg.StreamSettings), `"xmux"`) {
		t.Fatalf("GenXrayInboundConfig must not emit xmux: %s", cfg.StreamSettings)
	}
	if strings.Contains(in.StreamSettings, `"xmux"`) == false {
		t.Fatal("inbound row streamSettings must still carry xmux for subscriptions")
	}
}

func TestMixedClientsToAccounts_CompilesEnabledClientsOnly(t *testing.T) {
	settings := `{
		"auth":"noauth",
		"udp":true,
		"ip":"127.0.0.1",
		"clients":[
			{"email":"alice@example.test","password":"alice-secret","enable":true},
			{"email":"disabled@example.test","password":"disabled-secret","enable":false}
		]
	}`
	out, changed := MixedClientsToAccounts(settings)
	if !changed {
		t.Fatal("expected unified clients to compile to Xray accounts")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("compiled settings are invalid JSON: %v", err)
	}
	if _, exists := parsed["clients"]; exists {
		t.Fatalf("panel-only clients must not reach Xray: %s", out)
	}
	if parsed["auth"] != "password" {
		t.Fatalf("enabled credentials must force password auth: %#v", parsed["auth"])
	}
	accounts, ok := parsed["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("expected exactly one enabled account, got %#v", parsed["accounts"])
	}
	account := accounts[0].(map[string]any)
	if account["user"] != "alice@example.test" || account["pass"] != "alice-secret" {
		t.Fatalf("unexpected compiled account: %#v", account)
	}
	if parsed["udp"] != true || parsed["ip"] != "127.0.0.1" {
		t.Fatalf("unrelated Mixed settings were not preserved: %#v", parsed)
	}
}

func TestMixedClientsToAccounts_PreservesLegacyAccounts(t *testing.T) {
	settings := `{"auth":"password","accounts":[{"user":"legacy","pass":"secret"}],"udp":false}`
	out, changed := MixedClientsToAccounts(settings)
	if changed || out != settings {
		t.Fatalf("legacy rows must pass through until migrated, changed=%v out=%s", changed, out)
	}
}

func TestGenXrayInboundConfig_CompilesMixedClientsWithoutMutatingRow(t *testing.T) {
	settings := `{"auth":"password","clients":[{"email":"alice","password":"secret","enable":true}],"udp":false}`
	in := Inbound{Protocol: Mixed, Port: 1080, Tag: "mixed-in", Settings: settings}
	cfg := in.GenXrayInboundConfig()
	if strings.Contains(string(cfg.Settings), `"clients"`) {
		t.Fatalf("Xray config must not contain panel clients: %s", cfg.Settings)
	}
	if !strings.Contains(string(cfg.Settings), `"user": "alice"`) {
		t.Fatalf("Xray config must contain the compiled account: %s", cfg.Settings)
	}
	if in.Settings != settings {
		t.Fatal("runtime compilation must not mutate the database row")
	}
}

func TestGenXrayInboundConfigCompilesHTTPClientsWithLimits(t *testing.T) {
	settings := `{"clients":[{"email":"alice","password":"secret","enable":true,"bandwidth_bps":100000000,"committed_bps":10000000,"committed_burst_bytes":5000000}],"allowTransparent":false}`
	in := Inbound{Protocol: HTTP, Port: 8080, Tag: "http-in", Settings: settings}
	cfg := in.GenXrayInboundConfig()
	if strings.Contains(string(cfg.Settings), `"clients"`) {
		t.Fatalf("Xray config must not contain panel clients: %s", cfg.Settings)
	}
	var parsed struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(cfg.Settings, &parsed); err != nil {
		t.Fatalf("compiled HTTP settings are invalid: %v", err)
	}
	if len(parsed.Accounts) != 1 ||
		parsed.Accounts[0]["user"] != "alice" ||
		parsed.Accounts[0]["bandwidth_bps"] != float64(100_000_000) ||
		parsed.Accounts[0]["committed_bps"] != float64(10_000_000) ||
		parsed.Accounts[0]["committed_burst_bytes"] != float64(5_000_000) {
		t.Fatalf("HTTP client or limits lost: %#v", parsed.Accounts)
	}
	if in.Settings != settings {
		t.Fatal("runtime compilation must not mutate the database row")
	}
}
