package service

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

// applyClientRecordMerge merges incoming client-record fields onto row using the
// same rules everywhere a client record is persisted: scalar quota / lifecycle /
// subscription fields are applied unconditionally (so clearing them takes
// effect), while credentials and identifiers are only overwritten when the
// incoming value is non-empty (so a partial update preserves the stored UUID /
// password / keys). CreatedAt keeps the earliest known value. Email, UpdatedAt,
// and the Id primary key are intentionally not touched here — callers handle
// those separately. Shared by SyncInbound (per-inbound persistence) and Update
// (the no-attached-inbound fallback) so the two paths cannot diverge.
func applyClientRecordMerge(row *model.ClientRecord, incoming *model.ClientRecord) {
	if incoming.UUID != "" {
		row.UUID = incoming.UUID
	}
	if incoming.Password != "" {
		row.Password = incoming.Password
	}
	if incoming.Auth != "" {
		row.Auth = incoming.Auth
	}
	if incoming.Secret != "" {
		row.Secret = incoming.Secret
	}
	if incoming.AdTag != "" {
		row.AdTag = incoming.AdTag
	}
	row.Flow = incoming.Flow
	if incoming.Security != "" {
		row.Security = incoming.Security
	}
	if incoming.Reverse != "" {
		row.Reverse = incoming.Reverse
	}
	if incoming.PrivateKey != "" {
		row.PrivateKey = incoming.PrivateKey
	}
	if incoming.PublicKey != "" {
		row.PublicKey = incoming.PublicKey
	}
	if incoming.AllowedIPs != "" {
		row.AllowedIPs = incoming.AllowedIPs
	}
	row.PreSharedKey = incoming.PreSharedKey
	row.KeepAlive = incoming.KeepAlive
	row.SubID = incoming.SubID
	row.LimitIP = incoming.LimitIP
	// Unconditional, like the other quota scalars: clearing a limit in the form
	// must actually clear it (blank = unlimited).
	row.BandwidthBps = incoming.BandwidthBps
	row.CommittedBps = incoming.CommittedBps
	row.CommittedBurstBytes = incoming.CommittedBurstBytes
	row.ConnLimit = incoming.ConnLimit
	row.RateUnit = incoming.RateUnit
	row.BurstUnit = incoming.BurstUnit
	if incoming.EgressTag != "" {
		row.EgressTag = incoming.EgressTag
	}
	row.TotalGB = incoming.TotalGB
	row.ExpiryTime = incoming.ExpiryTime
	row.Enable = incoming.Enable
	row.TgID = incoming.TgID
	if incoming.Group != "" {
		row.Group = incoming.Group
	}
	row.Comment = incoming.Comment
	row.Reset = incoming.Reset
	if incoming.CreatedAt > 0 && (row.CreatedAt == 0 || incoming.CreatedAt < row.CreatedAt) {
		row.CreatedAt = incoming.CreatedAt
	}
}

func (s *ClientService) SyncInbound(tx *gorm.DB, inboundId int, clients []model.Client) error {
	if tx == nil {
		tx = database.GetDB()
	}

	if err := tx.Where("inbound_id = ?", inboundId).Delete(&model.ClientInbound{}).Error; err != nil {
		return err
	}

	emails := make([]string, 0, len(clients))
	seen := make(map[string]struct{}, len(clients))
	for i := range clients {
		email := strings.TrimSpace(clients[i].Email)
		if email == "" {
			continue
		}
		if _, ok := seen[email]; ok {
			continue
		}
		seen[email] = struct{}{}
		emails = append(emails, email)
	}

	existing := make(map[string]*model.ClientRecord, len(emails))
	const selectChunk = 400
	for start := 0; start < len(emails); start += selectChunk {
		end := min(start+selectChunk, len(emails))
		var rows []model.ClientRecord
		if err := tx.Where("email IN ?", emails[start:end]).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			r := rows[i]
			existing[r.Email] = &r
		}
	}

	idByEmail := make(map[string]int, len(emails))
	pending := make(map[string]*model.ClientRecord, len(emails))
	toCreate := make([]*model.ClientRecord, 0, len(emails))
	for i := range clients {
		email := strings.TrimSpace(clients[i].Email)
		if email == "" {
			continue
		}

		incoming := clients[i].ToRecord()
		// ToRecord copies the raw email; store the trimmed key this function
		// looks up by, or a padded email is inserted and never found again.
		incoming.Email = email
		row, ok := existing[email]
		if !ok {
			if _, dup := pending[email]; !dup {
				pending[email] = incoming
				toCreate = append(toCreate, incoming)
			}
			continue
		}

		before := *row
		applyClientRecordMerge(row, incoming)
		preservedUpdatedAt := max(incoming.UpdatedAt, row.UpdatedAt)
		row.UpdatedAt = preservedUpdatedAt

		idByEmail[email] = row.Id

		if *row == before {
			continue
		}
		if err := tx.Save(row).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ClientRecord{}).
			Where("id = ?", row.Id).
			UpdateColumn("updated_at", preservedUpdatedAt).Error; err != nil {
			return err
		}
	}

	if err := createClientRecords(tx, toCreate); err != nil {
		return err
	}
	for _, rec := range toCreate {
		idByEmail[rec.Email] = rec.Id
	}

	links := make([]model.ClientInbound, 0, len(clients))
	linked := make(map[int]struct{}, len(clients))
	for i := range clients {
		email := strings.TrimSpace(clients[i].Email)
		if email == "" {
			continue
		}
		id, ok := idByEmail[email]
		if !ok {
			continue
		}
		if _, dup := linked[id]; dup {
			continue
		}
		linked[id] = struct{}{}
		links = append(links, model.ClientInbound{
			ClientId:     id,
			InboundId:    inboundId,
			FlowOverride: clients[i].Flow,
		})
	}
	if len(links) > 0 {
		if err := tx.CreateInBatches(links, 200).Error; err != nil {
			return err
		}
	}
	return nil
}

