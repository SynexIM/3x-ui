package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

// BulkAttachResult reports the outcome of a bulk attach across target inbounds.
type BulkAttachResult struct {
	Attached []string `json:"attached"`
	Skipped  []string `json:"skipped"`
	Errors   []string `json:"errors"`
}

// BulkAttach attaches the given existing clients (by email) to each target inbound,
// reusing their identity (email/UUID/password/subId) and a shared traffic row. It adds
// all clients to a target in a single AddInboundClient call, and reports clients already
// present on a target as skipped.
func (s *ClientService) BulkAttach(inboundSvc *InboundService, emails []string, inboundIds []int) (*BulkAttachResult, bool, error) {
	result := &BulkAttachResult{}
	if len(emails) == 0 || len(inboundIds) == 0 {
		return result, false, nil
	}

	recordErr := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		result.Errors = append(result.Errors, msg)
		logger.Warningf("[BulkAttach] %s", msg)
	}

	records := make([]*model.ClientRecord, 0, len(emails))
	seenEmail := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		if email == "" {
			continue
		}
		key := strings.ToLower(email)
		if _, ok := seenEmail[key]; ok {
			continue
		}
		seenEmail[key] = struct{}{}
		rec, err := s.GetRecordByEmail(nil, email)
		if err != nil {
			recordErr("%s: %v", email, err)
			continue
		}
		records = append(records, rec)
	}

	emailSubIDs, sidErr := inboundSvc.getAllEmailSubIDs()
	if sidErr != nil {
		emailSubIDs = nil
		logger.Warningf("[BulkAttach] getAllEmailSubIDs: %v", sidErr)
	}

	needRestart := false
	for _, ibId := range inboundIds {
		inbound, err := inboundSvc.GetInbound(ibId)
		if err != nil {
			recordErr("inbound %d: %v", ibId, err)
			continue
		}
		wantedEmails := make([]string, 0, len(records))
		for _, record := range records {
			wantedEmails = append(wantedEmails, record.Email)
		}
		have, err := s.EmailsAlreadyOnInbound(nil, ibId, wantedEmails)
		if err != nil {
			recordErr("inbound %d: %v", ibId, err)
			continue
		}

		clientsToAdd := make([]model.Client, 0, len(records))
		for _, rec := range records {
			if _, attached := have[strings.ToLower(rec.Email)]; attached {
				result.Skipped = append(result.Skipped, rec.Email)
				continue
			}
			client := *rec.ToClient()
			client.UpdatedAt = time.Now().UnixMilli()
			if err := s.fillProtocolDefaults(&client, inbound); err != nil {
				recordErr("%s -> inbound %d: %v", rec.Email, ibId, err)
				continue
			}
			clientsToAdd = append(clientsToAdd, clientWithInboundFlow(client, inbound))
		}

		if len(clientsToAdd) == 0 {
			continue
		}

		payload, err := json.Marshal(map[string][]model.Client{"clients": clientsToAdd})
		if err != nil {
			recordErr("inbound %d: %v", ibId, err)
			continue
		}
		nr, err := s.addInboundClient(inboundSvc, &model.Inbound{Id: ibId, Settings: string(payload)}, emailSubIDs)
		if err != nil {
			recordErr("inbound %d: %v", ibId, err)
			continue
		}
		if nr {
			needRestart = true
		}
		for _, c := range clientsToAdd {
			result.Attached = append(result.Attached, c.Email)
		}
	}

	return result, needRestart, nil
}

// BulkDetachResult reports the outcome of a bulk detach across target inbounds.
type BulkDetachResult struct {
	Detached []string `json:"detached"`
	Skipped  []string `json:"skipped"`
	Errors   []string `json:"errors"`
}

// BulkDetach detaches the given existing clients (by email) from each target inbound.
// (email, inbound) pairs where the client is not currently attached are silently skipped
// at the inbound level; emails that aren't attached to any of the requested inbounds
// are reported under skipped. ClientRecord rows are kept even when they become orphaned
// (matches single-client detach semantics); callers should use bulkDelete for full removal.
func (s *ClientService) BulkDetach(inboundSvc *InboundService, emails []string, inboundIds []int) (*BulkDetachResult, bool, error) {
	result := &BulkDetachResult{}
	if len(emails) == 0 || len(inboundIds) == 0 {
		return result, false, nil
	}

	recordErr := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		result.Errors = append(result.Errors, msg)
		logger.Warningf("[BulkDetach] %s", msg)
	}

	requested := make(map[int]struct{}, len(inboundIds))
	for _, id := range inboundIds {
		requested[id] = struct{}{}
	}

	recsByInbound := make(map[int][]*model.ClientRecord)
	emailOrder := make([]string, 0, len(emails))
	emailRepr := make(map[string]string, len(emails))
	emailFailed := make(map[string]bool, len(emails))
	seenEmail := make(map[string]struct{}, len(emails))
	for _, email := range emails {
		if email == "" {
			continue
		}
		key := strings.ToLower(email)
		if _, ok := seenEmail[key]; ok {
			continue
		}
		seenEmail[key] = struct{}{}

		rec, err := s.GetRecordByEmail(nil, email)
		if err != nil {
			recordErr("%s: %v", email, err)
			continue
		}
		currentIds, err := s.GetInboundIdsForRecord(rec.Id)
		if err != nil {
			recordErr("%s: %v", email, err)
			continue
		}
		matched := false
		for _, id := range currentIds {
			if _, ok := requested[id]; ok {
				recsByInbound[id] = append(recsByInbound[id], rec)
				matched = true
			}
		}
		if !matched {
			result.Skipped = append(result.Skipped, rec.Email)
			continue
		}
		emailOrder = append(emailOrder, key)
		emailRepr[key] = rec.Email
	}

	needRestart := false
	for _, ibId := range inboundIds {
		recs, ok := recsByInbound[ibId]
		if !ok {
			continue
		}
		delete(recsByInbound, ibId)
		nr, err := s.delInboundClients(inboundSvc, ibId, recs, true, false)
		if err != nil {
			recordErr("inbound %d: %v", ibId, err)
			for _, rec := range recs {
				emailFailed[strings.ToLower(rec.Email)] = true
			}
			continue
		}
		if nr {
			needRestart = true
		}
	}

	for _, key := range emailOrder {
		if emailFailed[key] {
			continue
		}
		result.Detached = append(result.Detached, emailRepr[key])
	}

	return result, needRestart, nil
}

