package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/xtls/xray-core/infra/conf"
)

type InboundValidationError struct {
	Path    string `json:"path" example:"settings"`
	Message string `json:"message" example:"invalid inbound configuration"`
}

type InboundValidationResult struct {
	OK     bool                     `json:"ok" example:"false"`
	Errors []InboundValidationError `json:"errors"`
}

func (s *InboundService) ValidateInbound(inbound *model.Inbound) InboundValidationResult {
	result := InboundValidationResult{Errors: []InboundValidationError{}}
	add := func(path string, err error) {
		if err != nil {
			result.Errors = append(result.Errors, InboundValidationError{Path: path, Message: err.Error()})
		}
	}
	// The add endpoint uses these same field constraints and preparation steps.
	v := validator.New()
	if err := v.Struct(inbound); err != nil {
		if fields, ok := errors.AsType[validator.ValidationErrors](err); ok {
			for _, field := range fields {
				typ := reflect.TypeOf(*inbound)
				definition, _ := typ.FieldByName(field.StructField())
				path, _, _ := strings.Cut(definition.Tag.Get("json"), ",")
				add(path, fmt.Errorf("%s", field.Error()))
			}
		} else {
			add("$", err)
		}
	}
	add("$", binding.Validator.ValidateStruct(inbound))
	if inbound.NodeID != nil && *inbound.NodeID == 0 {
		inbound.NodeID = nil
	}
	add("streamSettings", s.prepareInbound(inbound))
	clients, _, err := ParseAndStripInboundDraftClients(inbound.Settings)
	add("settings", err)
	if err == nil {
		add("settings.clients", validateInboundDraftCredentials(inbound, clients))
	}
	ignoreID := 0
	if inbound.Tag != "" {
		var existing model.Inbound
		err := database.GetDB().Where("tag = ?", inbound.Tag).First(&existing).Error
		if err == nil {
			ignoreID = existing.Id
		} else if !database.IsNotFound(err) {
			add("tag", err)
		}
	}
	conflict, err := checkPortConflictTx(database.GetDB(), inbound, ignoreID)
	add("port", err)
	if conflict != nil {
		add("port", fmt.Errorf("%s", conflict.String()))
	}
	// A same-tag draft is an update; its own clients may already exist.
	existingEmails := map[string]bool{}
	if ignoreID != 0 {
		var emails []string
		err := database.GetDB().Model(&model.ClientRecord{}).Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").Where("client_inbounds.inbound_id = ?", ignoreID).Pluck("clients.email", &emails).Error
		add("settings.clients", err)
		for _, email := range emails {
			existingEmails[email] = true
		}
	}
	seen := map[string]bool{}
	for i, client := range clients {
		path := fmt.Sprintf("settings.clients[%d].email", i)
		if seen[client.Email] {
			add(path, fmt.Errorf("duplicate email %q", client.Email))
		}
		seen[client.Email] = true
		if !existingEmails[client.Email] {
			var count int64
			err := database.GetDB().Model(&model.ClientRecord{}).Where("email = ?", client.Email).Count(&count).Error
			add(path, err)
			if count > 0 {
				add(path, fmt.Errorf("duplicate email %q", client.Email))
			}
		}
	}
	raw, err := json.Marshal(inbound.GenXrayInboundConfig())
	add("$", err)
	if err == nil {
		var config conf.InboundDetourConfig
		err = json.Unmarshal(raw, &config)
		add("$", err)
		if err == nil {
			_, err = config.Build()
			add("settings", err)
		}
	}
	result.OK = len(result.Errors) == 0
	return result
}

func InboundDraftPortsConflict(a, b *model.Inbound) bool {
	sameNode := a.NodeID == nil && b.NodeID == nil || a.NodeID != nil && b.NodeID != nil && *a.NodeID == *b.NodeID
	return sameNode && a.Port == b.Port && listenOverlaps(a.Listen, b.Listen) && inboundTransports(a.Protocol, a.StreamSettings, a.Settings)&inboundTransports(b.Protocol, b.StreamSettings, b.Settings) != 0
}
