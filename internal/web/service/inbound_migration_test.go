package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// TestMigrationRequirementsRunsAfterClientsTableBackfill preserves the legacy
// JSON-only migration boundary: a restart explicitly runs ClientsTable before
// the normal migration work, which then must complete its MultiDomain rewrite.
func TestMigrationRequirementsRunsAfterClientsTableBackfill(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	db := database.GetDB()

	const backfillEmail = "needsbackfill@example.com"
	const uid = "ce8d33df-3a64-4f10-8f9b-91c3a8e0c010"

	// Inbound A: a client present only in settings.clients, with no client_traffics row.
	clientInbound := &model.Inbound{
		UserId:         1,
		Tag:            "a-tag",
		Enable:         true,
		Port:           30001,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[{"email":"` + backfillEmail + `","id":"` + uid + `","enable":true}]}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := db.Create(clientInbound).Error; err != nil {
		t.Fatalf("create client inbound: %v", err)
	}

	// Inbound B: a legacy MultiDomain inbound whose tag carries the 0.0.0.0: prefix.
	// Its presence makes the externalProxy query return rows, so the function does not
	// early-return and reaches the tag-cleanup statement.
	multiDomainInbound := &model.Inbound{
		UserId:         1,
		Tag:            "inbound-0.0.0.0:30002",
		Enable:         true,
		Port:           30002,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"security":"tls","tlsSettings":{"settings":{"domains":[{"domain":"example.com"}]}}}`,
	}
	if err := db.Create(multiDomainInbound).Error; err != nil {
		t.Fatalf("create multidomain inbound: %v", err)
	}

	if err := db.Where("seeder_name = ?", "ClientsTable").Delete(&model.HistoryOfSeeders{}).Error; err != nil {
		t.Fatalf("clear ClientsTable history: %v", err)
	}
	if err := database.CloseDB(); err != nil {
		t.Fatalf("close before explicit startup seeder: %v", err)
	}
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("restart for ClientsTable: %v", err)
	}
	db = database.GetDB()
	var seeded model.ClientRecord
	if err := db.Where("email = ?", backfillEmail).First(&seeded).Error; err != nil {
		t.Fatalf("ClientsTable did not backfill %s: %v", backfillEmail, err)
	}
	var links int64
	if err := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", seeded.Id, clientInbound.Id).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("ClientsTable linkage = %d, err=%v, want 1", links, err)
	}

	svc := InboundService{}
	svc.MigrationRequirements()

	// The MultiDomain→ExternalProxy migration must have committed too: the detection
	// query ran (.Scan executes it) and the loop rewrote the inbound's streamSettings.
	var refreshed model.Inbound
	if err := db.First(&refreshed, multiDomainInbound.Id).Error; err != nil {
		t.Fatalf("reload multidomain inbound: %v", err)
	}
	if !strings.Contains(refreshed.StreamSettings, "externalProxy") {
		t.Errorf("MultiDomain migration did not commit; streamSettings = %q", refreshed.StreamSettings)
	}
}

// TestMigrationRequirements_CleansLegacyZeroAddrTag guards the legacy tag cleanup that
// strips the auto-generated "0.0.0.0:" prefix. The inbound is MultiDomain TLS so the
// externalProxy detection query returns rows and the cleanup is reached (it early-returns
// at len(externalProxy)==0 otherwise). The cleanup must use tx.Exec, not tx.Raw, which
// only builds a non-SELECT statement without running it.
func TestMigrationRequirements_CleansLegacyZeroAddrTag(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	db := database.GetDB()
	legacy := &model.Inbound{
		UserId:         1,
		Tag:            "inbound-0.0.0.0:30002",
		Enable:         true,
		Port:           30002,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"security":"tls","tlsSettings":{"settings":{"domains":[{"domain":"example.com"}]}}}`,
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("create legacy inbound: %v", err)
	}

	svc := InboundService{}
	svc.MigrationRequirements()

	var got model.Inbound
	if err := db.First(&got, legacy.Id).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.Tag != "inbound-30002" {
		t.Fatalf("legacy 0.0.0.0: tag not stripped: got %q, want %q", got.Tag, "inbound-30002")
	}
}