// BulkAdjustResult is returned by BulkAdjust to report how many clients were
// successfully updated and which were skipped (typically because the field
// being adjusted was unlimited for that client) or failed.
type BulkAdjustResult struct {
	Adjusted int                `json:"adjusted"`
	Skipped  []BulkAdjustReport `json:"skipped,omitempty"`
}

type BulkAdjustReport struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

type bulkAdjustEntry struct {
	record      *model.ClientRecord
	applyExpiry bool
	newExpiry   int64
	applyTotal  bool
	newTotal    int64
}

// bulkFlowClear is the directive that strips the XTLS flow from every selected
// client. The vision values are the only positive flows xray accepts.
const bulkFlowClear = "none"

// bulkFlowAllowed whitelists the flow directives BulkAdjust accepts. Anything
// outside this set is treated as "" (leave flow untouched) so a malformed or
// hostile value can never be injected into a client's settings. The dropdown in
// ClientBulkAdjustModal.tsx offers the same set ("" / "none" / TLS_FLOW_CONTROL);
// keep the two in sync.
var bulkFlowAllowed = map[string]struct{}{
	"":                        {},
	bulkFlowClear:             {},
	"xtls-rprx-vision":        {},
	"xtls-rprx-vision-udp443": {},
}

// BulkAdjust shifts ExpiryTime by addDays (days) and TotalGB by addBytes
// for every email in the list. Clients whose corresponding field is
// unlimited (0) are skipped — bulk extend should not accidentally
// limit an unlimited client. addDays and addBytes may be negative.
//
// Work is grouped by inbound so the selected records can share one runtime
// reconciliation decision without loading the inbound's persisted settings.
func (s *ClientService) BulkAdjust(inboundSvc *InboundService, emails []string, addDays int, addBytes int64, flow string) (BulkAdjustResult, bool, error) {
	result := BulkAdjustResult{}
	if len(emails) == 0 {
		return result, false, nil
	}
	flow = strings.TrimSpace(flow)
	if _, ok := bulkFlowAllowed[flow]; !ok {
		flow = "" // ignore unknown directives — "" means "leave flow untouched"
	}
	adjustFlow := flow != ""
	if addDays == 0 && addBytes == 0 && !adjustFlow {
		return result, false, common.NewError("no adjustment specified")
	}

	addExpiryMs := int64(addDays) * 24 * 60 * 60 * 1000

	seen := map[string]struct{}{}
	cleanEmails := make([]string, 0, len(emails))
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		cleanEmails = append(cleanEmails, e)
	}
	if len(cleanEmails) == 0 {
		return result, false, nil
	}

	db := database.GetDB()

	var records []model.ClientRecord
	for _, batch := range chunkStrings(cleanEmails, sqlInChunk) {
		var rows []model.ClientRecord
		if err := db.Where("email IN ?", batch).Find(&rows).Error; err != nil {
			return result, false, err
		}
		records = append(records, rows...)
	}
	recordsByEmail := make(map[string]*model.ClientRecord, len(records))
	for i := range records {
		recordsByEmail[records[i].Email] = &records[i]
	}

	skippedReasons := map[string]string{}
	for _, email := range cleanEmails {
		if _, ok := recordsByEmail[email]; !ok {
			skippedReasons[email] = "client not found"
		}
	}

	plan := map[string]*bulkAdjustEntry{}
	for email, rec := range recordsByEmail {
		entry := &bulkAdjustEntry{record: rec}
		if addDays != 0 {
			switch {
			case rec.ExpiryTime == 0:
				if _, exists := skippedReasons[email]; !exists {
					skippedReasons[email] = "unlimited expiry"
				}
			case rec.ExpiryTime > 0:
				next := rec.ExpiryTime + addExpiryMs
				if next <= 0 {
					if _, exists := skippedReasons[email]; !exists {
						skippedReasons[email] = "reduction exceeds remaining time"
					}
				} else {
					entry.applyExpiry = true
					entry.newExpiry = next
				}
			default:
				next := rec.ExpiryTime - addExpiryMs
				if next >= 0 {
					if _, exists := skippedReasons[email]; !exists {
						skippedReasons[email] = "reduction exceeds delay window"
					}
				} else {
					entry.applyExpiry = true
					entry.newExpiry = next
				}
			}
		}
		if addBytes != 0 {
			if rec.TotalGB == 0 {
				if _, exists := skippedReasons[email]; !exists {
					skippedReasons[email] = "unlimited traffic"
				}
			} else {
				next := rec.TotalGB + addBytes
				if next <= 0 {
					if _, exists := skippedReasons[email]; !exists {
						skippedReasons[email] = "reduction exceeds remaining quota"
					}
				} else {
					entry.applyTotal = true
					entry.newTotal = next
				}
			}
		}
		if entry.applyExpiry || entry.applyTotal || adjustFlow {
			plan[email] = entry
		}
	}

	if len(plan) == 0 {
		for email, reason := range skippedReasons {
			result.Skipped = append(result.Skipped, BulkAdjustReport{Email: email, Reason: reason})
		}
		return result, false, nil
	}

	plannedIds := make([]int, 0, len(plan))
	recordIdToEmail := make(map[int]string, len(plan))
	for email, entry := range plan {
		plannedIds = append(plannedIds, entry.record.Id)
		recordIdToEmail[entry.record.Id] = email
	}

	var mappings []model.ClientInbound
	for _, batch := range chunkInts(plannedIds, sqlInChunk) {
		var rows []model.ClientInbound
		if err := db.Where("client_id IN ?", batch).Find(&rows).Error; err != nil {
			return result, false, err
		}
		mappings = append(mappings, rows...)
	}
	emailsByInbound := map[int][]string{}
	for _, m := range mappings {
		email, ok := recordIdToEmail[m.ClientId]
		if !ok {
			continue
		}
		emailsByInbound[m.InboundId] = append(emailsByInbound[m.InboundId], email)
	}

	needRestart := false
	flowHonored := map[string]bool{}
	flowIneligible := map[string]bool{}
	execFailed := map[string]bool{}
	for inboundId, ibEmails := range emailsByInbound {
		ibRes := s.bulkAdjustInboundClients(inboundSvc, inboundId, ibEmails, plan, flow)
		if ibRes.needRestart {
			needRestart = true
		}
		for email := range ibRes.flowHonored {
			flowHonored[email] = true
		}
		for email := range ibRes.flowIneligible {
			flowIneligible[email] = true
		}
		for email, reason := range ibRes.perEmailSkipped {
			execFailed[email] = true
			if _, already := skippedReasons[email]; !already {
				skippedReasons[email] = reason
			}
		}
	}

	cond, condArgs := depletedCond(db)
	candidateEmails := make([]string, 0, len(plan))
	for email, entry := range plan {
		if entry.applyExpiry || entry.applyTotal {
			candidateEmails = append(candidateEmails, email)
		}
	}
	wasDisabledDepleted := map[string]struct{}{}
	for _, batch := range chunkStrings(candidateEmails, sqlInChunk) {
		var rows []string
		if err := db.Model(xray.ClientTraffic{}).
			Where(cond+" AND enable = ? AND email IN ?", append(append([]any{}, condArgs...), false, batch)...).
			Pluck("email", &rows).Error; err != nil {
			return result, needRestart, err
		}
		for _, e := range rows {
			wasDisabledDepleted[e] = struct{}{}
		}
	}

	adjusted := map[string]struct{}{}
	for email, entry := range plan {
		if execFailed[email] {
			continue
		}
		updates := map[string]any{}
		if entry.applyExpiry {
			updates["expiry_time"] = entry.newExpiry
		}
		if entry.applyTotal {
			updates["total"] = entry.newTotal
		}
		if len(updates) > 0 {
			if err := db.Model(xray.ClientTraffic{}).Where("email = ?", email).Updates(updates).Error; err != nil {
				if _, already := skippedReasons[email]; !already {
					skippedReasons[email] = err.Error()
				}
				continue
			}
		}
		// Counted when expiry/total changed, or a flow directive was honored
		// for this client (flow lives in the inbound JSON, not ClientTraffic).
		if len(updates) > 0 || flowHonored[email] {
			adjusted[email] = struct{}{}
		}
	}
	result.Adjusted = len(adjusted)

	for email, reason := range skippedReasons {
		result.Skipped = append(result.Skipped, BulkAdjustReport{Email: email, Reason: reason})
	}
	// Report a flow directive that no inbound could carry — only when it was not
	// honored anywhere and the client has no other (expiry/total) skip reason.
	// The expiry/total part, if any, has already been applied and counted above.
	for email := range flowIneligible {
		if flowHonored[email] {
			continue
		}
		if _, already := skippedReasons[email]; already {
			continue
		}
		result.Skipped = append(result.Skipped, BulkAdjustReport{Email: email, Reason: "flow not supported on inbound"})
	}

	if len(wasDisabledDepleted) > 0 {
		stillDepleted := map[string]struct{}{}
		wasList := make([]string, 0, len(wasDisabledDepleted))
		for e := range wasDisabledDepleted {
			wasList = append(wasList, e)
		}
		for _, batch := range chunkStrings(wasList, sqlInChunk) {
			var rows []string
			if err := db.Model(xray.ClientTraffic{}).
				Where(cond+" AND email IN ?", append(append([]any{}, condArgs...), batch)...).
				Pluck("email", &rows).Error; err != nil {
				return result, needRestart, err
			}
			for _, e := range rows {
				stillDepleted[e] = struct{}{}
			}
		}
		reEnable := make([]string, 0, len(wasDisabledDepleted))
		for e := range wasDisabledDepleted {
			if _, still := stillDepleted[e]; !still {
				reEnable = append(reEnable, e)
			}
		}
		if len(reEnable) > 0 {
			_, nr, err := s.BulkSetEnable(inboundSvc, reEnable, true)
			if err != nil {
				return result, needRestart, err
			}
			if nr {
				needRestart = true
			}
		}
	}

	return result, needRestart, nil
}

