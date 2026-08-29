package service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

// advancePushedInbound advances the node's reconcile-skip fingerprint from the
// pre-edit settings to the saved ones after every per-client push succeeded.
func advancePushedInbound(rt runtime.Runtime, prevSettings string, ib *model.Inbound) {
	rem, ok := rt.(*runtime.Remote)
	if !ok {
		return
	}
	prev := *ib
	prev.Settings = prevSettings
	rem.AdvancePushedInbound(&prev, ib)
}

// delInboundClients removes normalized links in one transaction and applies the
// corresponding runtime user removals as a batch.
func (s *ClientService) delInboundClients(inboundSvc *InboundService, inboundId int, recs []*model.ClientRecord, keepTraffic, fullDelete bool) (bool, error) {
	if len(recs) == 0 {
		return false, nil
	}
	defer lockInbound(inboundId).Unlock()

	oldInbound, err := inboundSvc.GetInbound(inboundId)
	if err != nil {
		logger.Error("Load Old Data Error")
		return false, err
	}

	type removedClient struct {
		email      string
		needApiDel bool
	}
	removed := make([]removedClient, 0, len(recs))
	for _, rec := range recs {
		if rec.Email != "" {
			removed = append(removed, removedClient{email: rec.Email, needApiDel: rec.Enable})
		}
	}
	if len(removed) == 0 {
		return false, nil
	}

	prevSettings := oldInbound.Settings

	var sharedSet map[string]bool
	if !keepTraffic {
		removedEmails := make([]string, 0, len(removed))
		for _, r := range removed {
			if r.email != "" {
				removedEmails = append(removedEmails, r.email)
			}
		}
		var sharedErr error
		sharedSet, sharedErr = inboundSvc.emailsUsedByOtherInbounds(removedEmails, inboundId)
		if sharedErr != nil {
			return false, sharedErr
		}
	}

	needRestart := false

	// Read each client's live state before the DB write (DelClientStat would
	// erase the enable flag we need to decide on a runtime removal).
	type delTarget struct {
		email       string
		emailShared bool
		notDepleted bool
		needApiDel  bool
	}
	db := database.GetDB()
	targets := make([]delTarget, 0, len(removed))
	for _, r := range removed {
		email := r.email
		emailShared := sharedSet[strings.ToLower(strings.TrimSpace(email))]
		notDepleted := false
		if len(email) > 0 {
			var enables []bool
			if err := db.Model(xray.ClientTraffic{}).Where("email = ?", email).Limit(1).Pluck("enable", &enables).Error; err != nil {
				logger.Error("Get stats error")
				return needRestart, err
			}
			notDepleted = len(enables) > 0 && enables[0]
		}
		targets = append(targets, delTarget{email: email, emailShared: emailShared, notDepleted: notDepleted, needApiDel: r.needApiDel})
	}

	// Persist the batch deletion atomically, serialized against the traffic poll
	// to avoid the cross-transaction lock-order deadlock (runSerializedTx).
	if txErr := runSerializedTx(func(tx *gorm.DB) error {
		for _, t := range targets {
			if t.emailShared || keepTraffic {
				continue
			}
			if e := inboundSvc.DelClientIPs(tx, t.email); e != nil {
				logger.Error("Error in delete client IPs")
				return e
			}
			if len(t.email) > 0 {
				if e := inboundSvc.DelClientStat(tx, t.email); e != nil {
					logger.Error("Delete stats Data Error")
					return e
				}
			}
		}
		ids := make([]int, 0, len(recs))
		for _, rec := range recs {
			ids = append(ids, rec.Id)
		}
		if len(ids) > 0 {
			if e := tx.Where("inbound_id = ? AND client_id IN ?", inboundId, ids).Delete(&model.ClientInbound{}).Error; e != nil {
				return e
			}
		}
		if oldInbound.NodeID != nil {
			return (&NodeService{}).MarkNodeDirtyTx(tx, *oldInbound.NodeID)
		}
		return nil
	}); txErr != nil {
		return needRestart, txErr
	}

	// Resolve the node push plan once for the whole batch instead of per email.
	var nodeRt runtime.Runtime
	nodePush := false
	if oldInbound.NodeID != nil {
		rt, push, _, perr := inboundSvc.nodePushPlan(oldInbound)
		if perr != nil {
			return needRestart, perr
		}
		nodeRt, nodePush = rt, push
		// Large batches collapse into one reconcile push rather than M deletes.
		if nodePush && len(targets) > nodeBulkPushThreshold {
			nodePush = false
		}
	}
	if oldInbound.NodeID == nil && oldInbound.Protocol == model.MTProto {
		inboundSvc.applyLocalMtproto(oldInbound.Id)
		return false, nil
	}

	// Apply runtime deletes after commit — outside the serialized writer so a
	// slow node call can't stall traffic accounting.
	nodePushFailed := false
	for _, t := range targets {
		if len(t.email) == 0 {
			continue
		}
		if oldInbound.NodeID == nil {
			if t.needApiDel && t.notDepleted {
				rt, rterr := inboundSvc.runtimeFor(oldInbound)
				if rterr != nil {
					needRestart = true
				} else if err1 := rt.RemoveUser(context.Background(), oldInbound, t.email); err1 != nil {
					if !strings.Contains(err1.Error(), fmt.Sprintf("User %s not found.", t.email)) {
						needRestart = true
					}
				}
			}
		} else if nodePush {
			var err error
			if fullDelete {
				err = nodeRt.DeleteClient(context.Background(), t.email)
			} else {
				err = nodeRt.DeleteUser(context.Background(), oldInbound, t.email)
			}
			if err != nil {
				logger.Warning("Error in deleting client on", nodeRt.Name(), ":", err)
				nodePushFailed = true
			}
		}
	}
	if nodePush && !nodePushFailed {
		advancePushedInbound(nodeRt, prevSettings, oldInbound)
	}

	return needRestart, nil
}

