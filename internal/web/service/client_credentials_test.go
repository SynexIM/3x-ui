package service

import (
	"context"
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestCredentialPatchKeepsProtocolSecretsIndependent(t *testing.T) {
	setupBulkDB(t)
	svc, ibSvc := &ClientService{}, &InboundService{}
	ib := mkInbound(t, 26111, model.Hysteria, `{"version":2}`)
	record := model.ClientRecord{Email: "independent@x", Password: "old-password", Auth: "old-auth", MixedUser: "mixed-user", MixedPass: "mixed-pass"}
	if err := database.GetDB().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientInbound{ClientId: record.Id, InboundId: ib.Id}).Error; err != nil {
		t.Fatal(err)
	}
	password, auth := "new-password", "new-auth"
	for _, patch := range []ClientCredentialPatch{{Password: &password}, {Auth: &auth}} {
		if err := svc.UpdateCredentials(context.Background(), ibSvc, record.Email, patch); err != nil {
			t.Fatal(err)
		}
		if err := database.GetDB().First(&record, record.Id).Error; err != nil {
			t.Fatal(err)
		}
		wantAuth := "old-auth"
		if patch.Auth != nil {
			wantAuth = auth
		}
		if record.Password != password || record.Auth != wantAuth || record.MixedUser != "mixed-user" || record.MixedPass != "mixed-pass" {
			t.Fatalf("credentials after patch %+v: password=%q auth=%q mixed_user=%q mixed_pass=%q", patch, record.Password, record.Auth, record.MixedUser, record.MixedPass)
		}
	}
	other := model.ClientRecord{Email: "other@x", Auth: "taken-auth"}
	if err := database.GetDB().Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientInbound{ClientId: other.Id, InboundId: ib.Id}).Error; err != nil {
		t.Fatal(err)
	}
	err := svc.UpdateCredentials(context.Background(), ibSvc, record.Email, ClientCredentialPatch{Auth: &other.Auth})
	if !errors.Is(err, ErrClientCredentialConflict) {
		t.Fatalf("auth conflict = %v", err)
	}
	if err := database.GetDB().First(&record, record.Id).Error; err != nil {
		t.Fatal(err)
	}
	if record.Auth != auth {
		t.Fatalf("conflict changed auth to %q", record.Auth)
	}
}

func TestCredentialPatchRejectsEmptyMixedFields(t *testing.T) {
	empty := ""
	for _, patch := range []ClientCredentialPatch{{MixedUser: &empty}, {MixedPass: &empty}} {
		err := (&ClientService{}).UpdateCredentials(context.Background(), &InboundService{}, "unset@x", patch)
		if !errors.Is(err, ErrClientCredentialInvalid) {
			t.Fatalf("patch %+v: err=%v", patch, err)
		}
	}
}

func TestMixedHTTPClientWritesRequireIndependentCredentials(t *testing.T) {
	setupBulkDB(t)
	svc, ibSvc := &ClientService{}, &InboundService{}
	for i, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			ib := mkInbound(t, 26112+i, protocol, `{}`)
			client := model.Client{Email: string(protocol) + "@x", Password: "unrelated-password", MixedUser: "independent", MixedPass: "independent-pass", Enable: true}
			for _, missingUser := range []bool{true, false} {
				invalid := client
				if missingUser {
					invalid.MixedUser = ""
				} else {
					invalid.MixedPass = ""
				}
				if _, err := svc.CreateOne(ibSvc, ib.Id, invalid); !errors.Is(err, ErrClientCredentialInvalid) {
					t.Fatalf("create empty credential: %v", err)
				}
			}
			if _, err := svc.CreateOne(ibSvc, ib.Id, client); err != nil {
				t.Fatal(err)
			}
			row, err := svc.GetRecordByEmail(nil, client.Email)
			if err != nil {
				t.Fatal(err)
			}
			for _, missingUser := range []bool{true, false} {
				invalid := client
				if missingUser {
					invalid.MixedUser = ""
				} else {
					invalid.MixedPass = ""
				}
				if _, err := svc.Update(ibSvc, row.Id, invalid, 0); !errors.Is(err, ErrClientCredentialInvalid) {
					t.Fatalf("update empty credential: %v", err)
				}
			}
			client.MixedUser, client.MixedPass = "rotated-user", "rotated-pass"
			if _, err := svc.Update(ibSvc, row.Id, client, 0); err != nil {
				t.Fatal(err)
			}
			row, err = svc.GetRecordByEmail(nil, client.Email)
			if err != nil {
				t.Fatal(err)
			}
			if row.MixedUser != client.MixedUser || row.MixedPass != client.MixedPass || row.Password != client.Password {
				t.Fatalf("stored independent credentials = %+v", row)
			}
		})
	}
}