type bulkInboundAdjustResult struct {
	perEmailSkipped map[string]string
	flowHonored     map[string]bool
	// flowIneligible is tracked apart from perEmailSkipped: a flow directive
	// that an inbound cannot carry must not suppress the expiry/total write for
	// the same client (which would diverge the inbound JSON / ClientRecord from
	// ClientTraffic). It only feeds the final Skipped report.
	flowIneligible map[string]bool
	needRestart    bool
}

// bulkAdjustInboundClients updates only the records and attachment rows named
// by the bulk request. It deliberately never reads inbound.Settings clients.
func (s *ClientService) bulkAdjustInboundClients(
	inboundSvc *InboundService,
	inboundId int,
	emails []string,
	plan map[string]*bulkAdjustEntry,
	flow string,
) bulkInboundAdjustResult {
	res := bulkInboundAdjustResult{perEmailSkipped: map[string]string{}, flowHonored: map[string]bool{}, flowIneligible: map[string]bool{}}
	defer lockInbound(inboundId).Unlock()
	inbound, err := inboundSvc.GetInbound(inboundId)
	if err != nil {
		for _, email := range emails {
			res.perEmailSkipped[email] = err.Error()
		}
		return res
	}
	flowEligible := flow == bulkFlowClear || inboundCanEnableTlsFlow(string(inbound.Protocol), inbound.StreamSettings, inbound.Settings)
	now := time.Now().UnixMilli()
	changed := make([]model.Client, 0, len(emails))
	flowChanged := false
	for _, email := range emails {
		entry := plan[email]
		if entry == nil {
			res.perEmailSkipped[email] = "client not found"
			continue
		}
		client := *entry.record.ToClient()
		if entry.applyExpiry {
			client.ExpiryTime = entry.newExpiry
		}
		if entry.applyTotal {
			client.TotalGB = entry.newTotal
		}
		if flow != "" {
			if !flowEligible {
				res.flowIneligible[email] = true
			} else {
				if flow == bulkFlowClear {
					client.Flow = ""
				} else {
					client.Flow = flow
				}
				res.flowHonored[email] = true
				flowChanged = flowChanged || client.Flow != entry.record.Flow
			}
		}
		client.UpdatedAt = now
		changed = append(changed, client)
	}
	if len(changed) == 0 {
		return res
	}
	if err := runSerializedTx(func(tx *gorm.DB) error {
		for _, client := range changed {
			updates := map[string]any{"updated_at": now}
			if entry := plan[client.Email]; entry.applyExpiry {
				updates["expiry_time"] = client.ExpiryTime
			}
			if entry := plan[client.Email]; entry.applyTotal {
				updates["total_gb"] = client.TotalGB
			}
			if _, honored := res.flowHonored[client.Email]; honored {
				updates["flow"] = client.Flow
				if err := tx.Model(&model.ClientInbound{}).Where("inbound_id = ? AND client_id = ?", inboundId, plan[client.Email].record.Id).Update("flow_override", client.Flow).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&model.ClientRecord{}).Where("id = ?", plan[client.Email].record.Id).Updates(updates).Error; err != nil {
				return err
			}
		}
		if inbound.NodeID != nil {
			return (&NodeService{}).MarkNodeDirtyTx(tx, *inbound.NodeID)
		}
		return nil
	}); err != nil {
		for _, client := range changed {
			res.perEmailSkipped[client.Email] = err.Error()
		}
		return res
	}
	if flowChanged && inbound.NodeID == nil {
		res.needRestart = true
	}
	if inbound.NodeID != nil && !flowChanged && len(changed) <= nodeBulkPushThreshold {
		rt, push, _, err := inboundSvc.nodePushPlan(inbound)
		if err != nil {
			for _, client := range changed {
				res.perEmailSkipped[client.Email] = err.Error()
			}
			return res
		}
		if push {
			for _, client := range changed {
				if err := rt.UpdateUser(context.Background(), inbound, client.Email, client); err != nil {
					res.needRestart = true
				}
			}
		}
	}
	return res
}