func (s *ClientService) checkEmailsExistForClients(inboundSvc *InboundService, clients []model.Client, emailSubIDs map[string]string) (string, error) {
	if emailSubIDs == nil {
		var err error
		emailSubIDs, err = inboundSvc.getAllEmailSubIDs()
		if err != nil {
			return "", err
		}
	}
	seen := make(map[string]string, len(clients))
	for _, client := range clients {
		if client.Email == "" {
			continue
		}
		key := strings.ToLower(client.Email)
		if prev, ok := seen[key]; ok {
			if prev != client.SubID || client.SubID == "" {
				return client.Email, nil
			}
			continue
		}
		seen[key] = client.SubID
		if existingSub, ok := emailSubIDs[key]; ok {
			if client.SubID == "" || existingSub == "" || existingSub != client.SubID {
				return client.Email, nil
			}
		}
	}
	return "", nil
}

func (s *ClientService) AddInboundClient(inboundSvc *InboundService, data *model.Inbound) (bool, error) {
	return s.addInboundClient(inboundSvc, data, nil)
}

// addInboundClient applies a request draft to normalized client rows.
func (s *ClientService) addInboundClient(inboundSvc *InboundService, data *model.Inbound, emailSubIDs map[string]string) (bool, error) {
	defer lockInbound(data.Id).Unlock()

	clients, err := ParseInboundDraftClients(data.Settings)
	if err != nil {
		return false, err
	}

	var settings map[string]any
	err = json.Unmarshal([]byte(data.Settings), &settings)
	if err != nil {
		return false, err
	}

	interfaceClients, ok := settings["clients"].([]any)
	if !ok {
		return false, common.NewError("invalid clients format in inbound draft")
	}
	nowTs := time.Now().UnixMilli()
	for i := range clients {
		if clients[i].CreatedAt == 0 {
			clients[i].CreatedAt = nowTs
		}
		clients[i].UpdatedAt = nowTs
		if strings.TrimSpace(clients[i].SubID) == "" {
			clients[i].SubID = random.NumLower(16)
		}
	}
	existEmail, err := s.checkEmailsExistForClients(inboundSvc, clients, emailSubIDs)
	if err != nil {
		return false, err
	}
	if existEmail != "" {
		return false, common.NewError("Duplicate email:", existEmail)
	}

	oldInbound, err := inboundSvc.GetInbound(data.Id)
	if err != nil {
		return false, err
	}

	// A client already on this inbound is skipped instead of appended again:
	// checkEmailsExistForClients exempts a matching subId so one identity can
	// live on several inbounds, which let retried or raced adds duplicate the
	// same email inside a single settings array (#5770). clients and
	// interfaceClients are parsed from the same data.Settings array, so they
	// stay index-aligned while filtering.
	//
	// Asked of the normalized tables about **these** emails rather than by
	// parsing the whole settings blob: the old shape cost 70ms at 50k clients to
	// decide something about one email.
	incomingEmails := make([]string, 0, len(clients))
	for _, c := range clients {
		if c.Email != "" {
			incomingEmails = append(incomingEmails, c.Email)
		}
	}
	existingEmails, err := s.EmailsAlreadyOnInbound(nil, oldInbound.Id, incomingEmails)
	if err != nil {
		return false, err
	}
	if len(existingEmails) > 0 && len(clients) > 0 {
		keptClients := make([]model.Client, 0, len(clients))
		keptWire := make([]any, 0, len(interfaceClients))
		for i, c := range clients {
			if c.Email != "" {
				if _, dup := existingEmails[strings.ToLower(c.Email)]; dup {
					continue
				}
			}
			keptClients = append(keptClients, c)
			if i < len(interfaceClients) {
				keptWire = append(keptWire, interfaceClients[i])
			}
		}
		if len(keptClients) == 0 {
			return false, nil
		}
		clients = keptClients
		interfaceClients = keptWire
	}

	if oldInbound.Protocol == model.WireGuard {
		// Only WireGuard still needs the full membership (it derives peer
		// defaults from what is already there). Parsing the blob is O(N), so it
		// stays inside this branch instead of running for every protocol.
		existingClients, gcErr := inboundSvc.GetClients(oldInbound)
		if gcErr != nil {
			return false, gcErr
		}
		if dErr := defaultWireguardClients(existingClients, clients, interfaceClients); dErr != nil {
			return false, dErr
		}
	}

	for _, client := range clients {
		if strings.TrimSpace(client.Email) == "" {
			return false, common.NewError("client email is required")
		}
		switch oldInbound.Protocol {
		case "trojan", "mixed", "http":
			if client.Password == "" {
				return false, common.NewError("empty client ID")
			}
		case "shadowsocks":
			if client.Email == "" {
				return false, common.NewError("empty client ID")
			}
		case "hysteria":
			if client.Auth == "" {
				return false, common.NewError("empty client ID")
			}
		case "wireguard":
			if client.PublicKey == "" {
				return false, common.NewError("wireguard client requires a key")
			}
		case "mtproto":
			if client.Secret == "" {
				return false, common.NewError("mtproto client requires a secret")
			}
			if client.AdTag != "" && !model.ValidMtprotoAdTag(client.AdTag) {
				return false, common.NewError("mtproto client ad tag must be 32 hex characters")
			}
		default:
			if client.ID == "" {
				return false, common.NewError("empty client ID")
			}
		}
	}

	prevSettings := oldInbound.Settings
	cipher := ""
	if oldInbound.Protocol == model.Shadowsocks {
		cipher = shadowsocksMethodFromSettings(oldInbound.Settings)
	}

	needRestart := false

	rt, push, _, perr := inboundSvc.nodePushPlan(oldInbound)
	if perr != nil {
		return false, perr
	}

	// Persist the one client, its attachments and traffic row atomically,
	// serialized against the traffic poll to avoid the cross-transaction
	// Persist only the normalized client record and link rows. The inbound
	// settings blob is never rewritten for client CRUD.
	if txErr := runSerializedTx(func(tx *gorm.DB) error {
		for i := range clients {
			if len(clients[i].Email) == 0 {
				continue
			}
			if e := inboundSvc.AddClientStat(tx, data.Id, &clients[i]); e != nil {
				return e
			}
		}
		// Only the clients being added, not everyone already on the inbound.
		//
		// This used to re-parse the whole settings blob and hand all of it to
		// SyncInbound, which deletes and rebuilds every client_inbounds row for
		// the inbound. Adding the 50,001st client therefore paid to rewrite
		// 50,001 links. Measured on a real core at 50k: the parse cost 70ms and
		// the full replace 406ms warm (2.3s cold) inside this transaction —
		// together the largest part of one add.
		//
		// An add never removes anyone, so the delete-all was pure cost. Rows
		// this call does not touch are repaired by the periodic full pass.
		if err := s.SyncInboundAdd(tx, oldInbound.Id, clients); err != nil {
			return err
		}
		if oldInbound.NodeID != nil {
			return (&NodeService{}).MarkNodeDirtyTx(tx, *oldInbound.NodeID)
		}
		return nil
	}); txErr != nil {
		return false, txErr
	}

	// Apply to the running runtime after commit — outside the serialized writer
	// so a slow node call can't stall traffic accounting.
	if oldInbound.NodeID == nil {
		if !push {
			needRestart = true
		} else if oldInbound.Protocol == model.MTProto {
			inboundSvc.applyLocalMtproto(oldInbound.Id)
		} else {
			for _, client := range clients {
				if len(client.Email) == 0 {
					needRestart = true
					continue
				}
				if !client.Enable {
					continue
				}
				err1 := rt.AddUser(context.Background(), oldInbound, map[string]any{
					"email":        client.Email,
					"id":           client.ID,
					"auth":         client.Auth,
					"security":     client.Security,
					"flow":         client.Flow,
					"password":     client.Password,
					"cipher":       cipher,
					"publicKey":    client.PublicKey,
					"allowedIPs":   client.AllowedIPs,
					"preSharedKey": client.PreSharedKey,
					"keepAlive":    keepAliveStr(client.KeepAlive),
				})
				if err1 == nil {
					logger.Debug("Client added on", rt.Name(), ":", client.Email)
				} else {
					logger.Debug("Error in adding client on", rt.Name(), ":", err1)
					needRestart = true
				}
			}
		}
	} else {
		// Large batches would be M sequential per-client RPCs; normalized rows
		// already hold the final set, so mark dirty and let one reconcile push
		// converge the node instead.
		if push && len(clients) > nodeBulkPushThreshold {
			push = false
		}
		for _, client := range clients {
			if push {
				if err1 := rt.AddClient(context.Background(), oldInbound, client); err1 != nil {
					logger.Warning("Error in adding client on", rt.Name(), ":", err1)
					push = false
				}
			}
		}
		if push {
			advancePushedInbound(rt, prevSettings, oldInbound)
		}
	}

	return needRestart, nil
}

