package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *InboundService) AddTraffic(inboundTraffics []*xray.Traffic, clientTraffics []*xray.ClientTraffic) (needRestart bool, clientsDisabled bool, err error) {
	err = submitTrafficWrite(func() error {
		var inner error
		needRestart, clientsDisabled, inner = s.addTrafficLocked(inboundTraffics, clientTraffics)
		return inner
	})
	return
}

func (s *InboundService) addTrafficLocked(inboundTraffics []*xray.Traffic, clientTraffics []*xray.ClientTraffic) (bool, bool, error) {
	var err error
	db := database.GetDB()
	tx := db.Begin()

	defer func() {
		if err != nil {
			if rbErr := tx.Rollback().Error; rbErr != nil {
				logger.Warning("Error rolling back traffic tx:", rbErr)
			}
		} else if cErr := tx.Commit().Error; cErr != nil {
			logger.Warning("Error committing traffic tx:", cErr)
		}
	}()
	err = s.addInboundTraffic(tx, inboundTraffics)
	if err != nil {
		return false, false, err
	}
	err = s.addClientTraffic(tx, clientTraffics)
	if err != nil {
		return false, false, err
	}

	needRestart0, count, renewErr := s.autoRenewClients(tx)
	if renewErr != nil {
		logger.Warning("Error in renew clients:", renewErr)
	} else if count > 0 {
		logger.Debugf("%v clients renewed", count)
	}

	disabledClientsCount := int64(0)
	needRestart1, count, disableClientsErr := s.disableInvalidClients(tx)
	if disableClientsErr != nil {
		logger.Warning("Error in disabling invalid clients:", disableClientsErr)
	} else if count > 0 {
		logger.Debugf("%v clients disabled", count)
		disabledClientsCount = count
	}

	needRestart2, count, disableInboundsErr := s.disableInvalidInbounds(tx)
	if disableInboundsErr != nil {
		logger.Warning("Error in disabling invalid inbounds:", disableInboundsErr)
	} else if count > 0 {
		logger.Debugf("%v inbounds disabled", count)
	}
	return needRestart0 || needRestart1 || needRestart2, disabledClientsCount > 0, nil
}

