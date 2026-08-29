package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
)

func TestClientLifecycleDoesNotRewriteInboundSettings(t *testing.T) {
	setupBulkDB(t)
	clientSvc := &ClientService{}
	inboundSvc := &InboundService{}
	inbound := mkInbound(t, 54201, model.VLESS, `{"clients":[],"decryption":"none"}`)
	before := inbound.Settings

	email := "normalized@example.com"
	if _, err := clientSvc.Create(inboundSvc, &ClientCreatePayload{
		Client: model.Client{
			Email:  email,
			ID:     "11111111-1111-1111-1111-111111111111",
			SubID:  email,
			Enable: true,
		},
		InboundIds: []int{inbound.Id},
	}); err != nil {
		t.Fatalf("create normalized client: %v", err)
	}
	reloaded, err := inboundSvc.GetInbound(inbound.Id)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Settings != before {
		t.Fatalf("normalized create rewrote inbound settings: %s -> %s", before, reloaded.Settings)
	}
	linked, err := clientSvc.ListForInbound(nil, inbound.Id)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 1 || linked[0].Email != email {
		t.Fatalf("normalized membership = %+v, want %s", linked, email)
	}
	built, err := inboundSvc.buildInboundForLocalRuntime(database.GetDB(), reloaded)
	if err != nil {
		t.Fatalf("build runtime clone: %v", err)
	}
	if !strings.Contains(built.Settings, email) {
		t.Fatalf("runtime clone omitted normalized client %q: %s", email, built.Settings)
	}

	rec, err := clientSvc.GetRecordByEmail(nil, email)
	if err != nil {
		t.Fatal(err)
	}
	updated := rec.ToClient()
	updated.Comment = "updated without settings JSON"
	updated.BandwidthBps = 25_000_000
	if _, err := clientSvc.Update(inboundSvc, rec.Id, *updated); err != nil {
		t.Fatalf("update normalized client: %v", err)
	}
	got, err := clientSvc.GetRecordByEmail(nil, email)
	if err != nil {
		t.Fatal(err)
	}
	if got.Comment != updated.Comment || got.BandwidthBps != updated.BandwidthBps {
		t.Fatalf("normalized update not persisted: %+v", got)
	}

	if _, err := clientSvc.Delete(inboundSvc, rec.Id, false); err != nil {
		t.Fatalf("delete normalized client: %v", err)
	}
	if _, err := clientSvc.GetRecordByEmail(nil, email); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted normalized client lookup = %v, want record not found", err)
	}
	reloaded, err = inboundSvc.GetInbound(inbound.Id)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Settings != before {
		t.Fatalf("normalized lifecycle rewrote inbound settings: %s -> %s", before, reloaded.Settings)
	}
}
