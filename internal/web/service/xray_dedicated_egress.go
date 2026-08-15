package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// DedicatedEgressSpec is the small, platform-owned contract for one
// dedicated upstream. Credentials are accepted only for building the Xray
// config and are never returned in the result or written to logs.
type DedicatedEgressSpec struct {
	Tag        string `json:"tag"`
	InboundTag string `json:"inboundTag"`
	User       string `json:"user"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

type DedicatedEgressResult struct {
	Tag             string `json:"tag"`
	OutboundPresent bool   `json:"outboundPresent"`
	RoutePresent    bool   `json:"routePresent"`
	SpecMatches     bool   `json:"specMatches"`
}

var dedicatedEgressMu sync.Mutex
var dedicatedEgressTag = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// UpsertDedicatedEgress persists one tagged SOCKS outbound and an inbound/user
// route, then reconciles the running Xray process. Repeating the same request
// is idempotent by tag and replaces only this dedicated route.
func (s *XrayService) UpsertDedicatedEgress(spec DedicatedEgressSpec) (DedicatedEgressResult, error) {
	spec = normalizeDedicatedEgressSpec(spec)
	if err := validateDedicatedEgressSpec(spec); err != nil {
		return DedicatedEgressResult{}, err
	}
	dedicatedEgressMu.Lock()
	template, err := s.settingService.GetXrayConfigTemplate()
	if err == nil {
		template, err = upsertDedicatedEgressConfig(template, spec)
	}
	if err == nil {
		err = (&XraySettingService{}).SaveXraySetting(template)
	}
	dedicatedEgressMu.Unlock()
	if err != nil {
		return DedicatedEgressResult{}, err
	}
	if err := s.RestartXray(false); err != nil {
		return DedicatedEgressResult{}, err
	}
	result, err := s.ObserveDedicatedEgress(spec)
	if err != nil {
		return DedicatedEgressResult{}, err
	}
	if !result.SpecMatches {
		return DedicatedEgressResult{}, fmt.Errorf("dedicated egress readback incomplete for tag %q", spec.Tag)
	}
	return result, nil
}

// ObserveDedicatedEgress compares persisted Xray state with the requested projection.
func (s *XrayService) ObserveDedicatedEgress(spec DedicatedEgressSpec) (DedicatedEgressResult, error) {
	spec = normalizeDedicatedEgressSpec(spec)
	if err := validateDedicatedEgressSpec(spec); err != nil {
		return DedicatedEgressResult{}, err
	}
	template, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return DedicatedEgressResult{}, err
	}
	return dedicatedEgressObservation(template, spec)
}

// RemoveDedicatedEgress removes only the tagged dedicated outbound and its
// route. The customer inbound and all other outbounds remain untouched.
func (s *XrayService) RemoveDedicatedEgress(tag string) (DedicatedEgressResult, error) {
	tag = strings.TrimSpace(tag)
	if !dedicatedEgressTag.MatchString(tag) {
		return DedicatedEgressResult{}, errors.New("invalid dedicated egress tag")
	}
	dedicatedEgressMu.Lock()
	template, err := s.settingService.GetXrayConfigTemplate()
	if err == nil {
		template, err = removeDedicatedEgressConfig(template, tag)
	}
	if err == nil {
		err = (&XraySettingService{}).SaveXraySetting(template)
	}
	dedicatedEgressMu.Unlock()
	if err != nil {
		return DedicatedEgressResult{}, err
	}
	if err := s.RestartXray(false); err != nil {
		return DedicatedEgressResult{}, err
	}
	outboundPresent, routePresent, err := s.readDedicatedEgressPresence(tag)
	if err != nil {
		return DedicatedEgressResult{}, err
	}
	if outboundPresent || routePresent {
		return DedicatedEgressResult{}, fmt.Errorf("dedicated egress remove readback incomplete for tag %q", tag)
	}
	return DedicatedEgressResult{Tag: tag}, nil
}

// readDedicatedEgressPresence reads the persisted Xray template after the
// reconcile has completed. The endpoint must not report success based only on
// SaveXraySetting/RestartXray returning nil: a future Xray or storage change
// could otherwise leave a partial outbound/route while the platform records a
// successful capability probe.
func (s *XrayService) readDedicatedEgressPresence(tag string) (bool, bool, error) {
	template, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return false, false, err
	}
	return dedicatedEgressPresence(template, tag)
}

func validateDedicatedEgressSpec(spec DedicatedEgressSpec) error {
	spec.Tag = strings.TrimSpace(spec.Tag)
	spec.InboundTag = strings.TrimSpace(spec.InboundTag)
	spec.User = strings.TrimSpace(spec.User)
	spec.Address = strings.TrimSpace(spec.Address)
	if !dedicatedEgressTag.MatchString(spec.Tag) || !dedicatedEgressTag.MatchString(spec.InboundTag) {
		return errors.New("invalid dedicated egress tag")
	}
	if spec.User == "" || spec.Address == "" {
		return errors.New("dedicated egress user and address are required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return errors.New("dedicated egress port is invalid")
	}
	if spec.Username == "" || spec.Password == "" {
		return errors.New("dedicated egress credentials are required")
	}
	return nil
}

func normalizeDedicatedEgressSpec(spec DedicatedEgressSpec) DedicatedEgressSpec {
	spec.Tag = strings.TrimSpace(spec.Tag)
	spec.InboundTag = strings.TrimSpace(spec.InboundTag)
	spec.User = strings.TrimSpace(spec.User)
	spec.Address = strings.TrimSpace(spec.Address)
	spec.Username = strings.TrimSpace(spec.Username)
	spec.Password = strings.TrimSpace(spec.Password)
	return spec
}

func upsertDedicatedEgressConfig(raw string, spec DedicatedEgressSpec) (string, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return "", common.NewError("dedicated egress config is invalid:", err)
	}
	var outbounds []map[string]interface{}
	if rawOutbounds, ok := config["outbounds"]; ok && len(rawOutbounds) > 0 {
		if err := json.Unmarshal(rawOutbounds, &outbounds); err != nil {
			return "", common.NewError("dedicated egress outbounds are invalid:", err)
		}
	}
	outbounds = filterDedicatedOutbound(outbounds, spec.Tag)
	outbounds = append(outbounds, map[string]interface{}{
		"tag":      spec.Tag,
		"protocol": "socks",
		"settings": map[string]interface{}{
			"servers": []interface{}{map[string]interface{}{
				"address": spec.Address,
				"port":    spec.Port,
				"users":   []interface{}{map[string]interface{}{"user": spec.Username, "pass": spec.Password}},
			}},
		},
	})
	outboundJSON, err := json.Marshal(outbounds)
	if err != nil {
		return "", err
	}
	config["outbounds"] = outboundJSON

	routing, err := routingObject(config)
	if err != nil {
		return "", err
	}
	rules := filterDedicatedRoute(routing["rules"], spec.Tag)
	rules = append(rules, map[string]interface{}{
		"type":        "field",
		"inboundTag":  []interface{}{spec.InboundTag},
		"user":        []interface{}{spec.User},
		"outboundTag": spec.Tag,
	})
	routing["rules"] = rules
	routingJSON, err := json.Marshal(routing)
	if err != nil {
		return "", err
	}
	config["routing"] = routingJSON
	return marshalConfig(config)
}

func removeDedicatedEgressConfig(raw string, tag string) (string, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return "", common.NewError("dedicated egress config is invalid:", err)
	}
	if rawOutbounds, ok := config["outbounds"]; ok && len(rawOutbounds) > 0 {
		var outbounds []map[string]interface{}
		if err := json.Unmarshal(rawOutbounds, &outbounds); err != nil {
			return "", common.NewError("dedicated egress outbounds are invalid:", err)
		}
		outboundJSON, err := json.Marshal(filterDedicatedOutbound(outbounds, tag))
		if err != nil {
			return "", err
		}
		config["outbounds"] = outboundJSON
	}
	routing, err := routingObject(config)
	if err != nil {
		return "", err
	}
	routing["rules"] = filterDedicatedRoute(routing["rules"], tag)
	routingJSON, err := json.Marshal(routing)
	if err != nil {
		return "", err
	}
	config["routing"] = routingJSON
	return marshalConfig(config)
}

func dedicatedEgressPresence(raw string, tag string) (bool, bool, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return false, false, common.NewError("dedicated egress config is invalid:", err)
	}

	outboundPresent := false
	if rawOutbounds, ok := config["outbounds"]; ok && len(rawOutbounds) > 0 {
		var outbounds []map[string]interface{}
		if err := json.Unmarshal(rawOutbounds, &outbounds); err != nil {
			return false, false, common.NewError("dedicated egress outbounds are invalid:", err)
		}
		for _, outbound := range outbounds {
			if value, ok := outbound["tag"].(string); ok && value == tag {
				outboundPresent = true
				break
			}
		}
	}

	routing, err := routingObject(config)
	if err != nil {
		return false, false, err
	}
	routePresent := false
	if rules, ok := routing["rules"].([]interface{}); ok {
		for _, rawRule := range rules {
			rule, ok := rawRule.(map[string]interface{})
			if !ok {
				continue
			}
			if value, ok := rule["outboundTag"].(string); ok && value == tag {
				routePresent = true
				break
			}
		}
	}
	return outboundPresent, routePresent, nil
}

func dedicatedEgressObservation(raw string, spec DedicatedEgressSpec) (DedicatedEgressResult, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return DedicatedEgressResult{}, common.NewError("dedicated egress config is invalid:", err)
	}

	outboundPresent, outboundMatches, err := dedicatedOutboundMatches(config["outbounds"], spec)
	if err != nil {
		return DedicatedEgressResult{}, err
	}
	routing, err := routingObject(config)
	if err != nil {
		return DedicatedEgressResult{}, err
	}
	routePresent, routeMatches := dedicatedRouteMatches(routing["rules"], spec)
	return DedicatedEgressResult{
		Tag:             spec.Tag,
		OutboundPresent: outboundPresent,
		RoutePresent:    routePresent,
		SpecMatches:     outboundMatches && routeMatches,
	}, nil
}

func dedicatedOutboundMatches(raw json.RawMessage, spec DedicatedEgressSpec) (bool, bool, error) {
	var outbounds []map[string]interface{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &outbounds); err != nil {
			return false, false, common.NewError("dedicated egress outbounds are invalid:", err)
		}
	}
	for _, outbound := range outbounds {
		if stringValue(outbound["tag"]) != spec.Tag {
			continue
		}
		settings, _ := outbound["settings"].(map[string]interface{})
		servers, _ := settings["servers"].([]interface{})
		if len(servers) != 1 {
			return true, false, nil
		}
		server, _ := servers[0].(map[string]interface{})
		users, _ := server["users"].([]interface{})
		if len(users) != 1 {
			return true, false, nil
		}
		user, _ := users[0].(map[string]interface{})
		matches := stringValue(outbound["protocol"]) == "socks" &&
			stringValue(server["address"]) == spec.Address &&
			intValue(server["port"]) == spec.Port &&
			stringValue(user["user"]) == spec.Username &&
			stringValue(user["pass"]) == spec.Password
		return true, matches, nil
	}
	return false, false, nil
}

func dedicatedRouteMatches(raw interface{}, spec DedicatedEgressSpec) (bool, bool) {
	rules, _ := raw.([]interface{})
	for _, rawRule := range rules {
		rule, _ := rawRule.(map[string]interface{})
		if stringValue(rule["outboundTag"]) != spec.Tag {
			continue
		}
		matches := stringSliceEquals(rule["inboundTag"], []string{spec.InboundTag}) &&
			stringSliceEquals(rule["user"], []string{spec.User})
		return true, matches
	}
	return false, false
}

func stringValue(value interface{}) string {
	out, _ := value.(string)
	return out
}

func intValue(value interface{}) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func stringSliceEquals(value interface{}, expected []string) bool {
	raw, _ := value.([]interface{})
	if len(raw) != len(expected) {
		return false
	}
	for index, item := range raw {
		if stringValue(item) != expected[index] {
			return false
		}
	}
	return true
}

func routingObject(config map[string]json.RawMessage) (map[string]interface{}, error) {
	routing := map[string]interface{}{}
	if rawRouting, ok := config["routing"]; ok && len(rawRouting) > 0 {
		if err := json.Unmarshal(rawRouting, &routing); err != nil {
			return nil, common.NewError("dedicated egress routing is invalid:", err)
		}
	}
	return routing, nil
}

func filterDedicatedOutbound(outbounds []map[string]interface{}, tag string) []map[string]interface{} {
	filtered := make([]map[string]interface{}, 0, len(outbounds)+1)
	for _, outbound := range outbounds {
		if value, ok := outbound["tag"].(string); ok && value == tag {
			continue
		}
		filtered = append(filtered, outbound)
	}
	return filtered
}

func filterDedicatedRoute(raw interface{}, tag string) []map[string]interface{} {
	rules, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	filtered := make([]map[string]interface{}, 0, len(rules)+1)
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]interface{})
		if !ok {
			continue
		}
		if value, ok := rule["outboundTag"].(string); ok && value == tag {
			continue
		}
		filtered = append(filtered, rule)
	}
	return filtered
}

func marshalConfig(config map[string]json.RawMessage) (string, error) {
	output, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("marshal dedicated egress config: %w", err)
	}
	return string(output), nil
}
