package xray

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// Which panel changes restart Xray and which do not is a product promise, not
// an internal detail: a restart drops every client on the node, not just the
// edited one. This matrix is the written form of that promise — if a change
// moves between the two columns, this test says so out loud.

const (
	matrixVlessSettings = `{"clients":[{"email":"a@x","id":"11111111-1111-1111-1111-111111111111"}],"decryption":"none"}`
	matrixStream        = `{"network":"tcp","security":"none"}`
	matrixRouting       = `{"domainStrategy":"AsIs","rules":[{"type":"field","outboundTag":"direct","network":"tcp,udp"}]}`
	// GetXrayConfig always prepends an immutable bootstrap outbound so the
	// user's own default becomes hot-swappable; the matrix mirrors that shape.
	matrixOutbounds = `[{"protocol":"blackhole","tag":"panel-default-bootstrap"},{"protocol":"freedom","tag":"direct"}]`
)

func matrixConfig() *Config {
	return &Config{
		LogConfig:    json_util.RawMessage(`{"loglevel":"warning"}`),
		RouterConfig: json_util.RawMessage(matrixRouting),
		InboundConfigs: []InboundConfig{
			{
				Tag:            "api",
				Port:           62789,
				Protocol:       "dokodemo-door",
				Listen:         json_util.RawMessage(`"127.0.0.1"`),
				Settings:       json_util.RawMessage(`{"address":"127.0.0.1"}`),
				StreamSettings: json_util.RawMessage(matrixStream),
			},
			{
				Tag:            "inbound-vless",
				Port:           443,
				Protocol:       "vless",
				Listen:         json_util.RawMessage(`"0.0.0.0"`),
				Settings:       json_util.RawMessage(matrixVlessSettings),
				StreamSettings: json_util.RawMessage(matrixStream),
			},
		},
		OutboundConfigs: json_util.RawMessage(matrixOutbounds),
		Policy:          json_util.RawMessage(`{"levels":{"0":{"statsUserUplink":true}}}`),
		API:             json_util.RawMessage(`{"tag":"api","services":["HandlerService","StatsService"]}`),
		Stats:           json_util.RawMessage(`{}`),
		Reverse:         json_util.RawMessage(`{"bridges":[],"portals":[]}`),
		Metrics:         json_util.RawMessage(`{}`),
	}
}

