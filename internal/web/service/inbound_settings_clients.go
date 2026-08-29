package service

import (
	"encoding/json"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// ParseInboundDraftClients decodes clients supplied by an inbound request draft.
// Persisted inbounds must use GetAttachedClients instead.
func ParseInboundDraftClients(settings string) ([]model.Client, error) {
	trimmed := strings.TrimSpace(settings)
	if trimmed == "" || trimmed == "null" {
		return nil, common.NewError("inbound settings is empty")
	}

	var payload struct {
		Clients json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, err
	}
	if len(payload.Clients) == 0 || string(payload.Clients) == "null" {
		return nil, nil
	}

	var clients []model.Client
	if err := json.Unmarshal(payload.Clients, &clients); err != nil {
		return nil, err
	}
	return clients, nil
}

// inboundDraftHasClientMembership distinguishes an omitted membership field
// (ordinary protocol-only edit: keep normalized clients) from an explicit
// empty clients/peers array (replace membership with empty).
func inboundDraftHasClientMembership(settings string) (bool, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settings), &payload); err != nil {
		return false, err
	}
	_, hasClients := payload["clients"]
	_, hasPeers := payload["peers"]
	return hasClients || hasPeers, nil
}

// ParseAndStripInboundDraftClients separates request-only membership from the
// protocol settings persisted on an inbound. Client rows and WireGuard peers
// are rebuilt from normalized records only when a runtime configuration is
// materialized.
func ParseAndStripInboundDraftClients(settings string) ([]model.Client, string, error) {
	trimmed := strings.TrimSpace(settings)
	if trimmed == "" || trimmed == "null" {
		return nil, "", common.NewError("inbound settings is empty")
	}

	clients, err := ParseInboundDraftClients(trimmed)
	if err != nil {
		return nil, "", err
	}
	var protocolSettings map[string]any
	if err := json.Unmarshal([]byte(trimmed), &protocolSettings); err != nil {
		return nil, "", err
	}
	delete(protocolSettings, "clients")
	delete(protocolSettings, "peers")
	persisted, err := json.MarshalIndent(protocolSettings, "", "  ")
	if err != nil {
		return nil, "", err
	}
	return clients, string(persisted), nil
}

// injectNormalizedClients creates a runtime-only settings clone. Callers must
// never assign the result to an inbound row: client membership is persisted in
// clients/client_inbounds, while Xray still needs protocol-shaped users.
func injectNormalizedClients(settings map[string]any, protocol model.Protocol, clients []model.Client) error {
	entries := make([]any, 0, len(clients))
	peers := make([]any, 0, len(clients))
	for i := range clients {
		if protocol == model.WireGuard {
			peers = append(peers, model.WireguardPeerFromClient(clients[i]))
			continue
		}
		if protocol == model.Mixed || protocol == model.HTTP {
			entries = append(entries, map[string]any{"user": clients[i].Email, "pass": clients[i].Password})
			continue
		}
		encoded, err := json.Marshal(clients[i])
		if err != nil {
			return err
		}
		var entry map[string]any
		if err := json.Unmarshal(encoded, &entry); err != nil {
			return err
		}
		entries = append(entries, entry)
	}
	delete(settings, "clients")
	delete(settings, "peers")
	delete(settings, "accounts")
	switch protocol {
	case model.WireGuard:
		settings["peers"] = peers
	case model.Mixed, model.HTTP:
		settings["accounts"] = entries
	default:
		settings["clients"] = entries
	}
	return nil
}
