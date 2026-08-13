package database

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func createLegacyMixedInbound(t *testing.T, tag, settings string) *model.Inbound {
	t.Helper()
	inbound := &model.Inbound{
		UserId:   1,
		Port:     1080,
		Protocol: model.Mixed,
		Tag:      tag,
		Settings: settings,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("create Mixed inbound: %v", err)
	}
	return inbound
}

func TestMigrateMixedAccountsToClients(t *testing.T) {
	initWGMigrationDB(t)
	inbound := createLegacyMixedInbound(t, "mixed-legacy",
		`{"auth":"password","accounts":[{"user":"alice","pass":" secret "}],"udp":true,"ip":"127.0.0.1"}`)

	if err := migrateMixedAccountsToClients(); err != nil {
		t.Fatalf("migrateMixedAccountsToClients: %v", err)
	}

	settings := reloadInboundSettings(t, inbound.Id)
	if _, exists := settings["accounts"]; exists {
		t.Fatalf("legacy accounts must be removed: %#v", settings)
	}
	clients, ok := settings["clients"].([]any)
	if !ok || len(clients) != 1 {
		t.Fatalf("expected one unified client: %#v", settings["clients"])
	}
	client := clients[0].(map[string]any)
	if client["email"] != "alice" || client["password"] != " secret " {
		t.Fatalf("credential changed during migration: %#v", client)
	}
	var record model.ClientRecord
	if err := db.Where("email = ?", "alice").First(&record).Error; err != nil {
		t.Fatalf("client record missing: %v", err)
	}
	var links int64
	db.Model(&model.ClientInbound{}).
		Where("client_id = ? AND inbound_id = ?", record.Id, inbound.Id).
		Count(&links)
	if links != 1 {
		t.Fatalf("expected one client-inbound link, got %d", links)
	}

	if err := migrateMixedAccountsToClients(); err != nil {
		t.Fatalf("idempotent rerun: %v", err)
	}
	var records int64
	db.Model(&model.ClientRecord{}).Where("email = ?", "alice").Count(&records)
	if records != 1 {
		t.Fatalf("rerun duplicated client records: %d", records)
	}
}

func TestMigrateMixedAccountsToClientsLeavesCredentialConflictUntouched(t *testing.T) {
	initWGMigrationDB(t)
	if err := db.Create(&model.ClientRecord{Email: "alice", Password: "other", Enable: true}).Error; err != nil {
		t.Fatalf("seed conflicting client: %v", err)
	}
	inbound := createLegacyMixedInbound(t, "mixed-conflict",
		`{"auth":"password","accounts":[{"user":"alice","pass":"legacy-secret"}],"udp":false}`)

	if err := migrateMixedAccountsToClients(); err != nil {
		t.Fatalf("migration should report conflict without failing startup: %v", err)
	}
	settings := reloadInboundSettings(t, inbound.Id)
	if _, exists := settings["accounts"]; !exists {
		t.Fatalf("conflicting legacy account must remain active: %#v", settings)
	}
	if _, exists := settings["clients"]; exists {
		t.Fatalf("conflicting inbound must not be partially migrated: %#v", settings)
	}
	var links int64
	db.Model(&model.ClientInbound{}).Where("inbound_id = ?", inbound.Id).Count(&links)
	if links != 0 {
		t.Fatalf("conflicting inbound must not gain partial links: %d", links)
	}
}

func TestMigrateHTTPAccountsToClients(t *testing.T) {
	initWGMigrationDB(t)
	inbound := &model.Inbound{
		UserId:   1,
		Port:     8080,
		Protocol: model.HTTP,
		Tag:      "http-legacy",
		Settings: `{"accounts":[{"user":"alice-http","pass":"secret"}],"allowTransparent":true}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("create HTTP inbound: %v", err)
	}

	if err := migrateHTTPAccountsToClients(); err != nil {
		t.Fatalf("migrateHTTPAccountsToClients: %v", err)
	}

	settings := reloadInboundSettings(t, inbound.Id)
	if _, exists := settings["accounts"]; exists {
		t.Fatalf("legacy HTTP accounts must be removed: %#v", settings)
	}
	clients, ok := settings["clients"].([]any)
	if !ok || len(clients) != 1 {
		t.Fatalf("expected one HTTP client: %#v", settings["clients"])
	}
	client := clients[0].(map[string]any)
	if client["email"] != "alice-http" || client["password"] != "secret" {
		t.Fatalf("HTTP credential changed during migration: %#v", client)
	}
	if settings["allowTransparent"] != true {
		t.Fatalf("unrelated HTTP setting changed: %#v", settings)
	}
}
