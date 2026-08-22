package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const declarativeProvisioningStateKey = "ipveloDeclarativeProvisioningState"

// hysteriaConfigVersion is the only version xray-core builds — both
// conf.HysteriaServerConfig (the protocol half) and conf.HysteriaConfig (the
// transport half) answer anything else with "version != 2", and that error
// rejects the whole config, taking every other inbound on the node down with it.
const hysteriaConfigVersion = 2

var (
	ErrDeclarativeRevisionConflict = errors.New("declarative config revision conflicts with the applied revision")
	// ErrDeclarativelyManaged is returned to a panel-side inbound write while a
	// control plane owns this node's configuration.
	ErrDeclarativelyManaged     = errors.New("this node's inbounds are managed declaratively by the control plane and are read-only in the panel")
	declarativeProvisioningLock sync.Mutex
)

// IsDeclarativelyManaged reports whether a control plane has applied a
// declarative configuration to this panel.
//
// While it has, the template is the authority on local inbounds and the panel
// must not write the inbounds table: GetXrayConfig concatenates the two, so a
// panel-added inbound silently joins a configuration the control plane believes
// it fully describes — it never appears in the applied revision, it survives
// every subsequent apply, and its ports are outside what the control plane
// tracks. Node inbounds (NodeID != nil) belong to remote panels and are not
// part of the local core's config, so they stay editable.
func IsDeclarativelyManaged() bool {
	setting, err := (&SettingService{}).getSetting(declarativeProvisioningStateKey)
	return err == nil && setting != nil && strings.TrimSpace(setting.Value) != ""
}

type DeclarativeClient struct {
	Email     string  `json:"email"`
	UUID      string  `json:"uuid"`
	Password  *string `json:"password"`
	PirBps    uint64  `json:"pirBps"`
	CirBps    uint64  `json:"cirBps"`
	CbsBytes  uint64  `json:"cbsBytes"`
	ConnLimit uint32  `json:"connLimit"`
}

