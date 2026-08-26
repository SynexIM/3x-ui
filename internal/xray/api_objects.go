package xray

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/util/common"

	"github.com/xtls/xray-core/app/proxyman/command"
	routerService "github.com/xtls/xray-core/app/router/command"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/infra/conf"
)

// listRPCTimeout bounds the read-only listings. They are served from in-memory
// handler maps, so anything slower than this is a stuck connection, not work.
const listRPCTimeout = 10 * time.Second

// RuntimeOutbound is one outbound handler the running core currently holds.
// Only the tag survives the round trip: the core keeps built handlers, not the
// JSON they came from, so the stored template stays the place to read settings.
type RuntimeOutbound struct {
	Tag string `json:"tag" example:"proxy-jp"`
}

// RuntimeRule is one routing rule the running core currently holds.
//
// Only the two tags are readable here. The core's richer ListRuleFull would
// also report the rule's user and inbound conditions, but the xray-core this
// module pins panics the WHOLE CORE when it is called: ListRule builds each
// Route with a nil embedded routing.Context and ListRuleFull then calls
// GetUser() on it (app/router/router.go ListRule + app/router/command
// command.go:146). Fixed in the fork at 7300d185 and verified against a real
// core, but that commit has no released module version yet, so switching this
// call over has to wait for the dependency bump — doing it sooner would take
// the node down on every listing. The conditions are read from the stored
// template meanwhile, which is the authority for them anyway.
type RuntimeRule struct {
	RuleTag     string `json:"ruleTag" example:"ipl_route_ln000001"`
	OutboundTag string `json:"outboundTag" example:"proxy-jp"`
}

// ListOutbounds reports the outbound tags loaded in the running core, which is
// the only way to tell a persisted outbound from one that actually took effect.
func (x *XrayAPI) ListOutbounds() ([]RuntimeOutbound, error) {
	if x.HandlerServiceClient == nil {
		return nil, common.NewError("xray HandlerServiceClient is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), listRPCTimeout)
	defer cancel()

	resp, err := (*x.HandlerServiceClient).ListOutbounds(ctx, &command.ListOutboundsRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]RuntimeOutbound, 0, len(resp.GetOutbounds()))
	for _, handler := range resp.GetOutbounds() {
		out = append(out, RuntimeOutbound{Tag: handler.GetTag()})
	}
	return out, nil
}

// ListInboundTags reports the inbound tags loaded in the running core. Only
// tags: the core keeps built handlers, so the panel's own records stay the
// place to read an inbound's shape — what the core adds is which ones are
// really listening.
func (x *XrayAPI) ListInboundTags() ([]string, error) {
	if x.HandlerServiceClient == nil {
		return nil, common.NewError("xray HandlerServiceClient is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), listRPCTimeout)
	defer cancel()

	resp, err := (*x.HandlerServiceClient).ListInbounds(ctx, &command.ListInboundsRequest{IsOnlyTags: true})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.GetInbounds()))
	for _, handler := range resp.GetInbounds() {
		out = append(out, handler.GetTag())
	}
	return out, nil
}

// ListRules reports the routing rules loaded in the running core.
//
// Deliberately ListRule and not ListRuleFull: see RuntimeRule for why the
// richer call takes the core down with it.
func (x *XrayAPI) ListRules() ([]RuntimeRule, error) {
	if x.RoutingServiceClient == nil {
		return nil, common.NewError("xray RoutingServiceClient is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), listRPCTimeout)
	defer cancel()

	resp, err := (*x.RoutingServiceClient).ListRule(ctx, &routerService.ListRuleRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]RuntimeRule, 0, len(resp.GetRules()))
	for _, rule := range resp.GetRules() {
		out = append(out, RuntimeRule{
			RuleTag:     rule.GetRuleTag(),
			OutboundTag: rule.GetTag(),
		})
	}
	return out, nil
}

// RemoveRule drops one routing rule from the running core by its ruleTag.
func (x *XrayAPI) RemoveRule(ruleTag string) error {
	if x.RoutingServiceClient == nil {
		return common.NewError("xray RoutingServiceClient is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), listRPCTimeout)
	defer cancel()

	_, err := (*x.RoutingServiceClient).RemoveRule(ctx, &routerService.RemoveRuleRequest{RuleTag: ruleTag})
	return err
}

// AppendRules adds rules to the end of the running core's rule list in one
// gRPC call. Each element is one routing rule object, the same shape the
// template's routing.rules array holds.
//
// The core checks tags one rule at a time, so a partially applied batch is
// possible; the returned index is the first rule that did not go in, which is
// what the caller needs to undo the ones before it.
func (x *XrayAPI) AppendRules(rules []json.RawMessage) (failedIndex int, err error) {
	if x.RoutingServiceClient == nil {
		return -1, common.NewError("xray RoutingServiceClient is not initialized")
	}
	if len(rules) == 0 {
		return -1, nil
	}
	ensureXrayAssetLocation()

	configs := make([]*serial.TypedMessage, 0, len(rules))
	for i, rule := range rules {
		built, buildErr := buildSingleRuleConfig(rule)
		if buildErr != nil {
			return i, buildErr
		}
		configs = append(configs, built)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := (*x.RoutingServiceClient).BatchAddRule(ctx, &routerService.BatchAddRuleRequest{
		Configs:      configs,
		ShouldAppend: true,
	})
	if err != nil {
		return -1, err
	}
	for _, result := range resp.GetResults() {
		if !result.GetSuccess() {
			return int(result.GetIndex()), common.NewError("xray refused routing rule ", result.GetIndex(), ": ", result.GetError())
		}
	}
	return -1, nil
}

// ValidateRoutingRules builds the given rules through xray-core's own config
// loader, so a malformed rule is refused before it is persisted rather than
// after, when the only remaining move is to roll the template back.
func ValidateRoutingRules(rules []json.RawMessage) error {
	ensureXrayAssetLocation()
	for _, rule := range rules {
		if _, err := buildSingleRuleConfig(rule); err != nil {
			return err
		}
	}
	return nil
}

// buildSingleRuleConfig wraps one rule object in the router config the gRPC
// API takes: AddRule carries a whole app.router.Config, not a bare rule.
func buildSingleRuleConfig(rule json.RawMessage) (*serial.TypedMessage, error) {
	wrapped, err := json.Marshal(map[string]any{"rules": []json.RawMessage{rule}})
	if err != nil {
		return nil, err
	}
	routerConf := new(conf.RouterConfig)
	if err := json.Unmarshal(wrapped, routerConf); err != nil {
		return nil, err
	}
	built, err := routerConf.Build()
	if err != nil {
		return nil, err
	}
	return serial.ToTypedMessage(built), nil
}