func (s *InboundService) addInboundTraffic(tx *gorm.DB, traffics []*xray.Traffic) error {
	if len(traffics) == 0 {
		return nil
	}

	var err error

	for _, traffic := range traffics {
		if traffic.IsInbound {
			err = tx.Model(&model.Inbound{}).Where("tag = ? AND node_id IS NULL", traffic.Tag).
				Updates(map[string]any{
					"up":   gorm.Expr(database.ClampedAddExpr("up"), traffic.Up),
					"down": gorm.Expr(database.ClampedAddExpr("down"), traffic.Down),
				}).Error
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *InboundService) addClientTraffic(tx *gorm.DB, traffics []*xray.ClientTraffic) (err error) {
	if len(traffics) == 0 {
		return nil
	}

	emails := make([]string, 0, len(traffics))
	for _, traffic := range traffics {
		emails = append(emails, traffic.Email)
	}
	dbClientTraffics := make([]*xray.ClientTraffic, 0, len(traffics))
	// Match purely by email. client_traffics is email-keyed (one shared row per
	// email regardless of how many inbounds the client is attached to), and these
	// emails come from the local xray's report, so they always belong to a client
	// attached to a local inbound. The old `inbound_id NOT IN (node inbounds)`
	// filter dropped the local traffic of a client attached to both a node and the
	// mother inbound whenever the node inbound happened to be attached first — its
	// shared row then carried the node inbound's id (AddClientStat used to use
	// OnConflict DoNothing and never refreshed it; it now refreshes inbound_id on
	// conflict, but this filter was removed rather than relying on that ordering).
	err = tx.Model(xray.ClientTraffic{}).
		Where("email IN (?)", emails).
		Find(&dbClientTraffics).Error
	if err != nil {
		return err
	}

	// Avoid empty slice error
	if len(dbClientTraffics) == 0 {
		return nil
	}

	dbClientTraffics, convertedExpiryByEmail, err := s.adjustTraffics(tx, dbClientTraffics)
	if err != nil {
		return err
	}

	// Index by email for O(N) merge.
	trafficByEmail := make(map[string]*xray.ClientTraffic, len(traffics))
	for i := range traffics {
		if traffics[i] != nil {
			trafficByEmail[traffics[i].Email] = traffics[i]
		}
	}
	now := time.Now().UnixMilli()
	// Use atomic per-row UPDATE instead of read-modify-write Save. tx.Save
	// issues UPDATEs in slice order, which varies between concurrent callers;
	// on PostgreSQL two transactions locking the same rows in opposite order
	// deadlock. An atomic "SET up = up + ?" never holds a row lock across a
	// subsequent lock acquisition, so concurrent writers cannot deadlock.
	for _, ct := range dbClientTraffics {
		t, ok := trafficByEmail[ct.Email]
		if !ok || (t.Up == 0 && t.Down == 0) {
			continue
		}
		if err = tx.Exec(
			fmt.Sprintf(
				`UPDATE client_traffics SET up = %s, down = %s, last_online = %s WHERE email = ?`,
				database.ClampedAddExpr("up"),
				database.ClampedAddExpr("down"),
				database.GreatestExpr("last_online", "?"),
			),
			t.Up, t.Down, now, ct.Email,
		).Error; err != nil {
			logger.Warning("AddClientTraffic update data ", err)
		}
	}

	// adjustTraffics converts delayed-start rows (negative ExpiryTime → absolute
	// deadline) in-memory. Persist that conversion now since the traffic UPDATE
	// above only touches up/down/last_online. Only converted emails are written:
	// updating every polled row issued one no-op UPDATE per active client per
	// poll. Sorted order keeps concurrent writers lock-compatible on Postgres.
	for _, email := range slices.Sorted(maps.Keys(convertedExpiryByEmail)) {
		if err = tx.Exec(
			`UPDATE client_traffics SET expiry_time = ? WHERE email = ? AND expiry_time < 0`,
			convertedExpiryByEmail[email], email,
		).Error; err != nil {
			logger.Warning("AddClientTraffic update expiry_time ", err)
		}
	}

	return nil
}

func (s *InboundService) adjustTraffics(tx *gorm.DB, dbClientTraffics []*xray.ClientTraffic) ([]*xray.ClientTraffic, map[string]int64, error) {
	now := time.Now().UnixMilli()

	// "Start After First Use" stores a negative expiry (the duration). On the
	// first traffic tick it becomes an absolute deadline of now+duration. Compute
	// it once per email so every inbound the client is attached to lands on the
	// same value (recomputing per inbound would skip all but the first one).
	newExpiryByEmail := make(map[string]int64, len(dbClientTraffics))
	for traffic_index := range dbClientTraffics {
		if dbClientTraffics[traffic_index].ExpiryTime < 0 {
			newExpiryByEmail[dbClientTraffics[traffic_index].Email] = now - dbClientTraffics[traffic_index].ExpiryTime
		}
	}
	if len(newExpiryByEmail) == 0 {
		return dbClientTraffics, nil, nil
	}

	delayedEmails := make([]string, 0, len(newExpiryByEmail))
	for email := range newExpiryByEmail {
		delayedEmails = append(delayedEmails, email)
	}

	// Resolve the owning inbounds through the client_inbounds link, which is
	// authoritative. client_traffics.inbound_id goes stale when an inbound is
	// deleted and recreated, which would leave the negative expiry unconverted.
	var inboundIds []int
	err := tx.Table("client_inbounds").
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("clients.email IN (?)", delayedEmails).
		Distinct().
		Pluck("client_inbounds.inbound_id", &inboundIds).Error
	if err != nil {
		return nil, nil, err
	}
	if len(inboundIds) == 0 {
		return dbClientTraffics, nil, nil
	}

	for email, expiry := range newExpiryByEmail {
		if err := tx.Model(&model.ClientRecord{}).Where("email = ?", email).
			Updates(map[string]any{"expiry_time": expiry, "updated_at": now}).Error; err != nil {
			return nil, nil, err
		}
	}
	for trafficIndex := range dbClientTraffics {
		if expiry, ok := newExpiryByEmail[dbClientTraffics[trafficIndex].Email]; ok {
			dbClientTraffics[trafficIndex].ExpiryTime = expiry
		}
	}
	return dbClientTraffics, newExpiryByEmail, nil

}

// apiUserFromClient prepares an isolated runtime payload. Shadowsocks clients
// inherit their cipher from the inbound-level protocol settings.
func apiUserFromClient(client map[string]any, cipher string) map[string]any {
	user := maps.Clone(client)
	if user == nil {
		user = map[string]any{}
	}
	if cipher != "" {
		user["cipher"] = cipher
	}
	return user
}

func (s *InboundService) autoRenewClients(tx *gorm.DB) (bool, int64, error) {
	now := time.Now().UnixMilli()
	var traffics []*xray.ClientTraffic
	if err := tx.Model(xray.ClientTraffic{}).
		Where("reset > 0 AND expiry_time > 0 AND expiry_time <= ?", now).
		Where("email IN (?)", tx.Table("client_inbounds ci").Select("c.email").Joins("JOIN clients c ON c.id = ci.client_id").Joins("JOIN inbounds i ON i.id = ci.inbound_id").Where("i.node_id IS NULL")).
		Find(&traffics).Error; err != nil {
		return false, 0, err
	}
	if len(traffics) == 0 {
		return false, 0, nil
	}
	emails := make([]string, 0, len(traffics))
	for _, traffic := range traffics {
		emails = append(emails, traffic.Email)
	}
	var records []model.ClientRecord
	if err := tx.Where("email IN ?", emails).Find(&records).Error; err != nil {
		return false, 0, err
	}
	recordByEmail := make(map[string]*model.ClientRecord, len(records))
	for i := range records {
		recordByEmail[records[i].Email] = &records[i]
	}
	type attachment struct {
		InboundID int
		Email     string
	}
	var attachments []attachment
	if err := tx.Table("client_inbounds ci").Select("ci.inbound_id, c.email").Joins("JOIN clients c ON c.id = ci.client_id").Where("c.email IN ?", emails).Scan(&attachments).Error; err != nil {
		return false, 0, err
	}
	inboundIDs := make([]int, 0, len(attachments))
	for _, attachment := range attachments {
		inboundIDs = append(inboundIDs, attachment.InboundID)
	}
	var inbounds []model.Inbound
	if len(inboundIDs) > 0 {
		if err := tx.Where("id IN ?", uniqueInts(inboundIDs)).Find(&inbounds).Error; err != nil {
			return false, 0, err
		}
	}
	inboundByID := make(map[int]*model.Inbound, len(inbounds))
	for i := range inbounds {
		inboundByID[inbounds[i].Id] = &inbounds[i]
	}
	needRestart := false
	for _, traffic := range traffics {
		newExpiry := traffic.ExpiryTime
		for newExpiry < now {
			newExpiry += int64(traffic.Reset) * 86400000
		}
		wasDisabled := !traffic.Enable
		traffic.ExpiryTime = newExpiry
		traffic.Up, traffic.Down, traffic.Enable = 0, 0, true
		if record := recordByEmail[traffic.Email]; record != nil {
			record.ExpiryTime, record.Enable, record.UpdatedAt = newExpiry, true, now
		}
		if !wasDisabled {
			continue
		}
		for _, attachment := range attachments {
			if attachment.Email != traffic.Email {
				continue
			}
			inbound := inboundByID[attachment.InboundID]
			record := recordByEmail[traffic.Email]
			if inbound == nil || record == nil || !record.Enable {
				continue
			}
			rt, push, _, err := s.nodePushPlan(inbound)
			if err != nil || !push {
				needRestart = true
				continue
			}
			client := record.ToClient()
			if err := rt.AddUser(context.Background(), inbound, map[string]any{"email": client.Email, "id": client.ID, "auth": client.Auth, "security": client.Security, "flow": client.Flow, "password": client.Password, "cipher": shadowsocksMethodFromSettings(inbound.Settings)}); err != nil {
				needRestart = true
			}
		}
	}
	if err := tx.Save(traffics).Error; err != nil {
		return false, 0, err
	}
	for _, record := range recordByEmail {
		if err := tx.Model(&model.ClientRecord{}).Where("id = ?", record.Id).Updates(map[string]any{"expiry_time": record.ExpiryTime, "enable": record.Enable, "updated_at": record.UpdatedAt}).Error; err != nil {
			return false, 0, err
		}
	}
	if err := clearGlobalTraffic(tx, emails...); err != nil {
		return false, 0, err
	}
	return needRestart, int64(len(traffics)), nil
}

// AddClientStat inserts a per-client accounting row, or refreshes the
// config-derived columns on an email conflict. Xray reports traffic per
// email, so the surviving row also acts as the shared accumulator for
// inbounds that re-use the same identity — every call for that identity
// (one per attached inbound) carries the same enable/expiry/reset/total,
// so re-asserting them here is idempotent for that legitimate case.
//
// The conflict path matters on its own for a second reason: an inbound
// delete detaches its clients (InboundService.DelInbound) without deleting
// their client_traffics row, by design — mirroring ClientService.Detach,
// which intentionally leaves a fully-detached client's row in place so a
// later Attach can resume it with its accumulated traffic intact. If that
// same email is instead reused for a freshly (re)created client, the new
// config's enable/expiry/reset/total must win over whatever the orphaned
// row still holds; DoNothing left them stale indefinitely (#5958).
//
// up/down are deliberately excluded from the refresh: they are the
// accumulated traffic totals, and zeroing them here would erase real usage
// every time an existing, actively-used client is attached to one more
// inbound. One tradeoff this does not resolve: a genuinely new client that
// happens to reuse an orphaned email still inherits that row's leftover
// up/down, since nothing at this call site can tell the two cases apart.
func (s *InboundService) AddClientStat(tx *gorm.DB, inboundId int, client *model.Client) error {
	clientTraffic := xray.ClientTraffic{
		InboundId:  inboundId,
		Email:      client.Email,
		Total:      client.TotalGB,
		ExpiryTime: client.ExpiryTime,
		Enable:     client.Enable,
		Reset:      client.Reset,
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{"inbound_id", "total", "expiry_time", "enable", "reset"}),
	}).Create(&clientTraffic).Error
}

