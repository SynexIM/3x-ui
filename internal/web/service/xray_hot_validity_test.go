package service

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestReconcileClientValidityScopesExpiryAndRenewalToMutatedEmails(t *testing.T) {
	setupBulkDB(t)
	svc := &InboundService{}
	now := time.Now()
	past := now.Add(-48 * time.Hour).UnixMilli()

	clients := []model.Client{
		{
			Email:      "mutated-expired@x",
			ID:         "aaaaaaaa-0000-0000-0000-000000000001",
			Enable:     true,
			ExpiryTime: past,
		},
		{
			Email:      "mutated-renew@x",
			ID:         "aaaaaaaa-0000-0000-0000-000000000002",
			Enable:     false,
			ExpiryTime: past,
			Reset:      30,
		},
		{
			Email:      "unrelated-expired@x",
			ID:         "aaaaaaaa-0000-0000-0000-000000000003",
			Enable:     true,
			ExpiryTime: past,
		},
	}
	ib := mkInbound(t, 33001, model.VLESS, clientsSettings(t, clients))
	seedNormalizedInbound(t, ib, clients)

	db := database.GetDB()
	if err := db.Model(&xray.ClientTraffic{}).
		Where("email = ?", "mutated-renew@x").
		Updates(map[string]any{"up": int64(100), "down": int64(200)}).Error; err != nil {
		t.Fatalf("seed renewable usage: %v", err)
	}

	if err := svc.reconcileClientValidity([]string{
		"mutated-expired@x",
		"mutated-renew@x",
	}); err != nil {
		t.Fatalf("reconcileClientValidity: %v", err)
	}

	var expired xray.ClientTraffic
	if err := db.Where("email = ?", "mutated-expired@x").First(&expired).Error; err != nil {
		t.Fatalf("read mutated expired: %v", err)
	}
	if expired.Enable {
		t.Fatal("mutated expired client stayed enabled")
	}

	var renewed xray.ClientTraffic
	if err := db.Where("email = ?", "mutated-renew@x").First(&renewed).Error; err != nil {
		t.Fatalf("read mutated renewed: %v", err)
	}
	if !renewed.Enable || renewed.ExpiryTime <= now.UnixMilli() || renewed.Up != 0 || renewed.Down != 0 {
		t.Fatalf("mutated renewable client did not renew exactly: %+v", renewed)
	}

	var unrelated xray.ClientTraffic
	if err := db.Where("email = ?", "unrelated-expired@x").First(&unrelated).Error; err != nil {
		t.Fatalf("read unrelated expired: %v", err)
	}
	if !unrelated.Enable || unrelated.ExpiryTime != past {
		t.Fatalf("scoped reconcile changed an unrelated client: %+v", unrelated)
	}

	var expiredRecord, renewedRecord, unrelatedRecord model.ClientRecord
	for email, dst := range map[string]*model.ClientRecord{
		"mutated-expired@x":   &expiredRecord,
		"mutated-renew@x":     &renewedRecord,
		"unrelated-expired@x": &unrelatedRecord,
	} {
		if err := db.Where("email = ?", email).First(dst).Error; err != nil {
			t.Fatalf("read record %q: %v", email, err)
		}
	}
	if expiredRecord.Enable {
		t.Fatal("mutated expired normalized record stayed enabled")
	}
	if !renewedRecord.Enable || renewedRecord.ExpiryTime != renewed.ExpiryTime {
		t.Fatalf("renewed normalized record diverged from traffic row: %+v / %+v", renewedRecord, renewed)
	}
	if !unrelatedRecord.Enable || unrelatedRecord.ExpiryTime != past {
		t.Fatalf("unrelated normalized record changed: %+v", unrelatedRecord)
	}
}

func TestNormalizedClientEmailsTrimsAndDeduplicatesWithoutFoldingIdentity(t *testing.T) {
	got := normalizedClientEmails([]string{" a@x ", "", "a@x", "A@x", " \t "})
	want := []string{"a@x", "A@x"}
	if len(got) != len(want) {
		t.Fatalf("normalized emails = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized emails = %#v, want %#v", got, want)
		}
	}
}