func TestMigrationRemoveOrphanedTraffics(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	clientSvc := &ClientService{}
	inboundSvc := &InboundService{}

	const attachedEmail = "attached@example.com"
	attachedClient := model.Client{Email: attachedEmail, ID: "11111111-1111-1111-1111-111111111111", SubID: attachedEmail, Enable: true}
	attachedIb := mkInbound(t, 30003, model.VLESS, clientsSettings(t, []model.Client{attachedClient}))
	seedNormalizedInbound(t, attachedIb, []model.Client{attachedClient})
	mkTraffic(t, attachedIb.Id, attachedEmail, 0, 0, 0, 0, true)

	const detachedEmail = "detached@example.com"
	detachedClient := model.Client{Email: detachedEmail, ID: "22222222-2222-2222-2222-222222222222", SubID: detachedEmail, Enable: true}
	detachedIb := mkInbound(t, 30004, model.VLESS, clientsSettings(t, []model.Client{detachedClient}))
	seedNormalizedInbound(t, detachedIb, []model.Client{detachedClient})
	mkTraffic(t, detachedIb.Id, detachedEmail, 123, 456, 0, 0, true)
	detachedRec := lookupClientRecord(t, detachedEmail)
	if _, err := clientSvc.Detach(inboundSvc, detachedRec.Id, []int{detachedIb.Id}); err != nil {
		t.Fatalf("Detach: %v", err)
	}

	const jsonOnlyEmail = "jsononly@example.com"
	jsonOnlyClient := model.Client{Email: jsonOnlyEmail, ID: "33333333-3333-3333-3333-333333333333", SubID: jsonOnlyEmail, Enable: true}
	jsonOnlyIb := mkInbound(t, 30005, model.VLESS, clientsSettings(t, []model.Client{jsonOnlyClient}))
	mkTraffic(t, jsonOnlyIb.Id, jsonOnlyEmail, 0, 0, 0, 0, true)

	const trulyOrphanedEmail = "deleted@example.com"
	mkTraffic(t, attachedIb.Id, trulyOrphanedEmail, 0, 0, 0, 0, true)

	if err := db.Where("seeder_name = ?", "ClientsTable").Delete(&model.HistoryOfSeeders{}).Error; err != nil {
		t.Fatalf("clear ClientsTable history: %v", err)
	}
	if err := database.CloseDB(); err != nil {
		t.Fatalf("close before JSON-only seeder: %v", err)
	}
	if err := database.InitDB(filepath.Join(os.Getenv("XUI_DB_FOLDER"), "x-ui.db")); err != nil {
		t.Fatalf("restart for ClientsTable: %v", err)
	}
	db = database.GetDB()
	var seeded model.ClientRecord
	if err := db.Where("email = ?", jsonOnlyEmail).First(&seeded).Error; err != nil {
		t.Fatalf("ClientsTable did not backfill JSON-only client: %v", err)
	}
	inboundSvc.MigrationRemoveOrphanedTraffics()
	cases := []struct {
		name  string
		email string
		want  int64
	}{
		{"attached, in clients table and JSON", attachedEmail, 1},
		{"detached-but-alive, in clients table only", detachedEmail, 1},
		{"seeder-skipped-but-live, in JSON only", jsonOnlyEmail, 1},
		{"truly orphaned, in neither", trulyOrphanedEmail, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got int64
			if err := db.Model(xray.ClientTraffic{}).Where("email = ?", c.email).Count(&got).Error; err != nil {
				t.Fatalf("count client_traffics for %s: %v", c.email, err)
			}
			if got != c.want {
				t.Errorf("client_traffics count for %s: got %d, want %d", c.email, got, c.want)
			}
		})
	}
}

func TestMigrationRequirements_NormalizesShareAddressFields(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()

	invalidStrategy := &model.Inbound{
		UserId:         1,
		Tag:            "invalid-share-strategy",
		Enable:         true,
		Port:           31001,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	paddedStrategy := &model.Inbound{
		UserId:         1,
		Tag:            "padded-share-strategy",
		Enable:         true,
		Port:           31002,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	invalidAddress := &model.Inbound{
		UserId:         1,
		Tag:            "invalid-share-address",
		Enable:         true,
		Port:           31003,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := db.Create(invalidStrategy).Error; err != nil {
		t.Fatalf("create invalid strategy inbound: %v", err)
	}
	if err := db.Create(paddedStrategy).Error; err != nil {
		t.Fatalf("create padded strategy inbound: %v", err)
	}
	if err := db.Create(invalidAddress).Error; err != nil {
		t.Fatalf("create invalid address inbound: %v", err)
	}
	if err := db.Model(&model.Inbound{}).Where("id = ?", invalidStrategy.Id).Updates(map[string]any{
		"share_addr_strategy": " auto ",
		"share_addr":          "  edge.example.com  ",
	}).Error; err != nil {
		t.Fatalf("seed invalid share fields: %v", err)
	}
	if err := db.Model(&model.Inbound{}).Where("id = ?", paddedStrategy.Id).Updates(map[string]any{
		"share_addr_strategy": " listen ",
		"share_addr":          "  10.0.0.1  ",
	}).Error; err != nil {
		t.Fatalf("seed padded share fields: %v", err)
	}
	if err := db.Model(&model.Inbound{}).Where("id = ?", invalidAddress.Id).Updates(map[string]any{
		"share_addr_strategy": "custom",
		"share_addr":          "edge.example.com:8443",
	}).Error; err != nil {
		t.Fatalf("seed invalid address share fields: %v", err)
	}

	svc := InboundService{}
	svc.MigrationRequirements()

	var gotInvalid model.Inbound
	if err := db.First(&gotInvalid, invalidStrategy.Id).Error; err != nil {
		t.Fatalf("reload invalid strategy inbound: %v", err)
	}
	if gotInvalid.ShareAddrStrategy != "node" || gotInvalid.ShareAddr != "edge.example.com" {
		t.Fatalf("invalid share fields = (%q, %q), want (node, edge.example.com)", gotInvalid.ShareAddrStrategy, gotInvalid.ShareAddr)
	}

	var gotPadded model.Inbound
	if err := db.First(&gotPadded, paddedStrategy.Id).Error; err != nil {
		t.Fatalf("reload padded strategy inbound: %v", err)
	}
	if gotPadded.ShareAddrStrategy != "listen" || gotPadded.ShareAddr != "10.0.0.1" {
		t.Fatalf("padded share fields = (%q, %q), want (listen, 10.0.0.1)", gotPadded.ShareAddrStrategy, gotPadded.ShareAddr)
	}

	var gotInvalidAddress model.Inbound
	if err := db.First(&gotInvalidAddress, invalidAddress.Id).Error; err != nil {
		t.Fatalf("reload invalid address inbound: %v", err)
	}
	if gotInvalidAddress.ShareAddrStrategy != "node" || gotInvalidAddress.ShareAddr != "" {
		t.Fatalf("invalid address share fields = (%q, %q), want (node, empty)", gotInvalidAddress.ShareAddrStrategy, gotInvalidAddress.ShareAddr)
	}
}
