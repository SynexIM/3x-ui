package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Delete tombstones up front and keeps the record when an inbound fails. A
// surviving tombstone lets the next node merge finish the refused deletion.
func TestFailedDeleteWithdrawsTombstone(t *testing.T) {
	setupBulkDB(t)
	svc := &ClientService{}
	inboundSvc := &InboundService{}
	db := database.GetDB()

	const email = "retry@x"
	broken := mkInbound(t, 30401, model.VLESS, `{"clients":[]}`)
	seedNormalizedInbound(t, broken, []model.Client{{Email: email, Enable: true, ID: "33333333-3333-3333-3333-333333333333"}})
	rec := lookupClientRecord(t, email)
	if err := db.Model(&model.Inbound{}).Where("id = ?", broken.Id).Update("node_id", 9999).Error; err != nil {
		t.Fatalf("point fixture at missing node: %v", err)
	}

	t.Cleanup(func() { withdrawClientTombstones(email) })

	if _, err := svc.Delete(inboundSvc, rec.Id, false); err == nil {
		t.Fatal("setup: delete was expected to fail while the node is unavailable")
	}

	var surviving int64
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).Count(&surviving).Error; err != nil {
		t.Fatalf("count clients: %v", err)
	}
	if surviving != 1 {
		t.Fatalf("failed delete must keep the record for a retry, got %d rows", surviving)
	}
	if isClientEmailTombstoned(email) {
		t.Fatal("delete kept the record but left a live tombstone: the next node sync would finish the deletion it refused")
	}
}
