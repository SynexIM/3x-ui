package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm/clause"
)

func TestTrafficDisableImmediatelyUpdatesNodeRuntime(t *testing.T) {
	setupConflictDB(t)
	nodeID, fake := setupNodeRuntime(t)
	client := model.Client{Email: "spent-node", Enable: true}
	ib := nodeInbound(t, nodeID, 46301, []model.Client{client})
	// The fork's attach already created the traffic row; overwrite it depleted.
	if err := database.GetDB().Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "email"}}, UpdateAll: true}).Create(&xray.ClientTraffic{
		InboundId: ib.Id, Email: client.Email, Enable: true, Up: 100, Total: 100,
	}).Error; err != nil {
		t.Fatalf("seed traffic: %v", err)
	}

	if _, _, err := (&InboundService{}).AddTraffic(nil, nil); err != nil {
		t.Fatalf("AddTraffic: %v", err)
	}
	if got := fake.updateInbound.Load(); got != 1 {
		t.Fatalf("remote UpdateInbound calls = %d, want 1 after commit", got)
	}
}

func TestTrafficDisableRefreshesLocalMTProtoSidecar(t *testing.T) {
	setupConflictDB(t)
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }})
	fake := &fakeNodeRuntime{}
	mgr.SetLocalRuntimeOverride(fake)
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(nil) })

	seedInboundConflict(t, "mt-spent", "", 46302, model.MTProto, "",
		`{"clients":[{"email":"spent-mt","secret":"`+mtprotoTestSecretA+`","enable":true}]}`)
	ib := loadInboundByTag(t, "mt-spent")
	clients, err := ParseInboundDraftClients(ib.Settings) // fork: settings is only the seed draft
	if err != nil {
		t.Fatalf("GetClients: %v", err)
	}
	if err := (&ClientService{}).SyncInbound(nil, ib.Id, clients); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}
	seedClientTraffic(t, ib.Id, "spent-mt", true)
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", "spent-mt").
		Updates(map[string]any{"up": 100, "total": 100}).Error; err != nil {
		t.Fatalf("deplete traffic: %v", err)
	}

	if _, _, err := (&InboundService{}).AddTraffic(nil, nil); err != nil {
		t.Fatalf("AddTraffic: %v", err)
	}
	if got := fake.updateInbound.Load(); got != 1 {
		t.Fatalf("MTProto sidecar UpdateInbound calls = %d, want 1 after commit", got)
	}
}

func TestDelDepletedClientsCleansRuntimeAfterCommit(t *testing.T) {
	setupConflictDB(t)
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }})
	fake := &removeUserProbe{}
	mgr.SetLocalRuntimeOverride(fake)
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(nil) })

	seedInboundConflict(t, "depleted-only", "", 46303, model.VLESS, `{"network":"tcp"}`,
		`{"clients":[{"email":"gone","enable":true}]}`)
	ib := loadInboundByTag(t, "depleted-only")
	// Fork: membership lives in the normalized tables, so attach the seed there.
	if err := (&ClientService{}).SyncInbound(nil, ib.Id, []model.Client{{Email: "gone", ID: "0b9e8a6c-0000-4000-8000-000000000001", Enable: true}}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}
	seedClientTraffic(t, ib.Id, "gone", true)
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", "gone").
		Updates(map[string]any{"up": 100, "total": 100, "reset": 0}).Error; err != nil {
		t.Fatalf("deplete traffic: %v", err)
	}

	if err := (&InboundService{}).DelDepletedClients(-1); err != nil {
		t.Fatalf("DelDepletedClients: %v", err)
	}
	// The fork removes the one user instead of rebuilding the whole inbound.
	if got := fake.removed.Load(); got != 1 {
		t.Fatalf("runtime RemoveUser calls = %d, want 1 after commit", got)
	}
}

type removeUserProbe struct {
	fakeNodeRuntime
	removed atomic.Int32
}

func (p *removeUserProbe) RemoveUser(context.Context, *model.Inbound, string) error {
	p.removed.Add(1)
	return nil
}
