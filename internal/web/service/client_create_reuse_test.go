package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func inboundSettingsEqual(t *testing.T, inboundSvc *InboundService, inboundId int, want string) {
	t.Helper()
	ib, err := inboundSvc.GetInbound(inboundId)
	if err != nil {
		t.Fatalf("GetInbound %d: %v", inboundId, err)
	}
	if ib.Settings != want {
		t.Fatalf("inbound %d settings changed: got %q, want %q", inboundId, ib.Settings, want)
	}
}

func TestCreateRepeatKeepsExistingUUID(t *testing.T) {
	setupBulkDB(t)
	svc := &ClientService{}
	inboundSvc := &InboundService{}

	const settings = `{"clients":[]}`
	ibA := mkInbound(t, 21001, model.VLESS, settings)
	ibB := mkInbound(t, 21002, model.VLESS, settings)

	const originalUUID = "aaaaaaaa-1111-2222-3333-444444444444"
	if _, err := svc.Create(inboundSvc, &ClientCreatePayload{
		Client:     model.Client{Email: "repeat@x", ID: originalUUID, SubID: "sub-repeat", Enable: true},
		InboundIds: []int{ibA.Id},
	}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if rec := lookupClientRecord(t, "repeat@x"); rec.UUID != originalUUID {
		t.Fatalf("record UUID after first Create = %q, want %q", rec.UUID, originalUUID)
	}

	if _, err := svc.Create(inboundSvc, &ClientCreatePayload{
		Client:     model.Client{Email: "repeat@x", SubID: "sub-repeat", Enable: true},
		InboundIds: []int{ibB.Id},
	}); err != nil {
		t.Fatalf("repeat Create: %v", err)
	}

	if rec := lookupClientRecord(t, "repeat@x"); rec.UUID != originalUUID {
		t.Fatalf("record UUID after repeat Create = %q, want %q", rec.UUID, originalUUID)
	}
	inboundSettingsEqual(t, inboundSvc, ibA.Id, settings)
	inboundSettingsEqual(t, inboundSvc, ibB.Id, settings)
	for _, ib := range []*model.Inbound{ibA, ibB} {
		clients, err := svc.ListForInbound(nil, ib.Id)
		if err != nil {
			t.Fatalf("ListForInbound(%d): %v", ib.Id, err)
		}
		if len(clients) != 1 || clients[0].ID != originalUUID {
			t.Fatalf("inbound %d normalized client = %+v, want UUID %q", ib.Id, clients, originalUUID)
		}
	}
}
