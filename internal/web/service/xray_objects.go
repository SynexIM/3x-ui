package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Outbounds and routing rules as first-class objects.
//
// AUTHORITY. The stored template (setting `xrayTemplateConfig`) is the single
// source of truth for both. Every write here persists to the template first and
// only then reconciles the running core over the xray gRPC API, because a core
// start rebuilds itself from the template: anything applied to the runtime but
// not persisted disappears at the next restart, silently. When the runtime step
// fails the template write is rolled back, so the two never disagree except in
// the one case the caller is told about — a change the core has no reload API
// for, which is persisted, reported as requiresRestart and picked up whenever
// the panel next restarts the core.
//
// Contrast with the whole-template save (POST /panel/api/xray/update): that one
// still works and is still the way to rewrite the config wholesale. These
// endpoints exist so that adding one outbound does not mean resending, and
// revalidating, everything else on the node.

// xrayTemplateWriteLock serializes read-modify-write cycles on the template.
// Two concurrent single-object writes would otherwise each save a copy built
// from the same starting text, and the loser's object would vanish.
var xrayTemplateWriteLock sync.Mutex

// ErrXrayObjectNotFound is returned when a tag addresses nothing in the
// template. Callers turn it into 404 rather than a generic failure.
var ErrXrayObjectNotFound = errors.New("no object with that tag")

// XrayObjectService is the single-object surface over the template's outbounds
// and routing rules.
type XrayObjectService struct {
	settingService SettingService
	xrayService    XrayService
}

// ObjectApplyResult says what happened to the running core, which is the part
// a caller cannot see from the stored template alone.
type ObjectApplyResult struct {
	Tag string `json:"tag" example:"proxy-jp"`
	// HotApplied is true when the running core was updated over the gRPC API,
	// so no connection was dropped.
	HotApplied bool `json:"hotApplied" example:"true"`
	// RequiresRestart is true when the change is persisted but the running core
	// cannot take it without being restarted.
	RequiresRestart bool `json:"requiresRestart" example:"false"`
	// XrayRunning is false when there was no core to update at all.
	XrayRunning bool `json:"xrayRunning" example:"true"`
	// Count is how many objects the call wrote; 1 except for a batch.
	Count int `json:"count" example:"1"`
}

// OutboundListView pairs what is persisted with what the core is really
// running, so "saved" and "in effect" can never be confused for one another.
type OutboundListView struct {
	Outbounds []json.RawMessage      `json:"outbounds"`
	Runtime   []xray.RuntimeOutbound `json:"runtime"`
	// RuntimeError explains why Runtime is empty when the core is up but did
	// not answer, instead of letting it read as "the core has no outbounds".
	RuntimeError string `json:"runtimeError,omitempty" example:""`
}

// RoutingRuleListView is the routing counterpart of OutboundListView.
type RoutingRuleListView struct {
	Rules        []json.RawMessage  `json:"rules"`
	Runtime      []xray.RuntimeRule `json:"runtime"`
	RuntimeError string             `json:"runtimeError,omitempty" example:""`
}

// ---------------------------------------------------------------- outbounds

func (s *XrayObjectService) ListOutbounds() (*OutboundListView, error) {
	_, cfg, err := s.loadTemplate()
	if err != nil {
		return nil, err
	}
	stored, err := decodeArray(cfg["outbounds"])
	if err != nil {
		return nil, err
	}
	view := &OutboundListView{Outbounds: stored, Runtime: []xray.RuntimeOutbound{}}
	if runtime, listErr := s.runtimeOutbounds(); listErr != nil {
		view.RuntimeError = listErr.Error()
	} else if runtime != nil {
		view.Runtime = runtime
	}
	return view, nil
}

// AddOutbound appends one outbound to the template and adds it to the running
// core. Appending, never inserting: the core's first outbound is fixed at
// process start, so a new one at the front would force a restart.
func (s *XrayObjectService) AddOutbound(raw json.RawMessage) (*ObjectApplyResult, error) {
	tag, err := objectTag(raw, "outbound")
	if err != nil {
		return nil, err
	}
	if err := xray.ValidateOutboundConfig(raw); err != nil {
		return nil, common.NewError("xray core rejects outbound \""+tag+"\":", err)
	}
	return s.mutate(tag, 1, func(cfg map[string]json.RawMessage) error {
		outbounds, err := decodeArray(cfg["outbounds"])
		if err != nil {
			return err
		}
		if indexByTag(outbounds, tag) >= 0 {
			return common.NewErrorf("an outbound tagged %q already exists", tag)
		}
		return encodeArray(cfg, "outbounds", append(outbounds, raw))
	}, func() error {
		return s.xrayService.ApplyOutboundHotOnly(tag, raw)
	})
}