// BulkDeleteResult mirrors BulkAdjustResult: total deleted plus per-email
// skip reasons when an email could not be processed.
type BulkDeleteResult struct {
	Deleted int                `json:"deleted"`
	Skipped []BulkDeleteReport `json:"skipped,omitempty"`
}

type BulkDeleteReport struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

// BulkDelete removes every client in the list in one optimized pass.
// Instead of running the full single-delete pipeline N times (which would
// re-read, re-parse, and re-write each inbound's settings JSON for every
// email), it groups emails by inbound and performs a single
// read-modify-write per inbound. Per-row DB cleanups are also batched with
// IN-clause queries at the end. Errors on a particular email are recorded
// in the Skipped list and processing continues for the rest.
func (s *ClientService) BulkDelete(inboundSvc *InboundService, emails []string, keepTraffic bool) (BulkDeleteResult, bool, error) {
	result := BulkDeleteResult{}

	seen := map[string]struct{}{}
	cleanEmails := make([]string, 0, len(emails))
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		cleanEmails = append(cleanEmails, e)
	}
	if len(cleanEmails) == 0 {
		return result, false, nil
	}

	db := database.GetDB()

	var records []model.ClientRecord
	for _, batch := range chunkStrings(cleanEmails, sqlInChunk) {
		var rows []model.ClientRecord
		if err := db.Where("email IN ?", batch).Find(&rows).Error; err != nil {
			return result, false, err
		}
		records = append(records, rows...)
	}
	recordsByEmail := make(map[string]*model.ClientRecord, len(records))
	tombstoneEmails := make([]string, 0, len(records))
	for i := range records {
		recordsByEmail[records[i].Email] = &records[i]
		tombstoneEmails = append(tombstoneEmails, records[i].Email)
	}
	tombstoneClientEmails(tombstoneEmails)

	skippedReasons := map[string]string{}
	for _, email := range cleanEmails {
		if _, ok := recordsByEmail[email]; !ok {
			skippedReasons[email] = "client not found"
		}
	}

	clientIds := make([]int, 0, len(recordsByEmail))
	recordIdToEmail := make(map[int]string, len(recordsByEmail))
	for _, r := range recordsByEmail {
		clientIds = append(clientIds, r.Id)
		recordIdToEmail[r.Id] = r.Email
	}

	emailsByInbound := map[int][]string{}
	if len(clientIds) > 0 {
		var mappings []model.ClientInbound
		for _, batch := range chunkInts(clientIds, sqlInChunk) {
			var rows []model.ClientInbound
			if err := db.Where("client_id IN ?", batch).Find(&rows).Error; err != nil {
				return result, false, err
			}
			mappings = append(mappings, rows...)
		}
		for _, m := range mappings {
			email, ok := recordIdToEmail[m.ClientId]
			if !ok {
				continue
			}
			emailsByInbound[m.InboundId] = append(emailsByInbound[m.InboundId], email)
		}
	}

	needRestart := false
	for inboundId, ibEmails := range emailsByInbound {
		ibResult := s.bulkDelInboundClients(inboundSvc, inboundId, ibEmails, recordsByEmail, keepTraffic)
		if ibResult.needRestart {
			needRestart = true
		}
		for email, reason := range ibResult.perEmailSkipped {
			if _, already := skippedReasons[email]; !already {
				skippedReasons[email] = reason
			}
		}
	}

	successEmails := make([]string, 0, len(recordsByEmail))
	successIds := make([]int, 0, len(recordsByEmail))
	failedEmails := make([]string, 0, len(recordsByEmail))
	for email, rec := range recordsByEmail {
		if _, skipped := skippedReasons[email]; skipped {
			failedEmails = append(failedEmails, email)
			continue
		}
		successEmails = append(successEmails, email)
		successIds = append(successIds, rec.Id)
	}
	withdrawClientTombstones(failedEmails...)

	if len(successIds) > 0 {
		// Serialize the row cleanup against the traffic poll to avoid the
		// cross-transaction lock-order deadlock on client_traffics/inbounds.
		if err := runSerializedTx(func(tx *gorm.DB) error {
			if e := adjustGroupBaselinesForRemovedTraffic(tx, successEmails); e != nil {
				return e
			}
			for _, batch := range chunkInts(successIds, sqlInChunk) {
				if e := tx.Where("client_id IN ?", batch).Delete(&model.ClientInbound{}).Error; e != nil {
					return e
				}
				if e := tx.Where("client_id IN ?", batch).Delete(&model.ClientExternalLink{}).Error; e != nil {
					return e
				}
			}
			if !keepTraffic && len(successEmails) > 0 {
				for _, batch := range chunkStrings(successEmails, sqlInChunk) {
					if e := tx.Where("email IN ?", batch).Delete(&xray.ClientTraffic{}).Error; e != nil {
						return e
					}
					if e := tx.Where("client_email IN ?", batch).Delete(&model.InboundClientIps{}).Error; e != nil {
						return e
					}
				}
			}
			for _, batch := range chunkInts(successIds, sqlInChunk) {
				if e := tx.Where("id IN ?", batch).Delete(&model.ClientRecord{}).Error; e != nil {
					return e
				}
			}
			return nil
		}); err != nil {
			withdrawClientTombstones(successEmails...)
			return result, needRestart, err
		}
	}

	result.Deleted = len(successEmails)
	for email, reason := range skippedReasons {
		result.Skipped = append(result.Skipped, BulkDeleteReport{Email: email, Reason: reason})
	}
	return result, needRestart, nil
}