func (s *ClientService) UpdateInboundClient(inboundSvc *InboundService, data *model.Inbound, oldEmail string) (bool, error) {
	clients, err := ParseInboundDraftClients(data.Settings)
	if err != nil {
		return false, err
	}
	if len(clients) != 1 {
		return false, common.NewError("expected one client in inbound draft")
	}
	inbound, err := inboundSvc.GetInbound(data.Id)
	if err != nil {
		return false, err
	}
	old, err := s.GetRecordByEmail(nil, oldEmail)
	if err != nil {
		var folded model.ClientRecord
		if foldedErr := database.GetDB().Where("LOWER(email) = LOWER(?)", oldEmail).First(&folded).Error; foldedErr != nil {
			return false, err
		}
		old = &folded
	}
	updated := clients[0]
	updated.CreatedAt = old.CreatedAt
	if updated.SubID == "" {
		updated.SubID = old.SubID
	}
	merged := *old
	applyClientRecordMerge(&merged, updated.ToRecord())
	merged.Email = updated.Email
	merged.UpdatedAt = time.Now().UnixMilli()

	var link model.ClientInbound
	if err := database.GetDB().Where("client_id = ? AND inbound_id = ?", old.Id, inbound.Id).First(&link).Error; err != nil {
		return false, err
	}
	before, after := *old, merged
	before.UpdatedAt, after.UpdatedAt = 0, 0
	if reflect.DeepEqual(before, after) && link.FlowOverride == updated.Flow {
		return false, nil
	}

	if err := runSerializedTx(func(tx *gorm.DB) error {
		if err := tx.Save(&merged).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ClientInbound{}).
			Where("client_id = ? AND inbound_id = ?", old.Id, inbound.Id).
			Update("flow_override", updated.Flow).Error; err != nil {
			return err
		}
		if err := inboundSvc.UpdateClientStat(tx, old.Email, &updated); err != nil {
			return err
		}
		if err := inboundSvc.UpdateClientIPs(tx, old.Email, updated.Email); err != nil {
			return err
		}
		if inbound.NodeID != nil {
			return (&NodeService{}).MarkNodeDirtyTx(tx, *inbound.NodeID)
		}
		return nil
	}); err != nil {
		return false, err
	}

	if inbound.Protocol == model.MTProto && inbound.NodeID == nil {
		inboundSvc.applyLocalMtproto(inbound.Id)
		return false, nil
	}
	rt, push, _, err := inboundSvc.nodePushPlan(inbound)
	if err != nil {
		return false, err
	}
	if !push {
		return true, nil
	}
	if err := rt.UpdateUser(context.Background(), inbound, old.Email, updated); err != nil {
		if inbound.NodeID != nil {
			logger.Warning("Error in updating client on", rt.Name(), ":", err)
			return false, nil
		}
		return true, nil
	}
	return false, nil
}