// UpdateOutbound replaces the outbound carrying tag. The tag is the identity,
// so a body that renames it is refused rather than quietly creating a second
// outbound and orphaning every routing rule pointing at the old name.
func (s *XrayObjectService) UpdateOutbound(tag string, raw json.RawMessage) (*ObjectApplyResult, error) {
	bodyTag, err := objectTag(raw, "outbound")
	if err != nil {
		return nil, err
	}
	if bodyTag != tag {
		return nil, common.NewErrorf("the body is tagged %q but the path addresses %q; delete and re-add to rename", bodyTag, tag)
	}
	if err := xray.ValidateOutboundConfig(raw); err != nil {
		return nil, common.NewError("xray core rejects outbound \""+tag+"\":", err)
	}
	return s.mutate(tag, 1, func(cfg map[string]json.RawMessage) error {
		outbounds, err := decodeArray(cfg["outbounds"])
		if err != nil {
			return err
		}
		idx := indexByTag(outbounds, tag)
		if idx < 0 {
			return ErrXrayObjectNotFound
		}
		outbounds[idx] = raw
		return encodeArray(cfg, "outbounds", outbounds)
	}, func() error {
		return s.xrayService.ApplyOutboundHotOnly(tag, raw)
	})
}

func (s *XrayObjectService) DeleteOutbound(tag string) (*ObjectApplyResult, error) {
	if strings.TrimSpace(tag) == "" {
		return nil, common.NewError("an outbound tag is required")
	}
	return s.mutate(tag, 1, func(cfg map[string]json.RawMessage) error {
		outbounds, err := decodeArray(cfg["outbounds"])
		if err != nil {
			return err
		}
		idx := indexByTag(outbounds, tag)
		if idx < 0 {
			return ErrXrayObjectNotFound
		}
		return encodeArray(cfg, "outbounds", append(outbounds[:idx:idx], outbounds[idx+1:]...))
	}, func() error {
		return s.xrayService.ApplyOutboundHotOnly(tag, nil)
	})
}

// ------------------------------------------------------------------ routing

func (s *XrayObjectService) ListRoutingRules() (*RoutingRuleListView, error) {
	_, cfg, err := s.loadTemplate()
	if err != nil {
		return nil, err
	}
	rules, err := templateRules(cfg)
	if err != nil {
		return nil, err
	}
	view := &RoutingRuleListView{Rules: rules, Runtime: []xray.RuntimeRule{}}
	if runtime, listErr := s.runtimeRules(); listErr != nil {
		view.RuntimeError = listErr.Error()
	} else if runtime != nil {
		view.Runtime = runtime
	}
	return view, nil
}

// AddRoutingRules appends rules to the end of the template's rule list, which
// is where xray's first-match router puts the most specific overrides last.
//
// One call writes the whole batch or none of it: a half-applied batch would
// leave the caller unable to say which clients now route where.
func (s *XrayObjectService) AddRoutingRules(rules []json.RawMessage) (*ObjectApplyResult, error) {
	if len(rules) == 0 {
		return nil, common.NewError("at least one routing rule is required")
	}
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		tag, err := ruleTag(rule)
		if err != nil {
			return nil, err
		}
		if seen[tag] {
			return nil, common.NewErrorf("routing rule tag %q appears twice in the same request", tag)
		}
		seen[tag] = true
	}
	if err := xray.ValidateRoutingRules(rules); err != nil {
		return nil, common.NewError("xray core rejects the routing rules:", err)
	}

	firstTag, _ := ruleTag(rules[0])
	incomingTags := make([]string, 0, len(seen))
	for tag := range seen {
		incomingTags = append(incomingTags, tag)
	}
	return s.mutate(firstTag, len(rules), func(cfg map[string]json.RawMessage) error {
		// Fast path: splice without decoding the rules already there. Declines
		// (and falls through) whenever it cannot be sure — see appendTemplateRules.
		switch done, err := appendTemplateRules(cfg, rules, incomingTags); {
		case err != nil:
			return err
		case done:
			return nil
		}
		existing, err := templateRules(cfg)
		if err != nil {
			return err
		}
		for _, rule := range existing {
			tag, _ := ruleTag(rule)
			if tag != "" && seen[tag] {
				return common.NewErrorf("a routing rule tagged %q already exists", tag)
			}
		}
		return writeTemplateRules(cfg, append(existing, rules...))
	}, func() error {
		return s.xrayService.AppendRoutingRulesHotOnly(rules)
	})
}