type bulkInboundDeleteResult struct {
	perEmailSkipped map[string]string
	needRestart     bool
}

// bulkDelInboundClients detaches normalized links and applies the smallest
// runtime delta for the selected rows. It never decodes persisted settings.
func (s *ClientService) bulkDelInboundClients(
	inboundSvc *InboundService,
	inboundId int,
	emails []string,
	records map[string]*model.ClientRecord,
	keepTraffic bool,
) bulkInboundDeleteResult {
	res := bulkInboundDeleteResult{perEmailSkipped: map[string]string{}}
	recs := make([]*model.ClientRecord, 0, len(emails))
	for _, email := range emails {
		record := records[email]
		if record == nil {
			res.perEmailSkipped[email] = "client not found"
			continue
		}
		recs = append(recs, record)
	}
	if len(recs) == 0 {
		return res
	}
	needRestart, err := s.delInboundClients(inboundSvc, inboundId, recs, keepTraffic, true)
	if err != nil {
		for _, record := range recs {
			res.perEmailSkipped[record.Email] = err.Error()
		}
		return res
	}
	res.needRestart = needRestart
	return res
}

// BulkCreateResult mirrors BulkAdjustResult for the create flow.
type BulkCreateResult struct {
	Created int                `json:"created"`
	Skipped []BulkCreateReport `json:"skipped,omitempty"`
}

