package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A create must never silently take over another client's login on an inbound.
func TestCreateRefusesACredentialAnotherClientHoldsOnTheInbound(t *testing.T) {
	setupBulkDB(t)
	svc := &ClientService{}
	inboundSvc := &InboundService{}
	vless := mkInbound(t, 26101, model.VLESS, `{"clients":[]}`)
	mixed := mkInbound(t, 26102, model.Mixed, `{"clients":[]}`)
	const id = "3f1d2c4b-5a6e-4f70-8a9b-0c1d2e3f4a5b"

	if _, err := svc.Create(inboundSvc, &ClientCreatePayload{
		Client:     model.Client{Email: "owner@x", ID: id, Password: "Owner_pass1", MixedUser: "login1", Enable: true},
		InboundIds: []int{vless.Id, mixed.Id},
	}); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	for name, client := range map[string]model.Client{
		"same uuid":        {Email: "thief1@x", ID: id, Enable: true},
		"same mixed login": {Email: "thief2@x", MixedUser: "login1", Password: "Other_pass2", Enable: true},
	} {
		_, err := svc.Create(inboundSvc, &ClientCreatePayload{Client: client, InboundIds: []int{vless.Id, mixed.Id}})
		if !errors.Is(err, ErrClientCredentialConflict) {
			t.Fatalf("%s: err = %v, want CLIENT_CREDENTIAL_CONFLICT", name, err)
		}
	}

	res, _, err := svc.BulkCreate(inboundSvc, []ClientCreatePayload{
		{Client: model.Client{Email: "b1@x", ID: "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d", Enable: true}, InboundIds: []int{vless.Id}},
		{Client: model.Client{Email: "b2@x", ID: "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d", Enable: true}, InboundIds: []int{vless.Id}},
	})
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	if res.Created != 1 || len(res.Skipped) != 1 || !strings.HasPrefix(res.Skipped[0].Reason, ErrClientCredentialConflict.Error()) {
		t.Fatalf("BulkCreate = %+v, want the second same-uuid client refused as a conflict", res)
	}
}
