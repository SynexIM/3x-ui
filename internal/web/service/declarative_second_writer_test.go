package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// declarativeTemplate builds a config template shaped like one the declarative
// endpoint writes: the internal api inbound plus provisioned lines.
func declarativeTemplate(t *testing.T, inbounds string) {
	t.Helper()
	template := `{"inbounds":[` + inbounds + `],
		"outbounds":[{"protocol":"freedom","tag":"direct"}],
		"routing":{"rules":[]}}`
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", template); err != nil {
		t.Fatalf("save template: %v", err)
	}
}

// markDeclarativelyManaged puts the panel in the state a control plane leaves
// behind after a successful apply.
func markDeclarativelyManaged(t *testing.T) {
	t.Helper()
	if err := (&SettingService{}).saveSetting(declarativeProvisioningStateKey, `{"request":{"revision":1},"hash":"abc"}`); err != nil {
		t.Fatalf("save declarative state: %v", err)
	}
}

// GetXrayConfig hands the core the template's inbounds *plus* the inbounds
// table, but the conflict check only ever looked at the table. An admin adding
// an inbound on a port a provisioned line already holds got no warning, and the
// core then refused to start — which, before the readiness wait, nobody heard
// about either.
func TestPortConflictSeesTemplateInbounds(t *testing.T) {
	setupConflictDB(t)
	declarativeTemplate(t, `
		{"tag":"api","listen":"127.0.0.1","port":62789,"protocol":"tunnel","settings":{"rewriteAddress":"127.0.0.1"}},
		{"tag":"line-vless","listen":"0.0.0.0","port":30500,"protocol":"vless","streamSettings":{"network":"tcp"}}`)

	svc := &InboundService{}
	candidate := &model.Inbound{
		Tag:            "admin-added",
		Listen:         "0.0.0.0",
		Port:           30500,
		Protocol:       model.VLESS,
		StreamSettings: `{"network":"tcp"}`,
	}
	got, err := svc.checkPortConflict(candidate, 0)
	if err != nil {
		t.Fatalf("checkPortConflict: %v", err)
	}
	if got == nil {
		t.Fatal("an inbound colliding with a provisioned line must be rejected")
	}
	if msg := got.String(); !strings.Contains(msg, "line-vless") {
		t.Fatalf("the conflict must name the template inbound; got %q", msg)
	}
}

// A free port must stay free: the template scan must not reject everything.
func TestPortConflictAllowsAPortTheTemplateDoesNotUse(t *testing.T) {
	setupConflictDB(t)
	declarativeTemplate(t, `{"tag":"line-vless","listen":"0.0.0.0","port":30500,"protocol":"vless","streamSettings":{"network":"tcp"}}`)

	svc := &InboundService{}
	candidate := &model.Inbound{
		Tag:            "admin-added",
		Listen:         "0.0.0.0",
		Port:           30501,
		Protocol:       model.VLESS,
		StreamSettings: `{"network":"tcp"}`,
	}
	if got, err := svc.checkPortConflict(candidate, 0); err != nil || got != nil {
		t.Fatalf("an unused port must be allowed; got=%v err=%v", got, err)
	}
}

// tcp/udp coexistence holds for template inbounds just as it does for table
// rows: a udp-only inbound may share a port with a tcp-only one.
func TestPortConflictKeepsTcpUdpCoexistenceForTemplateInbounds(t *testing.T) {
	setupConflictDB(t)
	declarativeTemplate(t, `{"tag":"line-tcp","listen":"0.0.0.0","port":30500,"protocol":"vless","streamSettings":{"network":"tcp"}}`)

	svc := &InboundService{}
	candidate := &model.Inbound{
		Tag:      "hysteria-line",
		Listen:   "0.0.0.0",
		Port:     30500,
		Protocol: model.Hysteria,
	}
	if got, err := svc.checkPortConflict(candidate, 0); err != nil || got != nil {
		t.Fatalf("a udp-only inbound must coexist with a tcp template inbound; got=%v err=%v", got, err)
	}
}

// Nodes run their own core with their own config, so the local template says
// nothing about which ports are free over there.
func TestPortConflictIgnoresTemplateForNodeInbounds(t *testing.T) {
	setupConflictDB(t)
	declarativeTemplate(t, `{"tag":"line-vless","listen":"0.0.0.0","port":30500,"protocol":"vless","streamSettings":{"network":"tcp"}}`)

	nodeID := 1
	svc := &InboundService{}
	candidate := &model.Inbound{
		Tag:            "node-inbound",
		Listen:         "0.0.0.0",
		Port:           30500,
		Protocol:       model.VLESS,
		StreamSettings: `{"network":"tcp"}`,
		NodeID:         &nodeID,
	}
	if got, err := svc.checkPortConflict(candidate, 0); err != nil || got != nil {
		t.Fatalf("a node inbound must not collide with the local template; got=%v err=%v", got, err)
	}
}

// While a control plane owns the config, the panel must not write the inbounds
// table at all: anything it adds there joins a configuration the control plane
// believes it fully describes, and survives every later apply.
func TestPanelInboundWritesAreRefusedWhileDeclarativelyManaged(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}

	existing := &model.Inbound{
		Tag:            "pre-existing",
		Enable:         true,
		Listen:         "0.0.0.0",
		Port:           30600,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp"}`,
	}
	if _, _, err := svc.AddInbound(existing); err != nil {
		t.Fatalf("seeding an inbound before takeover must work: %v", err)
	}

	markDeclarativelyManaged(t)

	t.Run("add", func(t *testing.T) {
		added := &model.Inbound{
			Tag:            "sneaky",
			Enable:         true,
			Listen:         "0.0.0.0",
			Port:           30601,
			Protocol:       model.VLESS,
			Settings:       `{"clients":[],"decryption":"none"}`,
			StreamSettings: `{"network":"tcp"}`,
		}
		if _, _, err := svc.AddInbound(added); !errors.Is(err, ErrDeclarativelyManaged) {
			t.Fatalf("add must be refused, got %v", err)
		}
	})

	t.Run("update", func(t *testing.T) {
		edit := *existing
		edit.Port = 30602
		if _, _, err := svc.UpdateInbound(&edit); !errors.Is(err, ErrDeclarativelyManaged) {
			t.Fatalf("update must be refused, got %v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if _, err := svc.DelInbound(existing.Id); !errors.Is(err, ErrDeclarativelyManaged) {
			t.Fatalf("delete must be refused, got %v", err)
		}
	})

	t.Run("the row is untouched", func(t *testing.T) {
		got, err := svc.GetInbound(existing.Id)
		if err != nil {
			t.Fatalf("the inbound must still be there: %v", err)
		}
		if got.Port != 30600 {
			t.Fatalf("port changed to %d despite the refusal", got.Port)
		}
	})
}

// An unmanaged panel is a plain 3x-ui and must keep working exactly as before.
func TestPanelInboundWritesWorkWhenNotDeclarativelyManaged(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}

	added := &model.Inbound{
		Tag:            "hand-made",
		Enable:         true,
		Listen:         "0.0.0.0",
		Port:           30700,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp"}`,
	}
	if _, _, err := svc.AddInbound(added); err != nil {
		t.Fatalf("add on an unmanaged panel: %v", err)
	}
	if _, err := svc.DelInbound(added.Id); err != nil {
		t.Fatalf("delete on an unmanaged panel: %v", err)
	}
}