type BulkCreateReport struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

func (s *ClientService) BulkCreate(inboundSvc *InboundService, payloads []ClientCreatePayload) (BulkCreateResult, bool, error) {
	result := BulkCreateResult{}
	if len(payloads) == 0 {
		return result, false, nil
	}

	skip := func(email, reason string) {
		if strings.TrimSpace(email) == "" {
			email = "(missing email)"
		}
		result.Skipped = append(result.Skipped, BulkCreateReport{Email: email, Reason: reason})
	}

	emailSubIDs, err := inboundSvc.getAllEmailSubIDs()
	if err != nil {
		emailSubIDs = nil
	}

	type prepared struct {
		client     model.Client
		inboundIds []int
	}
	prep := make([]prepared, 0, len(payloads))
	emails := make([]string, 0, len(payloads))
	subIDs := make([]string, 0, len(payloads))
	seenEmail := make(map[string]struct{}, len(payloads))
	seenSubID := make(map[string]string, len(payloads))

	for i := range payloads {
		client := payloads[i].Client
		email := strings.TrimSpace(client.Email)
		if email == "" {
			skip("", "client email is required")
			continue
		}
		if verr := validateClientEmail(email); verr != nil {
			skip(email, verr.Error())
			continue
		}
		if verr := validateClientSubID(client.SubID); verr != nil {
			skip(email, verr.Error())
			continue
		}
		if len(payloads[i].InboundIds) == 0 {
			skip(email, "at least one inbound is required")
			continue
		}

		client.Email = email
		if client.SubID == "" {
			client.SubID = uuid.NewString()
		}
		if !client.Enable {
			client.Enable = true
		}
		now := time.Now().UnixMilli()
		if client.CreatedAt == 0 {
			client.CreatedAt = now
		}
		client.UpdatedAt = now

		le := strings.ToLower(email)
		if _, dup := seenEmail[le]; dup {
			skip(email, "email already in use: "+email)
			continue
		}
		if owner, ok := seenSubID[client.SubID]; ok && owner != le {
			skip(email, "subId already in use: "+client.SubID)
			continue
		}
		seenEmail[le] = struct{}{}
		seenSubID[client.SubID] = le

		prep = append(prep, prepared{client: client, inboundIds: payloads[i].InboundIds})
		emails = append(emails, email)
		subIDs = append(subIDs, client.SubID)
	}

	if len(prep) == 0 {
		return result, false, nil
	}

	db := database.GetDB()
	const lookupChunk = 400
	existingByEmail := make(map[string]model.ClientRecord, len(emails))
	for start := 0; start < len(emails); start += lookupChunk {
		end := min(start+lookupChunk, len(emails))
		var rows []model.ClientRecord
		if e := db.Where("email IN ?", emails[start:end]).Find(&rows).Error; e != nil {
			return result, false, e
		}
		for i := range rows {
			existingByEmail[strings.ToLower(rows[i].Email)] = rows[i]
		}
	}
	existingSubOwner := make(map[string]string, len(subIDs))
	for start := 0; start < len(subIDs); start += lookupChunk {
		end := min(start+lookupChunk, len(subIDs))
		var rows []model.ClientRecord
		if e := db.Where("sub_id IN ?", subIDs[start:end]).Find(&rows).Error; e != nil {
			return result, false, e
		}
		for i := range rows {
			existingSubOwner[rows[i].SubID] = strings.ToLower(rows[i].Email)
		}
	}

	inboundCache := make(map[int]*model.Inbound)
	getIb := func(id int) (*model.Inbound, error) {
		if ib, ok := inboundCache[id]; ok {
			return ib, nil
		}
		ib, e := inboundSvc.GetInbound(id)
		if e != nil {
			return nil, e
		}
		inboundCache[id] = ib
		return ib, nil
	}

	byInbound := make(map[int][]model.Client)
	idxByInbound := make(map[int][]int)
	inboundOrder := make([]int, 0)
	failed := make([]bool, len(prep))
	reason := make([]string, len(prep))

	for idx := range prep {
		le := strings.ToLower(prep[idx].client.Email)
		if rec, ok := existingByEmail[le]; ok {
			if rec.SubID != prep[idx].client.SubID {
				failed[idx] = true
				reason[idx] = "email already in use: " + prep[idx].client.Email
				continue
			}
			if prep[idx].client.ID == "" {
				prep[idx].client.ID = rec.UUID
			}
			if prep[idx].client.Password == "" {
				prep[idx].client.Password = rec.Password
			}
			if prep[idx].client.Auth == "" {
				prep[idx].client.Auth = rec.Auth
			}
			if prep[idx].client.Secret == "" {
				prep[idx].client.Secret = rec.Secret
			}
		}
		if owner, ok := existingSubOwner[prep[idx].client.SubID]; ok && owner != le {
			failed[idx] = true
			reason[idx] = "subId already in use: " + prep[idx].client.SubID
			continue
		}

		ok := true
		for _, ibId := range prep[idx].inboundIds {
			ib, e := getIb(ibId)
			if e != nil {
				failed[idx] = true
				reason[idx] = e.Error()
				ok = false
				break
			}
			if e := s.fillProtocolDefaults(&prep[idx].client, ib); e != nil {
				failed[idx] = true
				reason[idx] = e.Error()
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, ibId := range prep[idx].inboundIds {
			ib, _ := getIb(ibId)
			if _, seen := byInbound[ibId]; !seen {
				inboundOrder = append(inboundOrder, ibId)
			}
			byInbound[ibId] = append(byInbound[ibId], clientWithInboundFlow(prep[idx].client, ib))
			idxByInbound[ibId] = append(idxByInbound[ibId], idx)
		}
	}

	needRestart := false
	for _, ibId := range inboundOrder {
		payload, e := json.Marshal(map[string][]model.Client{"clients": byInbound[ibId]})
		if e == nil {
			var nr bool
			nr, e = s.addInboundClient(inboundSvc, &model.Inbound{Id: ibId, Settings: string(payload)}, emailSubIDs)
			if e == nil && nr {
				needRestart = true
			}
		}
		if e != nil {
			for _, idx := range idxByInbound[ibId] {
				failed[idx] = true
				if reason[idx] == "" {
					reason[idx] = e.Error()
				}
			}
		}
	}

	for idx := range prep {
		if failed[idx] {
			skip(prep[idx].client.Email, reason[idx])
		} else {
			result.Created++
		}
	}
	return result, needRestart, nil
}

func (s *ClientService) DelDepleted(inboundSvc *InboundService) (int, bool, error) {
	db := database.GetDB()
	now := time.Now().UnixMilli()
	depletedClause := "reset = 0 and ((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?))"

	var rows []xray.ClientTraffic
	if err := db.Where(depletedClause, now).Find(&rows).Error; err != nil {
		return 0, false, err
	}
	if len(rows) == 0 {
		return 0, false, nil
	}

	seen := make(map[string]struct{}, len(rows))
	emails := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Email == "" {
			continue
		}
		if _, ok := seen[r.Email]; ok {
			continue
		}
		seen[r.Email] = struct{}{}
		emails = append(emails, r.Email)
	}
	if len(emails) == 0 {
		return 0, false, nil
	}

	res, needRestart, err := s.BulkDelete(inboundSvc, emails, false)
	if err != nil {
		return res.Deleted, needRestart, err
	}
	return res.Deleted, needRestart, nil
}

type BulkSetEnableResult struct {
	Changed int                   `json:"changed"`
	Skipped []BulkSetEnableReport `json:"skipped,omitempty"`
}

type BulkSetEnableReport struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

func (s *ClientService) BulkSetEnable(inboundSvc *InboundService, emails []string, enable bool) (BulkSetEnableResult, bool, error) {
	result := BulkSetEnableResult{}

	seen := map[string]struct{}{}
	cleanEmails := make([]string, 0, len(emails))
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		cleanEmails = append(cleanEmails, e)
	}
	if len(cleanEmails) == 0 {
		return result, false, nil
	}

	db := database.GetDB()

	var records []model.ClientRecord
	for _, batch := range chunkStrings(cleanEmails, sqlInChunk) {
		var rows []model.ClientRecord
		if err := db.Where("email IN ?", batch).Find(&rows).Error; err != nil {
			return result, false, err
		}
		records = append(records, rows...)
	}
	recordsByEmail := make(map[string]*model.ClientRecord, len(records))
	for i := range records {
		recordsByEmail[records[i].Email] = &records[i]
	}

	skippedReasons := map[string]string{}
	for _, email := range cleanEmails {
		if _, ok := recordsByEmail[email]; !ok {
			skippedReasons[email] = "client not found"
		}
	}

	clientIds := make([]int, 0, len(recordsByEmail))
	recordIdToEmail := make(map[int]string, len(recordsByEmail))
	for _, r := range recordsByEmail {
		clientIds = append(clientIds, r.Id)
		recordIdToEmail[r.Id] = r.Email
	}

	emailsByInbound := map[int][]string{}
	if len(clientIds) > 0 {
		var mappings []model.ClientInbound
		for _, batch := range chunkInts(clientIds, sqlInChunk) {
			var rows []model.ClientInbound
			if err := db.Where("client_id IN ?", batch).Find(&rows).Error; err != nil {
				return result, false, err
			}
			mappings = append(mappings, rows...)
		}
		for _, m := range mappings {
			email, ok := recordIdToEmail[m.ClientId]
			if !ok {
				continue
			}
			emailsByInbound[m.InboundId] = append(emailsByInbound[m.InboundId], email)
		}
	}

	needRestart := false
	for inboundId, ibEmails := range emailsByInbound {
		ibRes := s.bulkSetEnableInboundClients(inboundSvc, inboundId, ibEmails, enable)
		if ibRes.needRestart {
			needRestart = true
		}
		for email, reason := range ibRes.perEmailSkipped {
			if _, already := skippedReasons[email]; !already {
				skippedReasons[email] = reason
			}
		}
	}

	successEmails := make([]string, 0, len(recordsByEmail))
	for email := range recordsByEmail {
		if _, skipped := skippedReasons[email]; skipped {
			continue
		}
		successEmails = append(successEmails, email)
	}

	if len(successEmails) > 0 {
		now := time.Now().UnixMilli()
		if err := runSerializedTx(func(tx *gorm.DB) error {
			for _, batch := range chunkStrings(successEmails, sqlInChunk) {
				if e := tx.Model(xray.ClientTraffic{}).Where("email IN ?", batch).Update("enable", enable).Error; e != nil {
					return e
				}
				if e := tx.Model(&model.ClientRecord{}).Where("email IN ?", batch).
					Updates(map[string]any{"enable": enable, "updated_at": now}).Error; e != nil {
					return e
				}
			}
			return nil
		}); err != nil {
			return result, needRestart, err
		}
	}

	result.Changed = len(successEmails)
	for email, reason := range skippedReasons {
		result.Skipped = append(result.Skipped, BulkSetEnableReport{Email: email, Reason: reason})
	}
	return result, needRestart, nil
}