func (s *ClientService) DelInboundClientByEmail(inboundSvc *InboundService, inboundId int, email string, keepTraffic bool, fullDelete bool) (bool, error) {
	record, err := s.GetRecordByEmail(nil, email)
	if err != nil {
		return false, err
	}
	return s.delInboundClients(inboundSvc, inboundId, []*model.ClientRecord{record}, keepTraffic, fullDelete)
}

func (s *ClientService) SetClientTelegramUserID(inboundSvc *InboundService, trafficId int, tgId int64) (bool, error) {
	traffic, inbound, err := inboundSvc.GetClientInboundByTrafficID(trafficId)
	if err != nil {
		return false, err
	}
	if inbound == nil {
		return false, common.NewError("Inbound Not Found For Traffic ID:", trafficId)
	}
	record, err := s.GetRecordByEmail(nil, traffic.Email)
	if err != nil {
		return false, err
	}
	if err := database.GetDB().Model(&model.ClientRecord{}).Where("id = ?", record.Id).Updates(map[string]any{"tg_id": tgId, "updated_at": time.Now().UnixMilli()}).Error; err != nil {
		return false, err
	}
	if inbound.NodeID != nil {
		return false, database.GetDB().Transaction(func(tx *gorm.DB) error { return (&NodeService{}).MarkNodeDirtyTx(tx, *inbound.NodeID) })
	}
	return false, nil
}

