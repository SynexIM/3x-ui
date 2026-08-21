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

var (
	ErrDeclarativeRevisionConflict = errors.New("declarative config revision conflicts with the applied revision")
	declarativeProvisioningLock    sync.Mutex
)

type DeclarativeClient struct {
	Email     string  `json:"email"`
	UUID      string  `json:"uuid"`
	Password  *string `json:"password"`
	LimitMbps int     `json:"limitMbps"`
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
	Inbounds  []DeclarativeInbound  `json:"inbounds"`
	Outbounds []DeclarativeOutbound `json:"outbounds"`
	Routing   struct {
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
	AppliedRevision int               `json:"appliedRevision"`
	HotApplied      bool              `json:"hotApplied"`
	Restarted       bool              `json:"restarted"`
	Counts          DeclarativeCounts `json:"counts"`
}

type DeclarativePanelStatus struct {
	Version         string            `json:"version"`
	Healthy         bool              `json:"healthy"`
	CapacityLines   int               `json:"capacityLines"`
	ActiveClients   int               `json:"activeClients"`
	AppliedRevision int               `json:"appliedRevision"`
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
			return receiptFor(request, false, false), nil
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
	if err := s.XrayService.RestartXray(false); err != nil {
		_ = s.XraySettingService.SaveXraySetting(previousTemplate)
		_ = s.XrayService.RestartXray(true)
		return nil, fmt.Errorf("apply declarative xray config: %w", err)
	}
	state := persistedDeclarativeState{Request: *request, Hash: hash}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if err := s.SettingService.saveSetting(declarativeProvisioningStateKey, string(encoded)); err != nil {
		return nil, err
	}
	return receiptFor(request, !request.RequiresRestart, request.RequiresRestart), nil
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
	status.AppliedRevision = state.Request.Revision
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
		panelInbound, err := modelInboundFor(inbound)
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

	inbounds := make([]any, 0, len(config.Inbounds))
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
	return string(encoded), nil
}

func modelInboundFor(inbound DeclarativeInbound) (*model.Inbound, error) {
	settings := cloneMap(inbound.Settings)
	clients := make([]any, 0, len(inbound.Clients))
	for _, client := range inbound.Clients {
		entry := map[string]any{
			"email":         client.Email,
			"id":            client.UUID,
			"enable":        true,
			"bandwidth_bps": uint64(client.LimitMbps) * 1_000_000,
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
		}
		clients = append(clients, entry)
	}
	settings["clients"] = clients
	if inbound.Protocol == "vless" {
		settings["decryption"] = "none"
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
		default:
			return fmt.Errorf("unsupported inbound protocol %q", inbound.Protocol)
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

func receiptFor(request *DeclarativeApplyRequest, hotApplied, restarted bool) *DeclarativeApplyReceipt {
	return &DeclarativeApplyReceipt{
		AppliedRevision: request.Revision,
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
