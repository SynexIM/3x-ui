package sub

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestLinksForClientReadsNormalizedClientAuthority(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	const (
		email = "normalized-link@example.com"
		uuid  = "11111111-2222-4333-8444-555555555555"
	)
	db := database.GetDB()
	inbound := &model.Inbound{
		UserId:         1,
		Tag:            "normalized-link",
		Remark:         "normalized-link",
		Enable:         true,
		Listen:         "0.0.0.0",
		Port:           443,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	client := &model.ClientRecord{Email: email, UUID: uuid, Enable: true}
	if err := db.Create(client).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{
		ClientId:  client.Id,
		InboundId: inbound.Id,
	}).Error; err != nil {
		t.Fatalf("seed client link: %v", err)
	}

	links := NewLinkProvider().LinksForClient("line.example.com", inbound, email)
	if len(links) != 1 || !strings.Contains(links[0], uuid) {
		t.Fatalf("normalized client link = %v, want one VLESS link carrying UUID", links)
	}
}