// createClientRecords inserts the new client rows and re-asserts enable=false
// for the ones the caller asked to create disabled.
//
// The re-assert is not belt-and-braces. `Enable` carries `gorm:"default:true"`,
// so GORM leaves the Go zero value out of the INSERT to let the column default
// apply — and then loads that applied default back into the struct. By the time
// Save returns, every rec.Enable reads true, the ones created disabled
// included. So who was disabled has to be remembered **before** the write:
// reading it back afterwards is reading GORM's answer, not the caller's.
func createClientRecords(tx *gorm.DB, toCreate []*model.ClientRecord) error {
	if len(toCreate) == 0 {
		return nil
	}
	disabled := make([]*model.ClientRecord, 0)
	for _, rec := range toCreate {
		if !rec.Enable {
			disabled = append(disabled, rec)
		}
	}
	if err := tx.Session(&gorm.Session{CreateBatchSize: 200}).Save(&toCreate).Error; err != nil {
		return err
	}
	if len(disabled) == 0 {
		return nil
	}
	ids := make([]int, 0, len(disabled))
	for _, rec := range disabled {
		// Put the caller's value back on the struct too: callers read these
		// rows after the call, and a silently flipped field is exactly the
		// bug this function exists to stop.
		rec.Enable = false
		ids = append(ids, rec.Id)
	}
	return tx.Model(&model.ClientRecord{}).Where("id IN ?", ids).UpdateColumn("enable", false).Error
}

// SyncInboundAdd links a handful of clients to an inbound **without touching the
// ones already there**.
//
// SyncInbound is a full replace: it deletes every client_inbounds row for the
// inbound and rebuilds all of them. That is right when the caller knows the
// complete membership (an inbound edit, an import), and wrong for "add one
// client" — the cost of adding the 50,001st client is then the cost of
// rewriting 50,001 links. Measured on a real core with 50k clients on one
// inbound: the full replace takes 2.3s cold / 406ms warm inside the
// transaction, and it is the single largest component of one client add.
//
// Removals are not this function's job. An add never removes anyone, so the
// delete-all in the full replace is pure cost here. Drift in rows this call did
// not touch is repaired by the periodic full pass, not by making every write
// pay for a sweep — the same argument that moved provisioning from whole-config
// pushes to single primitives.
func (s *ClientService) SyncInboundAdd(tx *gorm.DB, inboundId int, clients []model.Client) error {
	if tx == nil {
		tx = database.GetDB()
	}
	if len(clients) == 0 {
		return nil
	}

	emails := make([]string, 0, len(clients))
	seen := make(map[string]struct{}, len(clients))
	for i := range clients {
		email := strings.TrimSpace(clients[i].Email)
		if email == "" {
			continue
		}
		if _, dup := seen[email]; dup {
			continue
		}
		seen[email] = struct{}{}
		emails = append(emails, email)
	}
	if len(emails) == 0 {
		return nil
	}

	// One indexed lookup over the emails being added, not over every email on
	// the inbound. This is the whole point of the function.
	var rows []model.ClientRecord
	if err := tx.Where("email IN ?", emails).Find(&rows).Error; err != nil {
		return err
	}
	existing := make(map[string]*model.ClientRecord, len(rows))
	for i := range rows {
		existing[rows[i].Email] = &rows[i]
	}

	idByEmail := make(map[string]int, len(emails))
	pending := make(map[string]*model.ClientRecord, len(emails))
	toCreate := make([]*model.ClientRecord, 0, len(emails))
	for i := range clients {
		email := strings.TrimSpace(clients[i].Email)
		if email == "" {
			continue
		}
		incoming := clients[i].ToRecord()
		// ToRecord copies the raw email; store the trimmed key this function
		// looks up by, or a padded email is inserted and never found again.
		incoming.Email = email
		row, ok := existing[email]
		if !ok {
			if _, dup := pending[email]; !dup {
				pending[email] = incoming
				toCreate = append(toCreate, incoming)
			}
			continue
		}
		before := *row
		applyClientRecordMerge(row, incoming)
		preservedUpdatedAt := max(incoming.UpdatedAt, row.UpdatedAt)
		row.UpdatedAt = preservedUpdatedAt
		idByEmail[email] = row.Id
		if *row == before {
			continue
		}
		if err := tx.Save(row).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ClientRecord{}).
			Where("id = ?", row.Id).
			UpdateColumn("updated_at", preservedUpdatedAt).Error; err != nil {
			return err
		}
	}

	if err := createClientRecords(tx, toCreate); err != nil {
		return err
	}
	for _, rec := range toCreate {
		idByEmail[rec.Email] = rec.Id
	}

	// Which of these are already linked. Scoped to the ids being added, so the
	// query does not grow with the inbound.
	ids := make([]int, 0, len(idByEmail))
	for _, id := range idByEmail {
		ids = append(ids, id)
	}
	linked := make(map[int]struct{}, len(ids))
	if len(ids) > 0 {
		var already []int
		if err := tx.Model(&model.ClientInbound{}).
			Where("inbound_id = ? AND client_id IN ?", inboundId, ids).
			Pluck("client_id", &already).Error; err != nil {
			return err
		}
		for _, id := range already {
			linked[id] = struct{}{}
		}
	}

	links := make([]model.ClientInbound, 0, len(clients))
	for i := range clients {
		email := strings.TrimSpace(clients[i].Email)
		if email == "" {
			continue
		}
		id, ok := idByEmail[email]
		if !ok {
			continue
		}
		if _, dup := linked[id]; dup {
			continue
		}
		linked[id] = struct{}{}
		links = append(links, model.ClientInbound{
			ClientId:     id,
			InboundId:    inboundId,
			FlowOverride: clients[i].Flow,
		})
	}
	if len(links) > 0 {
		if err := tx.CreateInBatches(links, 200).Error; err != nil {
			return err
		}
	}
	return nil
}