func (s *ClientService) CheckIsEnabledByEmail(inboundSvc *InboundService, clientEmail string) (bool, error) {
	_, inbound, err := inboundSvc.GetClientInboundByEmail(clientEmail)
	if err != nil {
		return false, err
	}
	if inbound == nil {
		return false, common.NewError("Inbound Not Found For Email:", clientEmail)
	}

	clients, err := inboundSvc.GetClients(inbound)
	if err != nil {
		return false, err
	}

	isEnable := false

	for _, client := range clients {
		if client.Email == clientEmail {
			isEnable = client.Enable
			break
		}
	}

	return isEnable, err
}

func (s *ClientService) ToggleClientEnableByEmail(inboundSvc *InboundService, clientEmail string) (bool, bool, error) {
	current, err := s.CheckIsEnabledByEmail(inboundSvc, clientEmail)
	if err != nil {
		return false, false, err
	}
	target := !current
	needRestart, err := s.applyClientFieldByEmail(inboundSvc, clientEmail, func(c map[string]any) {
		c["enable"] = target
	})
	if err != nil {
		return false, needRestart, err
	}
	return target, needRestart, nil
}

func (s *ClientService) SetClientEnableByEmail(inboundSvc *InboundService, clientEmail string, enable bool) (bool, bool, error) {
	current, err := s.CheckIsEnabledByEmail(inboundSvc, clientEmail)
	if err != nil {
		return false, false, err
	}
	if current == enable {
		return false, false, nil
	}
	needRestart, err := s.applyClientFieldByEmail(inboundSvc, clientEmail, func(c map[string]any) {
		c["enable"] = enable
	})
	if err != nil {
		return false, needRestart, err
	}
	return true, needRestart, nil
}

// applyClientFieldByEmail mutates the normalized logical identity, then lets
// ClientService.Update propagate the targeted runtime updates to its links.
func (s *ClientService) applyClientFieldByEmail(inboundSvc *InboundService, clientEmail string, mutate func(c map[string]any)) (bool, error) {
	record, err := s.GetRecordByEmail(nil, clientEmail)
	if err != nil {
		return false, err
	}
	encoded, err := json.Marshal(record.ToClient())
	if err != nil {
		return false, err
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return false, err
	}
	mutate(fields)
	encoded, err = json.Marshal(fields)
	if err != nil {
		return false, err
	}
	var updated model.Client
	if err := json.Unmarshal(encoded, &updated); err != nil {
		return false, err
	}
	updated.UpdatedAt = time.Now().UnixMilli()
	return s.Update(inboundSvc, record.Id, updated)
}

func (s *ClientService) ResetClientIpLimitByEmail(inboundSvc *InboundService, clientEmail string, count int) (bool, error) {
	return s.applyClientFieldByEmail(inboundSvc, clientEmail, func(c map[string]any) {
		c["limitIp"] = count
	})
}

func (s *ClientService) ResetClientExpiryTimeByEmail(inboundSvc *InboundService, clientEmail string, expiry_time int64) (bool, error) {
	return s.applyClientFieldByEmail(inboundSvc, clientEmail, func(c map[string]any) {
		c["expiryTime"] = expiry_time
	})
}

func (s *ClientService) ResetClientTrafficLimitByEmail(inboundSvc *InboundService, clientEmail string, totalGB int) (bool, error) {
	if totalGB < 0 {
		return false, common.NewError("totalGB must be >= 0")
	}
	return s.applyClientFieldByEmail(inboundSvc, clientEmail, func(c map[string]any) {
		c["totalGB"] = totalGB * 1024 * 1024 * 1024
	})
}