func (s *XrayObjectService) DeleteRoutingRule(tag string) (*ObjectApplyResult, error) {
	if strings.TrimSpace(tag) == "" {
		return nil, common.NewError("a routing rule tag is required")
	}
	return s.mutate(tag, 1, func(cfg map[string]json.RawMessage) error {
		rules, err := templateRules(cfg)
		if err != nil {
			return err
		}
		kept := make([]json.RawMessage, 0, len(rules))
		found := false
		for _, rule := range rules {
			if ruleTagOf(rule) == tag {
				found = true
				continue
			}
			kept = append(kept, rule)
		}
		if !found {
			return ErrXrayObjectNotFound
		}
		return writeTemplateRules(cfg, kept)
	}, func() error {
		return s.xrayService.RemoveRoutingRuleHotOnly(tag)
	})
}

// ------------------------------------------------------------------ plumbing

// mutate runs one read-modify-write cycle over the template and then makes the
// running core match it, rolling the template back if the core refuses.
//
// apply drives the core's primitive for this one object rather than rebuilding
// and diffing the whole runtime config. The legacy template JSON still has to
// be rewritten here; the scale acceptance reports that persistence cost
// separately instead of hiding it inside the runtime result.
func (s *XrayObjectService) mutate(tag string, count int, edit func(cfg map[string]json.RawMessage) error, apply func() error) (*ObjectApplyResult, error) {
	xrayTemplateWriteLock.Lock()
	defer xrayTemplateWriteLock.Unlock()

	before, cfg, err := s.loadTemplate()
	if err != nil {
		return nil, err
	}
	if err := edit(cfg); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := s.settingService.saveSetting("xrayTemplateConfig", string(encoded)); err != nil {
		return nil, err
	}

	result := &ObjectApplyResult{Tag: tag, Count: count, XrayRunning: s.xrayService.IsXrayRunning()}
	if !result.XrayRunning {
		return result, nil
	}
	switch err := apply(); {
	case err == nil:
		result.HotApplied = true
	case errors.Is(err, ErrXrayHotApplyImpossible):
		// Persisted and honest about it: the core keeps running the previous
		// shape until someone restarts it.
		result.RequiresRestart = true
		s.xrayService.SetToNeedRestart()
	default:
		if restoreErr := s.settingService.saveSetting("xrayTemplateConfig", before); restoreErr != nil {
			return nil, common.NewError("the core refused the change ("+err.Error()+") and the template could not be rolled back:", restoreErr)
		}
		return nil, common.NewError("the core refused the change, nothing was saved:", err)
	}
	return result, nil
}

func (s *XrayObjectService) loadTemplate() (string, map[string]json.RawMessage, error) {
	raw, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return "", nil, err
	}
	raw = UnwrapXrayTemplateConfig(raw)
	cfg := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", nil, common.NewError("the stored xray template is not a JSON object:", err)
	}
	return raw, cfg, nil
}

func (s *XrayObjectService) runtimeOutbounds() ([]xray.RuntimeOutbound, error) {
	var out []xray.RuntimeOutbound
	err := s.xrayService.withRunningAPI(func(api *xray.XrayAPI) error {
		listed, err := api.ListOutbounds()
		out = listed
		return err
	})
	return out, err
}

func (s *XrayObjectService) runtimeRules() ([]xray.RuntimeRule, error) {
	var out []xray.RuntimeRule
	err := s.xrayService.withRunningAPI(func(api *xray.XrayAPI) error {
		listed, err := api.ListRules()
		out = listed
		return err
	})
	return out, err
}

// templateRules reads routing.rules, tolerating a template with no routing
// section at all — a fresh install has one, but a hand-edited one may not.
func templateRules(cfg map[string]json.RawMessage) ([]json.RawMessage, error) {
	routing := map[string]json.RawMessage{}
	if raw, ok := cfg["routing"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &routing); err != nil {
			return nil, common.NewError("the template's routing section is not a JSON object:", err)
		}
	}
	return decodeArray(routing["rules"])
}