// EmailsAlreadyOnInbound reports which of the given emails are already linked to
// the inbound, asking only about those emails.
//
// The caller used to answer this by parsing the inbound's whole settings blob and
// building a set of every email on it — 70ms at 50k clients, to decide something
// about one email. The normalized tables answer the same question with an index
// seek, and they are the side the running Xray users are built from.
//
// Returns lowercased emails, matching how the caller compares them.
func (s *ClientService) EmailsAlreadyOnInbound(tx *gorm.DB, inboundId int, emails []string) (map[string]struct{}, error) {
	if tx == nil {
		tx = database.GetDB()
	}
	out := make(map[string]struct{}, len(emails))
	wanted := make([]string, 0, len(emails))
	for _, email := range emails {
		trimmed := strings.TrimSpace(email)
		if trimmed != "" {
			wanted = append(wanted, trimmed)
		}
	}
	if len(wanted) == 0 {
		return out, nil
	}
	var found []string
	if err := tx.Model(&model.ClientInbound{}).
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("client_inbounds.inbound_id = ? AND clients.email IN ?", inboundId, wanted).
		Pluck("clients.email", &found).Error; err != nil {
		return nil, err
	}
	for _, email := range found {
		out[strings.ToLower(email)] = struct{}{}
	}
	return out, nil
}

func (s *ClientService) DetachInbound(tx *gorm.DB, inboundId int) error {
	if tx == nil {
		tx = database.GetDB()
	}
	return tx.Where("inbound_id = ?", inboundId).Delete(&model.ClientInbound{}).Error
}

func (s *ClientService) ListForInbound(tx *gorm.DB, inboundId int) ([]model.Client, error) {
	if tx == nil {
		tx = database.GetDB()
	}
	type joinedRow struct {
		model.ClientRecord
		FlowOverride string
	}
	var rows []joinedRow
	err := tx.Table("clients").
		Select("clients.*, client_inbounds.flow_override AS flow_override").
		Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").
		Where("client_inbounds.inbound_id = ?", inboundId).
		Order("clients.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]model.Client, 0, len(rows))
	for i := range rows {
		c := rows[i].ToClient()
		c.Flow = rows[i].FlowOverride
		out = append(out, *c)
	}
	return out, nil
}

// ListForInboundBySubId is ListForInbound narrowed to one subscription id —
// both filter columns are indexed, so the subscription server resolves a
// subscriber's clients without touching the inbound's settings JSON.
func (s *ClientService) ListForInboundBySubId(tx *gorm.DB, inboundId int, subId string) ([]model.Client, error) {
	if tx == nil {
		tx = database.GetDB()
	}
	type joinedRow struct {
		model.ClientRecord
		FlowOverride string
	}
	var rows []joinedRow
	err := tx.Table("clients").
		Select("clients.*, client_inbounds.flow_override AS flow_override").
		Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").
		Where("client_inbounds.inbound_id = ? AND clients.sub_id = ?", inboundId, subId).
		Order("clients.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]model.Client, 0, len(rows))
	for i := range rows {
		c := rows[i].ToClient()
		c.Flow = rows[i].FlowOverride
		out = append(out, *c)
	}
	return out, nil
}