func TestHotReloadMatrix(t *testing.T) {
	cases := []struct {
		change string
		// true = applied through the runtime API, no client on the node drops
		hot   bool
		apply func(c *Config)
	}{
		{
			change: "add an inbound",
			hot:    true,
			apply: func(c *Config) {
				c.InboundConfigs = append(c.InboundConfigs, InboundConfig{
					Tag: "inbound-new", Port: 8443, Protocol: "vless",
					Listen:         json_util.RawMessage(`"0.0.0.0"`),
					Settings:       json_util.RawMessage(matrixVlessSettings),
					StreamSettings: json_util.RawMessage(matrixStream),
				})
			},
		},
		{
			change: "remove an inbound",
			hot:    true,
			apply:  func(c *Config) { c.InboundConfigs = c.InboundConfigs[:1] },
		},
		{
			change: "add a client to an inbound",
			hot:    true,
			apply: func(c *Config) {
				c.InboundConfigs[1].Settings = json_util.RawMessage(
					`{"clients":[{"email":"a@x","id":"11111111-1111-1111-1111-111111111111"},{"email":"b@x","id":"22222222-2222-2222-2222-222222222222"}],"decryption":"none"}`)
			},
		},
		{
			change: "change a client's speed limit",
			hot:    true,
			apply: func(c *Config) {
				c.InboundConfigs[1].Settings = json_util.RawMessage(
					`{"clients":[{"email":"a@x","id":"11111111-1111-1111-1111-111111111111","bandwidth_bps":100000000,"committed_bps":20000000}],"decryption":"none"}`)
			},
		},
		{
			change: "change the user's default outbound",
			hot:    true,
			apply: func(c *Config) {
				c.OutboundConfigs = json_util.RawMessage(`[{"protocol":"blackhole","tag":"panel-default-bootstrap"},{"protocol":"freedom","settings":{"domainStrategy":"UseIP"},"tag":"direct"}]`)
			},
		},
		{
			change: "add an outbound",
			hot:    true,
			apply: func(c *Config) {
				c.OutboundConfigs = json_util.RawMessage(`[{"protocol":"blackhole","tag":"panel-default-bootstrap"},{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"blocked"}]`)
			},
		},
		{
			change: "replace the bootstrap outbound the panel pins first",
			hot:    false,
			apply: func(c *Config) {
				c.OutboundConfigs = json_util.RawMessage(`[{"protocol":"freedom","tag":"direct"}]`)
			},
		},
		{
			change: "change a routing rule",
			hot:    true,
			apply: func(c *Config) {
				c.RouterConfig = json_util.RawMessage(
					`{"domainStrategy":"AsIs","rules":[{"type":"field","ip":["geoip:private"],"outboundTag":"direct"},{"type":"field","outboundTag":"direct","network":"tcp,udp"}]}`)
			},
		},
		{
			change: "reformat the config without changing it",
			hot:    true,
			apply: func(c *Config) {
				c.Policy = json_util.RawMessage("{\n  \"levels\": { \"0\": { \"statsUserUplink\": true } }\n}")
			},
		},
		{
			change: "change the log section",
			hot:    false,
			apply:  func(c *Config) { c.LogConfig = json_util.RawMessage(`{"loglevel":"debug"}`) },
		},
		{
			change: "change the dns section",
			hot:    false,
			apply:  func(c *Config) { c.DNSConfig = json_util.RawMessage(`{"servers":["8.8.8.8"]}`) },
		},
		{
			change: "change the policy section",
			hot:    false,
			apply: func(c *Config) {
				c.Policy = json_util.RawMessage(`{"levels":{"0":{"statsUserUplink":false}}}`)
			},
		},
		{
			change: "change the reverse section",
			hot:    true,
			apply: func(c *Config) {
				c.Reverse = json_util.RawMessage(`{"bridges":[{"tag":"bridge","domain":"reverse.internal"}]}`)
			},
		},
		{
			change: "change the api section",
			hot:    false,
			apply: func(c *Config) {
				c.API = json_util.RawMessage(`{"tag":"api","services":["HandlerService"]}`)
			},
		},
		{
			change: "change the stats section",
			hot:    false,
			apply:  func(c *Config) { c.Stats = json_util.RawMessage(`{"place":"holder"}`) },
		},
		{
			change: "change the metrics section",
			hot:    false,
			apply:  func(c *Config) { c.Metrics = json_util.RawMessage(`{"tag":"metrics"}`) },
		},
		{
			change: "change the observatory section",
			hot:    false,
			apply: func(c *Config) {
				c.Observatory = json_util.RawMessage(`{"subjectSelector":["out"]}`)
			},
		},
		{
			change: "change the api inbound itself",
			hot:    false,
			apply:  func(c *Config) { c.InboundConfigs[0].Port = 62790 },
		},
		{
			change: "change routing domainStrategy",
			hot:    false,
			apply: func(c *Config) {
				c.RouterConfig = json_util.RawMessage(`{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","outboundTag":"direct","network":"tcp,udp"}]}`)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.change, func(t *testing.T) {
			oldCfg := matrixConfig()
			newCfg := matrixConfig()
			c.apply(newCfg)

			_, hot := ComputeHotDiff(oldCfg, newCfg)
			if hot == c.hot {
				return
			}
			if c.hot {
				t.Errorf("%q now restarts Xray. Every client on this node drops, not just the edited one — if that is intended, move this row and say so in the release notes", c.change)
			} else {
				t.Errorf("%q is now hot-applied. That is an improvement, but the matrix is the documented promise — move this row deliberately", c.change)
			}
		})
	}
}

func TestFirstReverseEnableStillForcesRestart(t *testing.T) {
	oldCfg := matrixConfig()
	oldCfg.Reverse = nil
	newCfg := matrixConfig()
	newCfg.Reverse = json_util.RawMessage(`{"bridges":[{"tag":"bridge","domain":"reverse.internal"}]}`)

	if _, hot := ComputeHotDiff(oldCfg, newCfg); hot {
		t.Fatal("first reverse enable must restart because the running core has no reverse app")
	}
}

// Guard the shape of the promise, not just its contents: a new section added to
// Config with no reload API must land in the restart column on purpose.
func TestEverySectionWithoutReloadAPIIsListed(t *testing.T) {
	want := []string{"log", "dns", "transport", "policy", "api", "stats", "fakedns", "observatory", "burstObservatory", "metrics", "geodata", "env"}
	src := hotDiffStaticSectionNames()
	for _, name := range want {
		if !src[name] {
			t.Errorf("section %q dropped out of the restart-forcing list — a change there would now be silently ignored instead of restarting", name)
		}
	}
	if len(src) != len(want) {
		var extra []string
		for name := range src {
			if !contains(want, name) {
				extra = append(extra, name)
			}
		}
		t.Errorf("restart-forcing sections changed: unexpected %v — update the matrix and the docs", strings.Join(extra, ","))
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