type DeclarativeShareAddress struct {
	Strategy string `json:"strategy"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

type DeclarativeInbound struct {
	Tag            string                  `json:"tag"`
	Protocol       string                  `json:"protocol"`
	ListenPort     int                     `json:"listenPort"`
	ShareAddr      DeclarativeShareAddress `json:"shareAddr"`
	Settings       map[string]any          `json:"settings"`
	StreamSettings map[string]any          `json:"streamSettings"`
	Clients        []DeclarativeClient     `json:"clients"`
}

type DeclarativeSocksServer struct {
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username *string `json:"username"`
	Password *string `json:"password"`
}

type DeclarativeOutbound struct {
	Tag      string                 `json:"tag"`
	Protocol string                 `json:"protocol"`
	Server   DeclarativeSocksServer `json:"server"`
}

type DeclarativeRule struct {
	AccountEmail string `json:"accountEmail"`
	OutboundTag  string `json:"outboundTag"`
}

type DeclarativeNodeConfig struct {
	NodeBandwidthBps uint64                `json:"nodeBandwidthBps"`
	Inbounds         []DeclarativeInbound  `json:"inbounds"`
	Outbounds        []DeclarativeOutbound `json:"outbounds"`
	Routing          struct {
		Rules []DeclarativeRule `json:"rules"`
	} `json:"routing"`
}

type DeclarativeApplyRequest struct {
	Revision        int                   `json:"revision"`
	RequiresRestart bool                  `json:"requiresRestart"`
	Config          DeclarativeNodeConfig `json:"config"`
}

type DeclarativeCounts struct {
	Inbounds  int `json:"inbounds"`
	Clients   int `json:"clients"`
	Outbounds int `json:"outbounds"`
	Rules     int `json:"rules"`
}

type DeclarativeApplyReceipt struct {
	AppliedRevision int `json:"appliedRevision"`
	// ConfigHash is the identity of the configuration now applied. The control
	// plane sends it back as the baseHash of its next delta, so it never has to
	// reproduce the panel's hashing to address the state it is building on.
	ConfigHash string            `json:"configHash"`
	HotApplied bool              `json:"hotApplied"`
	Restarted  bool              `json:"restarted"`
	Counts     DeclarativeCounts `json:"counts"`
}

type DeclarativePanelStatus struct {
	Version         string            `json:"version"`
	Healthy         bool              `json:"healthy"`
	CapacityLines   int               `json:"capacityLines"`
	ActiveClients   int               `json:"activeClients"`
	AppliedRevision int               `json:"appliedRevision"`
	ConfigHash      string            `json:"configHash"`
	Counts          DeclarativeCounts `json:"counts"`
}

type DeclarativeConnection struct {
	Protocol string  `json:"protocol"`
	Label    string  `json:"label"`
	URI      string  `json:"uri"`
	QRData   *string `json:"qrDataUrl"`
}

type DeclarativeDelivery struct {
	AccountEmail string                  `json:"accountEmail"`
	Connections  []DeclarativeConnection `json:"connections"`
}

type persistedDeclarativeState struct {
	Request DeclarativeApplyRequest `json:"request"`
	Hash    string                  `json:"hash"`
}

type DeclarativeProvisioningService struct {
	SettingService     SettingService
	XraySettingService XraySettingService
	XrayService        XrayService
}

func (s *DeclarativeProvisioningService) Apply(request *DeclarativeApplyRequest) (*DeclarativeApplyReceipt, error) {
	declarativeProvisioningLock.Lock()
	defer declarativeProvisioningLock.Unlock()

	return s.apply(request)
}

// apply is the whole apply path, minus the lock. ApplyDelta holds the same lock
// across reading the applied state and applying the state it derived from it.
func (s *DeclarativeProvisioningService) apply(request *DeclarativeApplyRequest) (*DeclarativeApplyReceipt, error) {
	if err := validateDeclarativeRequest(request); err != nil {
		return nil, err
	}
	hash, err := hashDeclarativeConfig(request.Config)
	if err != nil {
		return nil, err
	}
	current, err := s.loadState()
	if err != nil {
		return nil, err
	}
	if current != nil {
		if request.Revision == current.Request.Revision && current.Hash != hash {
			return nil, ErrDeclarativeRevisionConflict
		}
		if request.Revision == current.Request.Revision {
			if err := s.XrayService.SetNodeBandwidth(request.Config.NodeBandwidthBps); err != nil {
				return nil, fmt.Errorf("reconcile node bandwidth: %w", err)
			}
			return receiptFor(request, hash, false, false), nil
		}
	}

	template, err := s.buildTemplate(request.Config)
	if err != nil {
		return nil, err
	}
	previousTemplate, err := s.SettingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}
	if err := s.XraySettingService.SaveXraySetting(template); err != nil {
		return nil, err
	}
	if err := s.XrayService.RestartXray(request.RequiresRestart); err != nil {
		return nil, s.rollback(previousTemplate, current, fmt.Errorf("apply declarative xray config: %w", err))
	}
	if err := s.XrayService.SetNodeBandwidth(request.Config.NodeBandwidthBps); err != nil {
		return nil, s.rollback(previousTemplate, current, fmt.Errorf("apply node bandwidth: %w", err))
	}
	state := persistedDeclarativeState{Request: *request, Hash: hash}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if err := s.SettingService.saveSetting(declarativeProvisioningStateKey, string(encoded)); err != nil {
		return nil, err
	}
	return receiptFor(request, hash, !request.RequiresRestart, request.RequiresRestart), nil
}

// rollback puts the previously applied configuration back and returns cause —
// joined with whatever went wrong on the way back.
//
// A rollback that fails quietly is worse than the failure it is undoing: the
// node keeps the rejected template, and the control plane hears only about the
// original problem, so neither side knows this node needs a human. The node
// bandwidth is re-pushed too, because the rollback restart drops it.
func (s *DeclarativeProvisioningService) rollback(previousTemplate string, previous *persistedDeclarativeState, cause error) error {
	errs := []error{cause}
	if err := s.XraySettingService.SaveXraySetting(previousTemplate); err != nil {
		errs = append(errs, fmt.Errorf("rollback: restoring the previous template failed: %w", err))
	}
	if err := s.XrayService.RestartXray(true); err != nil {
		errs = append(errs, fmt.Errorf("rollback: restarting on the previous template failed: %w", err))
	}
	if previous != nil {
		if err := s.XrayService.SetNodeBandwidth(previous.Request.Config.NodeBandwidthBps); err != nil {
			errs = append(errs, fmt.Errorf("rollback: restoring node bandwidth failed: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (s *DeclarativeProvisioningService) Status() (*DeclarativePanelStatus, error) {
	state, err := s.loadState()
	if err != nil {
		return nil, err
	}
	status := &DeclarativePanelStatus{
		Version:       s.XrayService.GetXrayVersion(),
		Healthy:       s.XrayService.IsXrayRunning(),
		CapacityLines: measuredCapacityLines(),
	}
	if state == nil {
		return status, nil
	}
	if status.Healthy {
		if err := s.XrayService.SetNodeBandwidth(state.Request.Config.NodeBandwidthBps); err != nil {
			return nil, fmt.Errorf("reconcile node bandwidth: %w", err)
		}
	}
	status.AppliedRevision = state.Request.Revision
	status.ConfigHash = state.Hash
	status.Counts = countsFor(state.Request.Config)
	status.ActiveClients = status.Counts.Clients
	return status, nil
}

func (s *DeclarativeProvisioningService) DeliveryInbounds(email string) ([]*model.Inbound, error) {
	state, err := s.loadState()
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errors.New("no declarative config has been applied")
	}
	inbounds := make([]*model.Inbound, 0, len(state.Request.Config.Inbounds))
	for _, inbound := range state.Request.Config.Inbounds {
		if !containsDeclarativeClient(inbound.Clients, email) {
			continue
		}
		panelInbound, err := deliveryInboundFor(inbound)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, panelInbound)
	}
	if len(inbounds) == 0 {
		return nil, errors.New("account is not present in the applied declarative config")
	}
	return inbounds, nil
}

func (s *DeclarativeProvisioningService) loadState() (*persistedDeclarativeState, error) {
	setting, err := s.SettingService.getSetting(declarativeProvisioningStateKey)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "record not found") {
			return nil, nil
		}
		return nil, err
	}
	state := &persistedDeclarativeState{}
	if err := json.Unmarshal([]byte(setting.Value), state); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *DeclarativeProvisioningService) buildTemplate(config DeclarativeNodeConfig) (string, error) {
	rawDefault, err := s.SettingService.GetDefaultXrayConfig()
	if err != nil {
		return "", err
	}
	template, ok := rawDefault.(map[string]any)
	if !ok {
		return "", errors.New("default xray config is not an object")
	}

	inbounds := make([]any, 0, len(config.Inbounds)+1)
	if defaults, ok := template["inbounds"].([]any); ok {
		for _, candidate := range defaults {
			inbound, ok := candidate.(map[string]any)
			if ok && inbound["tag"] == "api" {
				inbounds = append(inbounds, inbound)
				break
			}
		}
	}
	for _, inbound := range config.Inbounds {
		panelInbound, err := modelInboundFor(inbound)
		if err != nil {
			return "", err
		}
		inbounds = append(inbounds, panelInbound.GenXrayInboundConfig())
	}
	template["inbounds"] = inbounds

	outbounds := defaultFreedomOutbounds(template["outbounds"])
	for _, outbound := range config.Outbounds {
		server := map[string]any{
			"address": outbound.Server.Host,
			"port":    outbound.Server.Port,
		}
		if outbound.Server.Username != nil && outbound.Server.Password != nil {
			server["users"] = []any{map[string]any{
				"user": *outbound.Server.Username,
				"pass": *outbound.Server.Password,
			}}
		}
		outbounds = append(outbounds, map[string]any{
			"tag":      outbound.Tag,
			"protocol": "socks",
			"settings": map[string]any{"servers": []any{server}},
		})
	}
	template["outbounds"] = outbounds

	rules := make([]any, 0, len(config.Routing.Rules))
	for _, rule := range config.Routing.Rules {
		rules = append(rules, map[string]any{
			"type":        "field",
			"user":        []string{rule.AccountEmail},
			"outboundTag": rule.OutboundTag,
		})
	}
	routing := map[string]any{"domainStrategy": "AsIs", "rules": rules}
	template["routing"] = routing
	encoded, err := json.Marshal(template)
	if err != nil {
		return "", err
	}
	return EnsureStatsRouting(string(encoded))
}

func modelInboundFor(inbound DeclarativeInbound) (*model.Inbound, error) {
	settings := cloneMap(inbound.Settings)
	clients := make([]any, 0, len(inbound.Clients))
	for _, client := range inbound.Clients {
		entry := map[string]any{
			"email":                 client.Email,
			"id":                    client.UUID,
			"enable":                true,
			"bandwidth_bps":         client.PirBps,
			"committed_bps":         client.CirBps,
			"committed_burst_bytes": client.CbsBytes,
			"conn_limit":            client.ConnLimit,
		}
		if client.Password != nil {
			entry["password"] = *client.Password
		}
		switch inbound.Protocol {
		case "vmess":
			entry["security"] = "auto"
		case "vless":
			entry["flow"] = "xtls-rprx-vision"
		case "trojan", "shadowsocks", "mixed":
			if client.Password == nil || *client.Password == "" {
				return nil, fmt.Errorf("%s client %q requires a password", inbound.Protocol, client.Email)
			}
		case "hysteria":
			// Hysteria2 has no UUID: the account is the "auth" token, and
			// conf.HysteriaUserConfig reads it from that key alone. The shared
			// line password is that token, so one identity still spans all five
			// inbounds.
			if client.Password == nil || *client.Password == "" {
				return nil, fmt.Errorf("hysteria client %q requires a password to use as its auth token", client.Email)
			}
			entry["auth"] = *client.Password
		}
		clients = append(clients, entry)
	}
	settings["clients"] = clients
	if inbound.Protocol == "vless" {
		settings["decryption"] = "none"
	}
	if inbound.Protocol == "hysteria" {
		settings["version"] = hysteriaConfigVersion
	}
	stream := cloneMap(inbound.StreamSettings)
	if reality, ok := stream["reality"].(map[string]any); ok {
		delete(stream, "reality")
		stream["security"] = "reality"
		serverName := firstNonEmptyString(reality, "serverName", "server_name", "sni")
		realitySettings := cloneMap(reality)
		if serverName != "" {
			realitySettings["serverNames"] = []string{serverName}
			if _, exists := realitySettings["target"]; !exists {
				realitySettings["target"] = serverName + ":443"
			}
		}
		if shortID := firstNonEmptyString(reality, "shortId", "short_id"); shortID != "" {
			realitySettings["shortIds"] = []string{shortID}
		}
		stream["realitySettings"] = realitySettings
	}
	if inbound.Protocol == "hysteria" {
		// Two things xray-core does not tolerate and the control plane should
		// not have to remember. The transport has to be hysteria — over any
		// other network the config still builds, so nothing complains, and the
		// listener is simply not a hysteria one. And hysteriaSettings has to be
		// present: transport/internet/hysteria.Listen type-asserts
		// streamSettings.ProtocolSettings.(*Config), which panics when the
		// section is absent. Version 2 is the only value Build() accepts.
		stream["network"] = "hysteria"
		hysteriaSettings := cloneMap(mapValue(stream, "hysteriaSettings"))
		hysteriaSettings["version"] = hysteriaConfigVersion
		stream["hysteriaSettings"] = hysteriaSettings
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	streamJSON, err := json.Marshal(stream)
	if err != nil {
		return nil, err
	}
	return &model.Inbound{
		Remark:            inbound.Tag,
		Enable:            true,
		Listen:            "0.0.0.0",
		Port:              inbound.ListenPort,
		Protocol:          model.Protocol(inbound.Protocol),
		Settings:          string(settingsJSON),
		StreamSettings:    string(streamJSON),
		Tag:               inbound.Tag,
		ShareAddrStrategy: "custom",
		ShareAddr:         inbound.ShareAddr.Host,
	}, nil
}

func deliveryInboundFor(inbound DeclarativeInbound) (*model.Inbound, error) {
	panelInbound, err := modelInboundFor(inbound)
	if err != nil {
		return nil, err
	}
	panelInbound.Port = inbound.ShareAddr.Port
	return panelInbound, nil
}

func validateDeclarativeRequest(request *DeclarativeApplyRequest) error {
	if request == nil || request.Revision <= 0 {
		return errors.New("revision must be a positive integer")
	}
	tags := map[string]bool{}
	ports := map[int]bool{}
	outboundTags := map[string]bool{}
	accounts := map[string]bool{}
	for _, inbound := range request.Config.Inbounds {
		if inbound.Tag == "" || tags[inbound.Tag] {
			return errors.New("inbound tags must be non-empty and unique")
		}
		if inbound.ListenPort <= 0 || inbound.ListenPort > 65535 || ports[inbound.ListenPort] {
			return errors.New("inbound listen ports must be valid and unique")
		}
		if inbound.ShareAddr.Strategy != "custom" || inbound.ShareAddr.Host == "" ||
			inbound.ShareAddr.Port <= 0 || inbound.ShareAddr.Port > 65535 {
			return errors.New("each inbound requires a valid custom share address")
		}
		switch inbound.Protocol {
		case "vless", "vmess", "mixed", "shadowsocks", "trojan":
		case "hysteria":
			// A hysteria inbound without TLS builds fine and then fails to
			// listen ("tls config is nil" in hysteria.Listen), which costs a
			// restart and a rollback to discover. QUIC has no unencrypted mode
			// to fall back to, so refuse it here instead.
			if security, _ := inbound.StreamSettings["security"].(string); !strings.EqualFold(security, "tls") {
				return fmt.Errorf("hysteria inbound %q must use tls; hysteria has no unencrypted mode", inbound.Tag)
			}
			if _, ok := inbound.StreamSettings["tlsSettings"].(map[string]any); !ok {
				return fmt.Errorf("hysteria inbound %q needs tlsSettings with a certificate", inbound.Tag)
			}
		default:
			return fmt.Errorf("unsupported inbound protocol %q", inbound.Protocol)
		}
		for _, client := range inbound.Clients {
			if client.Email == "" || client.UUID == "" || client.PirBps == 0 {
				return errors.New("each client requires email, uuid and pirBps")
			}
			if client.CirBps > client.PirBps {
				return errors.New("client cirBps must not exceed pirBps")
			}
			if client.CbsBytes > 0 && client.CirBps == 0 {
				return errors.New("client cbsBytes requires cirBps")
			}
		}
		tags[inbound.Tag] = true
		ports[inbound.ListenPort] = true
	}
	for _, outbound := range request.Config.Outbounds {
		if outbound.Tag == "" || outboundTags[outbound.Tag] || outbound.Protocol != "socks" {
			return errors.New("outbound tags must be unique and use socks")
		}
		if outbound.Server.Host == "" || outbound.Server.Port <= 0 || outbound.Server.Port > 65535 {
			return errors.New("each outbound requires a valid socks server")
		}
		outboundTags[outbound.Tag] = true
	}
	for _, rule := range request.Config.Routing.Rules {
		if rule.AccountEmail == "" || accounts[rule.AccountEmail] || !outboundTags[rule.OutboundTag] {
			return errors.New("routing accounts must be unique and target a declared outbound")
		}
		accounts[rule.AccountEmail] = true
	}
	return nil
}

func hashDeclarativeConfig(config DeclarativeNodeConfig) (string, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func receiptFor(request *DeclarativeApplyRequest, hash string, hotApplied, restarted bool) *DeclarativeApplyReceipt {
	return &DeclarativeApplyReceipt{
		AppliedRevision: request.Revision,
		ConfigHash:      hash,
		HotApplied:      hotApplied,
		Restarted:       restarted,
		Counts:          countsFor(request.Config),
	}
}

func countsFor(config DeclarativeNodeConfig) DeclarativeCounts {
	clients := map[string]bool{}
	for _, inbound := range config.Inbounds {
		for _, client := range inbound.Clients {
			clients[client.Email] = true
		}
	}
	return DeclarativeCounts{
		Inbounds:  len(config.Inbounds),
		Clients:   len(clients),
		Outbounds: len(config.Outbounds),
		Rules:     len(config.Routing.Rules),
	}
}

func measuredCapacityLines() int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("IPVELO_MEASURED_CAPACITY_LINES")))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func defaultFreedomOutbounds(raw any) []any {
	items, ok := raw.([]any)
	if !ok {
		return []any{map[string]any{"tag": "direct", "protocol": "freedom"}}
	}
	for _, item := range items {
		outbound, ok := item.(map[string]any)
		if ok && outbound["protocol"] == "freedom" {
			return []any{outbound}
		}
	}
	return []any{map[string]any{"tag": "direct", "protocol": "freedom"}}
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// mapValue returns the nested object at key, or an empty one when it is absent
// or is not an object.
func mapValue(input map[string]any, key string) map[string]any {
	if value, ok := input[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

func firstNonEmptyString(input map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func containsDeclarativeClient(clients []DeclarativeClient, email string) bool {
	for _, client := range clients {
		if client.Email == email {
			return true
		}
	}
	return false
}