func (s *InboundService) UpdateClientStat(tx *gorm.DB, email string, client *model.Client) error {
	result := tx.Model(xray.ClientTraffic{}).
		Where("email = ?", email).
		Updates(map[string]any{
			"enable":      client.Enable,
			"email":       client.Email,
			"total":       client.TotalGB,
			"expiry_time": client.ExpiryTime,
			"reset":       client.Reset,
		})
	err := result.Error
	return err
}

func (s *InboundService) DelClientStat(tx *gorm.DB, email string) error {
	if err := adjustGroupBaselinesForRemovedTraffic(tx, []string{email}); err != nil {
		return err
	}
	if err := tx.Where("email = ?", email).Delete(xray.ClientTraffic{}).Error; err != nil {
		return err
	}
	if err := clearGlobalTraffic(tx, email); err != nil {
		return err
	}
	return tx.Where("email = ?", email).Delete(&model.NodeClientTraffic{}).Error
}

func (s *InboundService) delClientStatsByEmails(tx *gorm.DB, emails []string) error {
	if err := adjustGroupBaselinesForRemovedTraffic(tx, emails); err != nil {
		return err
	}
	const chunk = 400
	for start := 0; start < len(emails); start += chunk {
		end := min(start+chunk, len(emails))
		batch := emails[start:end]
		if err := tx.Where("email IN ?", batch).Delete(xray.ClientTraffic{}).Error; err != nil {
			return err
		}
		if err := tx.Where("email IN ?", batch).Delete(&model.ClientGlobalTraffic{}).Error; err != nil {
			return err
		}
		if err := tx.Where("email IN ?", batch).Delete(&model.NodeClientTraffic{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *InboundService) ResetClientTrafficByEmail(clientEmail string) error {
	err := submitTrafficWrite(func() error {
		return database.GetDB().Transaction(func(tx *gorm.DB) error {
			if err := adjustGroupBaselinesForRemovedTraffic(tx, []string{clientEmail}); err != nil {
				return err
			}
			if err := clearGlobalTraffic(tx, clientEmail); err != nil {
				return err
			}
			if err := tx.Model(xray.ClientTraffic{}).
				Where("email = ?", clientEmail).
				Updates(map[string]any{"enable": true, "up": 0, "down": 0}).Error; err != nil {
				return err
			}
			return tx.Where("email = ?", clientEmail).Delete(&model.NodeClientTraffic{}).Error
		})
	})
	if err == nil {
		s.resetMtprotoClientQuota(clientEmail)
	}
	return err
}

func (s *InboundService) ResetClientTraffic(id int, clientEmail string) (needRestart bool, err error) {
	err = submitTrafficWrite(func() error {
		var inner error
		needRestart, inner = s.resetClientTrafficLocked(id, clientEmail)
		return inner
	})
	if err == nil {
		s.resetMtprotoClientQuota(clientEmail)
	}
	return
}

func (s *InboundService) resetClientTrafficLocked(id int, clientEmail string) (bool, error) {
	needRestart := false

	traffic, err := s.GetClientTrafficByEmail(clientEmail)
	if err != nil {
		return false, err
	}

	if !traffic.Enable {
		inbound, err := s.GetInbound(id)
		if err != nil {
			return false, err
		}
		clients, err := s.GetClients(inbound)
		if err != nil {
			return false, err
		}
		for _, client := range clients {
			if client.Email == clientEmail && client.Enable {
				rt, push, _, perr := s.nodePushPlan(inbound)
				if perr != nil {
					return false, perr
				}
				if !push {
					if inbound.NodeID == nil {
						needRestart = true
					}
					break
				}
				cipher := ""
				if string(inbound.Protocol) == "shadowsocks" {
					var oldSettings map[string]any
					err = json.Unmarshal([]byte(inbound.Settings), &oldSettings)
					if err != nil {
						return false, err
					}
					cipher, _ = oldSettings["method"].(string)
				}
				err1 := rt.AddUser(context.Background(), inbound, map[string]any{
					"email":    client.Email,
					"id":       client.ID,
					"auth":     client.Auth,
					"security": client.Security,
					"flow":     client.Flow,
					"password": client.Password,
					"cipher":   cipher,
				})
				if err1 == nil {
					logger.Debug("Client enabled on", rt.Name(), "due to reset traffic:", clientEmail)
				} else if inbound.NodeID != nil {
					logger.Warning("Error in enabling client on", rt.Name(), ":", err1)
				} else {
					logger.Debug("Error in enabling client on", rt.Name(), ":", err1)
					needRestart = true
				}
				break
			}
		}
	}

	traffic.Up = 0
	traffic.Down = 0
	traffic.Enable = true

	db := database.GetDB()
	now := time.Now().UnixMilli()
	inbound, err := s.GetInbound(id)
	if err != nil {
		return false, err
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := adjustGroupBaselinesForRemovedTraffic(tx, []string{clientEmail}); err != nil {
			return err
		}
		if err := tx.Save(traffic).Error; err != nil {
			return err
		}
		if err := clearGlobalTraffic(tx, clientEmail); err != nil {
			return err
		}
		if err := tx.Where("email = ?", clientEmail).Delete(&model.NodeClientTraffic{}).Error; err != nil {
			return err
		}
		if err := tx.Model(model.Inbound{}).
			Where("id = ?", id).
			Update("last_traffic_reset_time", now).Error; err != nil {
			return err
		}
		if inbound != nil && inbound.NodeID != nil {
			return (&NodeService{}).MarkNodeDirtyTx(tx, *inbound.NodeID)
		}
		return nil
	}); err != nil {
		return false, err
	}

	if inbound != nil && inbound.NodeID != nil {
		if rt, rterr := s.runtimeFor(inbound); rterr == nil {
			if e := rt.ResetClientTraffic(context.Background(), inbound, clientEmail); e != nil {
				logger.Warning("ResetClientTraffic: remote propagation to", rt.Name(), "failed:", e)
			}
		} else {
			logger.Warning("ResetClientTraffic: runtime lookup failed:", rterr)
		}
	}

	return needRestart, nil
}

func (s *InboundService) ResetAllTraffics() error {
	err := submitTrafficWrite(func() error {
		return s.resetAllTrafficsLocked()
	})
	if err == nil {
		s.propagateResetAllTrafficsToNodes()
		s.resetAllMtprotoQuotas()
	}
	return err
}

func (s *InboundService) resetAllTrafficsLocked() error {
	db := database.GetDB()
	now := time.Now().UnixMilli()

	return db.Model(model.Inbound{}).
		Where("user_id > ?", 0).
		Updates(map[string]any{
			"up":                      0,
			"down":                    0,
			"last_traffic_reset_time": now,
		}).Error
}

// propagateResetAllTrafficsToNodes tells every node to zero its own counters.
// Kept OUT of the traffic-writer transaction: each remote call can block up to
// remoteHTTPTimeout, and holding the single serial writer across N such calls
// stalls traffic accounting and drops the deltas of every concurrent poll.
func (s *InboundService) propagateResetAllTrafficsToNodes() {
	nodes, err := (&NodeService{}).GetAll()
	if err != nil {
		return
	}
	for _, node := range nodes {
		if rt, err := runtime.GetManager().RuntimeFor(&node.Id); err == nil {
			if e := rt.ResetAllTraffics(context.Background()); e != nil {
				logger.Warning("ResetAllTraffics: remote propagation to", rt.Name(), "failed:", e)
			}
		}
	}
}

func (s *InboundService) ResetInboundTraffic(id int) error {
	if err := submitTrafficWrite(func() error {
		return database.GetDB().Model(model.Inbound{}).
			Where("id = ?", id).
			Updates(map[string]any{"up": 0, "down": 0}).Error
	}); err != nil {
		return err
	}

	inbound, err := s.GetInbound(id)
	if err == nil && inbound != nil && inbound.NodeID != nil {
		if rt, rterr := s.runtimeFor(inbound); rterr == nil {
			if e := rt.ResetInboundTraffic(context.Background(), inbound); e != nil {
				logger.Warning("ResetInboundTraffic: remote propagation to", rt.Name(), "failed:", e)
			}
		} else {
			logger.Warning("ResetInboundTraffic: runtime lookup failed:", rterr)
		}
	}
	return nil
}

func (s *InboundService) DelDepletedClients(id int) error {
	db := database.GetDB()
	now := time.Now().UnixMilli()
	var traffics []xray.ClientTraffic
	if err := db.Where("reset = 0 AND ((total > 0 AND up + down >= total) OR (expiry_time > 0 AND expiry_time <= ?))", now).Find(&traffics).Error; err != nil {
		return err
	}
	if len(traffics) == 0 {
		return nil
	}
	emails := make([]string, 0, len(traffics))
	for _, traffic := range traffics {
		emails = append(emails, traffic.Email)
	}
	type linkRow struct {
		InboundID int    `gorm:"column:inbound_id"`
		ClientID  int    `gorm:"column:client_id"`
		Email     string `gorm:"column:email"`
	}
	query := db.Table("client_inbounds ci").Select("ci.inbound_id, ci.client_id, c.email").Joins("JOIN clients c ON c.id = ci.client_id").Where("c.email IN ?", emails)
	if id >= 0 {
		query = query.Where("ci.inbound_id = ?", id)
	}
	var links []linkRow
	if err := query.Scan(&links).Error; err != nil {
		return err
	}
	byInbound := make(map[int][]*model.ClientRecord)
	for _, link := range links {
		record := &model.ClientRecord{Id: link.ClientID, Email: link.Email}
		byInbound[link.InboundID] = append(byInbound[link.InboundID], record)
	}
	for inboundID, records := range byInbound {
		if _, err := s.clientService.delInboundClients(s, inboundID, records, false, false); err != nil {
			return err
		}
	}
	if id < 0 {
		return db.Where("email IN ? AND NOT EXISTS (SELECT 1 FROM client_inbounds ci JOIN clients c ON c.id = ci.client_id WHERE c.email = client_traffics.email)", emails).Delete(&xray.ClientTraffic{}).Error
	}
	return nil
}
func (s *InboundService) GetClientTrafficTgBot(tgId int64) ([]*xray.ClientTraffic, error) {
	db := database.GetDB()
	var emails []string
	if err := db.Model(&model.ClientRecord{}).Where("tg_id = ?", tgId).Pluck("email", &emails).Error; err != nil {
		return nil, err
	}
	uniqEmails := uniqueNonEmptyStrings(emails)
	if len(uniqEmails) == 0 {
		return nil, nil
	}
	traffics := make([]*xray.ClientTraffic, 0, len(uniqEmails))
	for _, batch := range chunkStrings(uniqEmails, sqliteMaxVars) {
		var page []*xray.ClientTraffic
		if err := db.Where("email IN ?", batch).Find(&page).Error; err != nil {
			return nil, err
		}
		traffics = append(traffics, page...)
	}
	for _, traffic := range traffics {
		if record, err := s.clientService.GetRecordByEmail(db, traffic.Email); err == nil && record != nil {
			traffic.Enable = record.Enable
			traffic.UUID = record.UUID
			traffic.SubId = record.SubID
		}
	}
	return traffics, nil
}

// BumpClientsLastOnline sets client_traffics.last_online to now for the given
// emails. Used in online-API mode for clients that hold a live connection but
// moved no bytes this poll — the traffic path (addClientTraffic) only bumps
// last_online on a non-zero delta, so idle-but-connected clients would
// otherwise show a stale "last online" while being reported online.
func (s *InboundService) BumpClientsLastOnline(emails []string) error {
	uniq := uniqueNonEmptyStrings(emails)
	if len(uniq) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	return submitTrafficWrite(func() error {
		db := database.GetDB()
		for _, batch := range chunkStrings(uniq, sqliteMaxVars) {
			if err := db.Model(xray.ClientTraffic{}).Where("email IN ?", batch).Update("last_online", now).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *InboundService) GetActiveClientTraffics(emails []string) ([]*xray.ClientTraffic, error) {
	uniq := uniqueNonEmptyStrings(emails)
	if len(uniq) == 0 {
		return nil, nil
	}
	db := database.GetDB()
	traffics := make([]*xray.ClientTraffic, 0, len(uniq))
	for _, batch := range chunkStrings(uniq, sqliteMaxVars) {
		var page []*xray.ClientTraffic
		if err := db.Model(xray.ClientTraffic{}).Where("email IN ?", batch).Find(&page).Error; err != nil {
			return nil, err
		}
		traffics = append(traffics, page...)
	}
	overlayGlobalTraffic(db, traffics)
	return traffics, nil
}

// GetAllClientTraffics returns the full set of client_traffics rows so the
// websocket broadcasters can ship a complete snapshot every cycle. A pure
// delta path silently dropped the per-client section whenever no client moved
// bytes in the cycle or a node sync failed, leaving client rows in the UI
// stuck at stale numbers — so small installs broadcast this snapshot, and only
// above the traffic job's snapshot threshold (where the marshaled snapshot
// would exceed the hub's payload cap and be dropped wholesale) does the job
// fall back to active-row deltas.
func (s *InboundService) GetAllClientTraffics() ([]*xray.ClientTraffic, error) {
	db := database.GetDB()
	var traffics []*xray.ClientTraffic
	if err := db.Model(xray.ClientTraffic{}).Find(&traffics).Error; err != nil {
		return nil, err
	}
	overlayGlobalTraffic(db, traffics)
	return traffics, nil
}

func (s *InboundService) CountClientTraffics() (int64, error) {
	db := database.GetDB()
	var count int64
	err := db.Model(xray.ClientTraffic{}).Count(&count).Error
	return count, err
}

type InboundTrafficSummary struct {
	Id     int   `json:"id"`
	Up     int64 `json:"up"`
	Down   int64 `json:"down"`
	Total  int64 `json:"total"`
	Enable bool  `json:"enable"`
}

func (s *InboundService) GetInboundsTrafficSummary() ([]InboundTrafficSummary, error) {
	db := database.GetDB()
	var summaries []InboundTrafficSummary
	if err := db.Model(&model.Inbound{}).
		Select("id, up, down, total, enable").
		Find(&summaries).Error; err != nil {
		return nil, err
	}
	return summaries, nil
}

func (s *InboundService) GetClientTrafficByEmail(email string) (traffic *xray.ClientTraffic, err error) {
	db := database.GetDB()
	var traffics []*xray.ClientTraffic
	if err := db.Model(xray.ClientTraffic{}).Where("email = ?", email).Find(&traffics).Error; err != nil {
		logger.Warningf("Error retrieving ClientTraffic with email %s: %v", email, err)
		return nil, err
	}
	if len(traffics) == 0 {
		return nil, nil
	}
	overlayGlobalTraffic(db, traffics)
	t := traffics[0]

	if rec, rErr := s.clientService.GetRecordByEmail(db, email); rErr == nil && rec != nil {
		c := rec.ToClient()
		t.UUID = c.ID
		t.SubId = c.SubID
		return t, nil
	}

	t2, client, err := s.GetClientByEmail(email)
	if err != nil {
		logger.Warningf("Error retrieving ClientTraffic with email %s: %v", email, err)
		return nil, err
	}
	if t2 != nil && client != nil {
		t2.UUID = client.ID
		t2.SubId = client.SubID
		return t2, nil
	}
	return nil, nil
}

func (s *InboundService) UpdateClientTrafficByEmail(email string, upload int64, download int64) error {
	return submitTrafficWrite(func() error {
		db := database.GetDB()
		err := db.Model(xray.ClientTraffic{}).
			Where("email = ?", email).
			Updates(map[string]any{
				"up":   upload,
				"down": download,
			}).Error
		if err != nil {
			logger.Warningf("Error updating ClientTraffic with email %s: %v", email, err)
		}
		return err
	})
}

func (s *InboundService) SearchClientTraffic(query string) (traffic *xray.ClientTraffic, err error) {
	var record model.ClientRecord
	if err := database.GetDB().Where("uuid = ? OR password = ?", query, query).First(&record).Error; err != nil {
		return nil, err
	}
	traffic = &xray.ClientTraffic{}
	if err := database.GetDB().Where("email = ?", record.Email).First(traffic).Error; err != nil {
		return nil, err
	}
	return traffic, nil
}
