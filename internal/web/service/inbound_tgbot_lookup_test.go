package service

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// TestGetClientTrafficTgBotSettingsSerializationStyles keeps compact and
// indented legacy settings as protocol fixtures while explicitly seeding
// normalized client rows. Lookup must rely on those rows, not JSON formatting.
func TestGetClientTrafficTgBotSettingsSerializationStyles(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	db := database.GetDB()

	const tgId int64 = 123456789
	cases := []struct {
		name     string
		settings string
		email    string
		port     int
	}{
		{"compact", `{"clients":[{"id":"u1","email":"compact-user","tgId":123456789}]}`, "compact-user", 41001},
		{"spaced", `{"clients": [{"id": "u2", "email": "spaced-user", "tgId": 123456789}]}`, "spaced-user", 41002},
	}
	for _, c := range cases {
		inbound := &model.Inbound{UserId: 1, Tag: "tg-" + c.name, Enable: true, Port: c.port, Protocol: model.VLESS, Settings: c.settings}
		if err := db.Create(inbound).Error; err != nil {
			t.Fatalf("create %s inbound: %v", c.name, err)
		}
		seedNormalizedInbound(t, inbound, []model.Client{{Email: c.email, ID: "u" + c.name, TgID: tgId, Enable: true}})
	}

	svc := InboundService{}
	traffics, err := svc.GetClientTrafficTgBot(tgId)
	if err != nil {
		t.Fatalf("GetClientTrafficTgBot: %v", err)
	}
	got := make(map[string]bool, len(traffics))
	for _, tr := range traffics {
		got[tr.Email] = true
	}
	if len(traffics) != 2 || !got["compact-user"] || !got["spaced-user"] {
		t.Fatalf("expected traffic for compact-user and spaced-user, got %v", got)
	}

	other, err := svc.GetClientTrafficTgBot(42)
	if err != nil {
		t.Fatalf("GetClientTrafficTgBot(42): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("expected no traffic for unknown tgId, got %d rows", len(other))
	}
}