// appendTemplateRules adds rules to routing.rules **without decoding the rules
// already there**.
//
// The straightforward shape — decode the array, walk it to check for a tag
// collision, re-encode — costs, measured at 50,000 rules (a 5.2 MB array):
//
//	decode into 50k RawMessage   62ms
//	50k × ruleTagOf (Unmarshal)  42ms
//	re-encode the array          77ms
//
// 181ms to append one rule, and all of it is about the other 49,999.
//
// The collision check does not need to parse anything. A rule carrying tag T
// must contain the bytes of T somewhere in its JSON; so if the whole array does
// not contain those bytes, no rule has that tag — a single scan over 5.2 MB
// costs 2ms and settles it. The reverse is not true (T could appear inside an
// email or a domain), so a hit falls back to the exact walk. False positives
// cost one slow path; false negatives are impossible.
//
// The append itself is a splice: a JSON array ends with ']', so the new
// elements go in front of it. Anything that does not look like a well-formed
// array is handed to the slow path rather than guessed at.
//
// Returns false when it declined, and the caller runs the decoding version.
func appendTemplateRules(cfg map[string]json.RawMessage, rules []json.RawMessage, tags []string) (bool, error) {
	routing := map[string]json.RawMessage{}
	if raw, ok := cfg["routing"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &routing); err != nil {
			return false, common.NewError("the template's routing section is not a JSON object:", err)
		}
	}
	existing := bytes.TrimSpace(routing["rules"])

	// No array yet, or something we do not recognise: let the slow path build it.
	if len(existing) == 0 || existing[0] != '[' || existing[len(existing)-1] != ']' {
		return false, nil
	}
	// A tag's bytes appear somewhere → cannot rule out a collision here.
	for _, tag := range tags {
		if tag != "" && bytes.Contains(existing, []byte(tag)) {
			return false, nil
		}
	}

	added := make([]byte, 0, len(rules)*128)
	for i, rule := range rules {
		if i > 0 {
			added = append(added, ',')
		}
		added = append(added, bytes.TrimSpace(rule)...)
	}

	spliced := make([]byte, 0, len(existing)+len(added)+2)
	if inner := bytes.TrimSpace(existing[1 : len(existing)-1]); len(inner) == 0 {
		spliced = append(spliced, '[')
		spliced = append(spliced, added...)
		spliced = append(spliced, ']')
	} else {
		spliced = append(spliced, existing[:len(existing)-1]...)
		spliced = append(spliced, ',')
		spliced = append(spliced, added...)
		spliced = append(spliced, ']')
	}
	if !json.Valid(spliced) {
		// Never persist something we spliced wrong. Cheap insurance next to the
		// cost of a corrupted routing section.
		return false, nil
	}

	routing["rules"] = spliced
	encodedRouting, err := json.Marshal(routing)
	if err != nil {
		return false, err
	}
	cfg["routing"] = encodedRouting
	return true, nil
}

func writeTemplateRules(cfg map[string]json.RawMessage, rules []json.RawMessage) error {
	routing := map[string]json.RawMessage{}
	if raw, ok := cfg["routing"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &routing); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	routing["rules"] = encoded
	encodedRouting, err := json.Marshal(routing)
	if err != nil {
		return err
	}
	cfg["routing"] = encodedRouting
	return nil
}

func decodeArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return []json.RawMessage{}, nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, common.NewError("expected a JSON array:", err)
	}
	if out == nil {
		out = []json.RawMessage{}
	}
	return out, nil
}

func encodeArray(cfg map[string]json.RawMessage, key string, values []json.RawMessage) error {
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	cfg[key] = encoded
	return nil
}

func indexByTag(objects []json.RawMessage, tag string) int {
	for i, object := range objects {
		if tagOf(object) == tag {
			return i
		}
	}
	return -1
}

func tagOf(object json.RawMessage) string {
	var tagged struct {
		Tag string `json:"tag"`
	}
	_ = json.Unmarshal(object, &tagged)
	return tagged.Tag
}

func ruleTagOf(object json.RawMessage) string {
	var tagged struct {
		RuleTag string `json:"ruleTag"`
	}
	_ = json.Unmarshal(object, &tagged)
	return tagged.RuleTag
}

// objectTag insists on a tag: it is the only handle the delete and update
// endpoints have, and xray itself keys its runtime handlers on it.
func objectTag(raw json.RawMessage, kind string) (string, error) {
	tag := strings.TrimSpace(tagOf(raw))
	if tag == "" {
		return "", common.NewErrorf("this %s has no tag; a tag is what every later request addresses it by", kind)
	}
	if tag == internalDefaultOutboundTag {
		return "", common.NewErrorf("outbound tag %q is reserved by the panel", internalDefaultOutboundTag)
	}
	return tag, nil
}

