package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func hotDefaultConfig(outbounds, routing string) *xray.Config {
	return &xray.Config{
		OutboundConfigs: json_util.RawMessage(outbounds),
		RouterConfig:    json_util.RawMessage(routing),
	}
}

func TestEnsureHotReloadableDefaultOutbound(t *testing.T) {
	cfg := hotDefaultConfig(
		`[{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"blocked"}]`,
		`{"domainStrategy":"AsIs","rules":[{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}]}`,
	)
	ensureHotReloadableDefaultOutbound(cfg)

	var outbounds []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatalf("outbounds are invalid: %v", err)
	}
	if len(outbounds) != 3 || outbounds[0]["tag"] != internalDefaultOutboundTag {
		t.Fatalf("immutable bootstrap must be first: %#v", outbounds)
	}
	if outbounds[0]["protocol"] != "blackhole" {
		t.Fatalf("bootstrap must fail closed: %#v", outbounds[0])
	}

	var routing map[string]any
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatalf("routing is invalid: %v", err)
	}
	rules := routing["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["outboundTag"] != "direct" || last["network"] != "tcp,udp" {
		t.Fatalf("final rule must preserve the user's effective default: %#v", last)
	}
}

func TestHotDefaultRuleStaysAfterPanelAndNodeEgressRules(t *testing.T) {
	cfg := hotDefaultConfig(
		`[{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"blocked"}]`,
		`{"domainStrategy":"AsIs","rules":[]}`,
	)
	injectPanelEgress(cfg, "blocked")
	injectNodeEgresses(cfg, []*model.Node{{Id: 7, Enable: true, OutboundTag: "blocked"}})
	ensureHotReloadableDefaultOutbound(cfg)

	var routing map[string]any
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatalf("routing is invalid: %v", err)
	}
	rules := routing["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("expected two dedicated rules and one default, got %#v", rules)
	}
	last := rules[len(rules)-1].(map[string]any)
	if _, hasInboundMatch := last["inboundTag"]; hasInboundMatch {
		t.Fatalf("catch-all must be last, after dedicated egress rules: %#v", rules)
	}
	if last["outboundTag"] != "direct" {
		t.Fatalf("catch-all must preserve the user default: %#v", last)
	}
}

func TestHotDefaultMakesFirstUserOutboundChangeHotApplicable(t *testing.T) {
	oldCfg := hotDefaultConfig(
		`[{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"blocked"}]`,
		`{"domainStrategy":"AsIs","rules":[]}`,
	)
	newCfg := hotDefaultConfig(
		`[{"protocol":"blackhole","tag":"blocked"},{"protocol":"freedom","settings":{"domainStrategy":"UseIP"},"tag":"direct"}]`,
		`{"domainStrategy":"AsIs","rules":[]}`,
	)
	ensureHotReloadableDefaultOutbound(oldCfg)
	ensureHotReloadableDefaultOutbound(newCfg)

	diff, ok := xray.ComputeHotDiff(oldCfg, newCfg)
	if !ok {
		t.Fatal("changing the user's effective default must be hot-applicable")
	}
	if len(diff.RemovedOutboundTags) == 0 || len(diff.AddedOutbounds) == 0 {
		t.Fatalf("expected outbound handler changes, got %+v", diff)
	}
	if diff.RoutingConfig == nil {
		t.Fatal("changing the effective default must hot-reload routing")
	}
}

func TestEnsureHotReloadableDefaultOutboundSkipsReservedCollision(t *testing.T) {
	raw := `[{"protocol":"freedom","tag":"` + internalDefaultOutboundTag + `"}]`
	cfg := hotDefaultConfig(raw, `{"rules":[]}`)
	ensureHotReloadableDefaultOutbound(cfg)
	if string(cfg.OutboundConfigs) != raw {
		t.Fatalf("reserved collision must not produce duplicate tags: %s", cfg.OutboundConfigs)
	}
}

func TestCheckXrayConfigRejectsReservedDefaultOutboundTag(t *testing.T) {
	template := `{
		"outbounds":[{"protocol":"blackhole","settings":{},"tag":"` + internalDefaultOutboundTag + `"}],
		"routing":{"rules":[]}
	}`
	err := (&XraySettingService{}).CheckXrayConfig(template)
	if err == nil {
		t.Fatal("reserved outbound tag must be rejected")
	}
	want := `outbound tag "` + internalDefaultOutboundTag + `" is reserved by the panel`
	if err.Error() != want {
		t.Fatalf("reserved tag error = %q, want %q", err.Error(), want)
	}
}