type bulkSetEnableInboundResult struct {
	perEmailSkipped map[string]string
	needRestart     bool
}

func (s *ClientService) bulkSetEnableInboundClients(inboundSvc *InboundService, inboundId int, emails []string, enable bool) bulkSetEnableInboundResult {
	res := bulkSetEnableInboundResult{perEmailSkipped: map[string]string{}}
	defer lockInbound(inboundId).Unlock()
	inbound, err := inboundSvc.GetInbound(inboundId)
	if err != nil {
		for _, email := range emails {
			res.perEmailSkipped[email] = err.Error()
		}
		return res
	}
	attached, err := s.EmailsAlreadyOnInbound(nil, inboundId, emails)
	if err != nil {
		for _, email := range emails {
			res.perEmailSkipped[email] = err.Error()
		}
		return res
	}
	changed := make([]model.Client, 0, len(emails))
	for _, email := range emails {
		if _, ok := attached[strings.ToLower(strings.TrimSpace(email))]; !ok {
			res.perEmailSkipped[email] = "Client Not Found In Inbound"
			continue
		}
		record, err := s.GetRecordByEmail(nil, email)
		if err != nil {
			res.perEmailSkipped[email] = err.Error()
			continue
		}
		if record.Enable == enable {
			continue
		}
		client := *record.ToClient()
		client.Enable = enable
		client.UpdatedAt = time.Now().UnixMilli()
		changed = append(changed, client)
	}
	if len(changed) == 0 {
		return res
	}
	if err := runSerializedTx(func(tx *gorm.DB) error {
		for _, client := range changed {
			if err := tx.Model(&model.ClientRecord{}).
				Where("email = ?", client.Email).
				Updates(map[string]any{"enable": enable, "updated_at": client.UpdatedAt}).Error; err != nil {
				return err
			}
			if err := tx.Model(&xray.ClientTraffic{}).
				Where("email = ?", client.Email).
				Update("enable", enable).Error; err != nil {
				return err
			}
		}
		if inbound.NodeID != nil {
			return (&NodeService{}).MarkNodeDirtyTx(tx, *inbound.NodeID)
		}
		return nil
	}); err != nil {
		for _, client := range changed {
			res.perEmailSkipped[client.Email] = err.Error()
		}
		return res
	}
	if inbound.NodeID != nil {
		return res
	}
	rt, push, _, err := inboundSvc.nodePushPlan(inbound)
	if err != nil || !push {
		res.needRestart = true
		return res
	}
	cipher := ""
	if inbound.Protocol == model.Shadowsocks {
		cipher = shadowsocksMethodFromSettings(inbound.Settings)
	}
	for _, client := range changed {
		if enable {
			if err := rt.AddUser(context.Background(), inbound, apiUserFromClient(map[string]any{
				"email": client.Email, "id": client.ID, "auth": client.Auth, "security": client.Security,
				"flow": client.Flow, "password": client.Password, "cipher": cipher,
			}, "")); err != nil {
				res.needRestart = true
			}
			continue
		}
		if err := rt.RemoveUser(context.Background(), inbound, client.Email); err != nil && !strings.Contains(err.Error(), fmt.Sprintf("User %s not found.", client.Email)) {
			res.needRestart = true
		}
	}
	return res
}