// ruleTag insists on a ruleTag for the same reason, plus one more: xray's own
// RemoveRule takes a ruleTag, so an untagged rule cannot be removed from a
// running core at all.
func ruleTag(raw json.RawMessage) (string, error) {
	tag := strings.TrimSpace(ruleTagOf(raw))
	if tag == "" {
		return "", common.NewError("this routing rule has no ruleTag; without one it can never be addressed or removed again")
	}
	return tag, nil
}

// ------------------------------------------------------------ runtime status

// RuntimeInbound is one of this panel's own inbounds, paired with whether the
// running core has actually loaded it. Configured and listening are different
// facts, and the whole point of this view is not to confuse them.
type RuntimeInbound struct {
	Tag      string `json:"tag" example:"vless-in"`
	Protocol string `json:"protocol" example:"vless"`
	Port     int    `json:"port" example:"443"`
	Enabled  bool   `json:"enabled" example:"true"`
	// Loaded is true when the core reports a handler with this tag.
	Loaded  bool  `json:"loaded" example:"true"`
	Clients int   `json:"clients" example:"120"`
	Up      int64 `json:"up" example:"10240"`
	Down    int64 `json:"down" example:"20480"`
}

// XrayRuntimeView is the read-only answer to "what is this node actually
// running right now", assembled from the core rather than from the database.
type XrayRuntimeView struct {
	Running       bool                   `json:"running" example:"true"`
	PID           int                    `json:"pid" example:"1234"`
	Version       string                 `json:"version" example:"26.7.28"`
	UptimeSeconds uint64                 `json:"uptimeSeconds" example:"3600"`
	Inbounds      []RuntimeInbound       `json:"inbounds"`
	Outbounds     []xray.RuntimeOutbound `json:"outbounds"`
	Rules         []xray.RuntimeRule     `json:"rules"`
	OnlineClients int                    `json:"onlineClients" example:"37"`
	TotalUp       int64                  `json:"totalUp" example:"1048576"`
	TotalDown     int64                  `json:"totalDown" example:"2097152"`
	// RuntimeError explains an empty core-side list when the core is up but did
	// not answer, so "nothing loaded" is never guessed from silence.
	RuntimeError string `json:"runtimeError,omitempty" example:""`
}

// RuntimeSnapshot reads the running core and pairs it with this panel's own
// records. Nothing here writes: it is the page an operator opens to find out
// whether what the panel shows is what the node is doing.
func (s *XrayObjectService) RuntimeSnapshot() (*XrayRuntimeView, error) {
	view := &XrayRuntimeView{
		Running:   s.xrayService.IsXrayRunning(),
		PID:       s.xrayService.GetXrayPID(),
		Version:   s.xrayService.GetXrayVersion(),
		Inbounds:  []RuntimeInbound{},
		Outbounds: []xray.RuntimeOutbound{},
		Rules:     []xray.RuntimeRule{},
	}
	if process := currentXrayProcess(); process != nil && process.IsRunning() {
		view.UptimeSeconds = process.GetUptime()
	}

	inbounds, err := (&InboundService{}).GetAllInbounds()
	if err != nil {
		return nil, err
	}

	loaded := map[string]bool{}
	if view.Running {
		if err := s.xrayService.withRunningAPI(func(api *xray.XrayAPI) error {
			tags, err := api.ListInboundTags()
			if err != nil {
				return err
			}
			for _, tag := range tags {
				loaded[tag] = true
			}
			if view.Outbounds, err = api.ListOutbounds(); err != nil {
				return err
			}
			view.Rules, err = api.ListRules()
			return err
		}); err != nil {
			view.RuntimeError = err.Error()
		}
	}

	for _, inbound := range inbounds {
		// Node inbounds belong to a remote panel's core, not this one.
		if inbound.NodeID != nil {
			continue
		}
		view.Inbounds = append(view.Inbounds, RuntimeInbound{
			Tag:      inbound.Tag,
			Protocol: string(inbound.Protocol),
			Port:     inbound.Port,
			Enabled:  inbound.Enable,
			Loaded:   loaded[inbound.Tag],
			Clients:  len(inbound.ClientStats),
			Up:       inbound.Up,
			Down:     inbound.Down,
		})
		view.TotalUp += inbound.Up
		view.TotalDown += inbound.Down
	}

	if online, ok, err := s.xrayService.GetOnlineUsers(); err == nil && ok {
		view.OnlineClients = len(online)
	}
	return view, nil
}
