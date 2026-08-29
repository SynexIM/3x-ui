package service

import (
	"encoding/json"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func (s *InboundService) MigrationRemoveOrphanedTraffics() {
	result := database.GetDB().Exec("DELETE FROM client_traffics WHERE email NOT IN (SELECT email FROM clients)")
	if result.Error != nil {
		logger.Warning("MigrationRemoveOrphanedTraffics failed:", result.Error)
		return
	}
	if result.RowsAffected > 0 {
		logger.Infof("MigrationRemoveOrphanedTraffics: removed %d orphaned client_traffics row(s)", result.RowsAffected)
	}
}

func (s *InboundService) MigrationRequirements() {
	db := database.GetDB()
	tx := db.Begin()
	var err error
	defer func() {
		if err == nil {
			tx.Commit()
			if !database.IsPostgres() {
				if dbErr := db.Exec(`VACUUM "main"`).Error; dbErr != nil {
					logger.Warningf("VACUUM failed: %v", dbErr)
				}
			}
		} else {
			tx.Rollback()
		}
	}()

	if tx.Migrator().HasColumn(&model.Inbound{}, "all_time") {
		if err = tx.Migrator().DropColumn(&model.Inbound{}, "all_time"); err != nil {
			return
		}
	}
	if tx.Migrator().HasColumn(&xray.ClientTraffic{}, "all_time") {
		if err = tx.Migrator().DropColumn(&xray.ClientTraffic{}, "all_time"); err != nil {
			return
		}
	}
	if err = normalizeInboundShareAddressColumns(tx); err != nil {
		return
	}

	// Normalize "enable" columns to boolean on Postgres. Legacy SQLite data
	// (0/1 integers), partial migrations, or mixed write paths (public API
	// inbound updates that flow through UpdateClientStat + client syncs, plus
	// node traffic merge deltas) can leave the column as integer or with mixed
	// interpretation. This (combined with the dialect-aware
	// ClientTrafficEnableMergeExpr) prevents type problems in the node traffic
	// sync merge (SetRemoteTraffic) and makes the sync robust even when
	// inbounds are updated via the public API (incl. ones carrying
	// externalProxy in streamSettings). The same expression is also safe on
	// SQLite (no PG :: casts).
	if database.IsPostgres() {
		// Use DO block so it is idempotent and doesn't fail if already boolean.
		normalizeBool := func(table, col string) {
			tx.Exec(fmt.Sprintf(`
				DO $$
				BEGIN
					IF EXISTS (
						SELECT 1 FROM information_schema.columns
						WHERE table_name = '%s' AND column_name = '%s'
						  AND data_type <> 'boolean'
					) THEN
						ALTER TABLE %s ALTER COLUMN %s
							TYPE boolean USING (CASE WHEN %s::text IN ('1','true','t','yes') THEN true ELSE false END);
					END IF;
				END $$;`, table, col, table, col, col))
		}
		normalizeBool("inbounds", "enable")
		normalizeBool("client_traffics", "enable")
		normalizeBool("nodes", "enable")
		normalizeBool("clients", "enable")
		normalizeBool("api_tokens", "enabled")
		normalizeBool("outbound_subscriptions", "enabled")
	}

	// Client membership was moved to clients/client_inbounds by the one-shot
	// database seeder (ClientsTable). Do not rescan and rewrite every inbound
	// settings blob at startup: once seeded, blobs are protocol settings only.
	// Remove orphaned traffics
	tx.Where("inbound_id = 0").Delete(xray.ClientTraffic{})

	// Migrate old MultiDomain to External Proxy
	var externalProxy []struct {
		Id             int
		Port           int
		StreamSettings string // text column on both DBs; safer than []byte for cross-DB scan
	}
	externalProxyQuery := `select id, port, stream_settings
	from inbounds
	WHERE protocol in ('vmess','vless','trojan')
	  AND json_extract(stream_settings, '$.security') = 'tls'
	  AND json_extract(stream_settings, '$.tlsSettings.settings.domains') IS NOT NULL`
	if database.IsPostgres() {
		externalProxyQuery = `select id, port, stream_settings
	from inbounds
	WHERE protocol in ('vmess','vless','trojan')
	  AND NULLIF(stream_settings, '')::jsonb #>> '{security}' = 'tls'
	  AND NULLIF(stream_settings, '')::jsonb #> '{tlsSettings,settings,domains}' IS NOT NULL`
	}
	err = tx.Raw(externalProxyQuery).Scan(&externalProxy).Error
	if err != nil || len(externalProxy) == 0 {
		return
	}

	for _, ep := range externalProxy {
		var reverses any
		var stream map[string]any
		_ = json.Unmarshal([]byte(ep.StreamSettings), &stream)
		if tlsSettings, ok := stream["tlsSettings"].(map[string]any); ok {
			if settings, ok := tlsSettings["settings"].(map[string]any); ok {
				if domains, ok := settings["domains"].([]any); ok {
					for _, domain := range domains {
						if domainMap, ok := domain.(map[string]any); ok {
							domainMap["forceTls"] = "same"
							domainMap["port"] = ep.Port
							domainMap["dest"] = domainMap["domain"].(string)
							delete(domainMap, "domain")
						}
					}
				}
				reverses = settings["domains"]
				delete(settings, "domains")
			}
		}
		stream["externalProxy"] = reverses
		newStream, _ := json.MarshalIndent(stream, " ", "  ")
		tx.Model(model.Inbound{}).Where("id = ?", ep.Id).Update("stream_settings", newStream)
	}

	// Legacy tag cleanup for old auto-generated tags (e.g. "0.0.0.0:443-...").
	// Must be cross-DB: INSTR/REPLACE work on SQLite; Postgres needs position().
	tagCleanup := `UPDATE inbounds
		SET tag = REPLACE(tag, '0.0.0.0:', '')
		WHERE INSTR(tag, '0.0.0.0:') > 0;`
	if database.IsPostgres() {
		tagCleanup = `UPDATE inbounds
			SET tag = REPLACE(tag, '0.0.0.0:', '')
			WHERE position('0.0.0.0:' in tag) > 0;`
	}
	err = tx.Exec(tagCleanup).Error
	if err != nil {
		return
	}
}

func (s *InboundService) MigrateDB() {
	s.MigrationRequirements()
	s.MigrationRemoveOrphanedTraffics()
	s.MigrationRestoreVisionFlow()
}

// MigrationRestoreVisionFlow repairs attachment-level flow overrides without
// revisiting the retired settings.clients authority.
func (s *InboundService) MigrationRestoreVisionFlow() {
	db := database.GetDB()
	var inbounds []model.Inbound
	if err := db.Where("protocol = ?", model.VLESS).Find(&inbounds).Error; err != nil {
		logger.Warning("MigrationRestoreVisionFlow: load inbounds failed:", err)
		return
	}
	for _, inbound := range inbounds {
		if !inboundCanEnableTlsFlow(string(inbound.Protocol), inbound.StreamSettings, inbound.Settings) {
			continue
		}
		visionLinks := db.Table("client_inbounds intended").Select("intended.client_id").Where("intended.flow_override IN ?", []string{"xtls-rprx-vision", "xtls-rprx-vision-udp443"})
		result := db.Model(&model.ClientInbound{}).
			Where("inbound_id = ? AND flow_override = '' AND client_id IN (?)", inbound.Id, visionLinks).
			Update("flow_override", "xtls-rprx-vision")
		if result.Error != nil {
			logger.Warning("MigrationRestoreVisionFlow: update inbound", inbound.Id, "failed:", result.Error)
			continue
		}
		if result.RowsAffected > 0 {
			logger.Info("MigrationRestoreVisionFlow: restored XTLS Vision flow on inbound", inbound.Id)
		}
	}
}
