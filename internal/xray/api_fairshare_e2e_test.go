package xray

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestFairShareAPI_E2E proves the panel's fair-share settings reach a real core:
// it pushes a policy, then reads GetStatus back off the same process. A fake
// gRPC server can only prove the wire format; only the core proves the numbers
// were accepted and are the ones now in effect.
//
// Skipped unless XRAY_E2E_BINARY points at an xray built from the fork in
// go.mod — an upstream binary has no FairShareService and would fail to start.
func TestFairShareAPI_E2E(t *testing.T) {
	bin := os.Getenv("XRAY_E2E_BINARY")
	if bin == "" {
		t.Skip("set XRAY_E2E_BINARY to an xray binary to run this test")
	}

	apiPort := freePort(t)
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"api": map[string]any{
			"services": []string{"HandlerService", "StatsService", "FairShareService"},
			"tag":      "api",
		},
		"inbounds": []any{
			map[string]any{
				"listen":   "127.0.0.1",
				"port":     apiPort,
				"protocol": "tunnel",
				"settings": map[string]any{"rewriteAddress": "127.0.0.1"},
				"tag":      "api",
			},
		},
		"outbounds": []any{
			map[string]any{"protocol": "freedom", "settings": map[string]any{}, "tag": "direct"},
		},
		"routing": map[string]any{
			"rules": []any{
				map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
			},
		},
		"stats": map[string]any{},
	}
	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-c", cfgPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start xray: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	waitForPort(t, apiPort)

	api := XrayAPI{}
	if err := api.Init(apiPort); err != nil {
		t.Fatalf("api init: %v", err)
	}
	defer api.Close()

	if err := api.SetNodeBandwidth(NodeFairShare{
		AvailBitPerSec:         800_000_000,
		SoftFloorBitPerSec:     4_000_000,
		CongestionEnterPercent: 85,
		CongestionExitPercent:  70,
		CongestionExitTicks:    3,
	}); err != nil {
		t.Fatalf("SetNodeBandwidth: %v", err)
	}
	if err := api.SetClassPolicy([]ClassFairShare{{
		Name:               "live",
		Weight:             3,
		NormalCapBitPerSec: 160_000_000,
		BurstCapBitPerSec:  400_000_000,
		BurstCreditBytes:   1_000_000_000,
		FloorRatioPercent:  20,
	}}); err != nil {
		t.Fatalf("SetClassPolicy: %v", err)
	}

	status, err := api.GetFairShareStatus()
	if err != nil {
		t.Fatalf("GetFairShareStatus: %v", err)
	}
	// The core reports byte/s; reading 800 Mbit/s back is the whole round trip,
	// units included.
	if status.RootCapBitPerSec != 800_000_000 {
		t.Fatalf("root cap = %d bit/s, want 800000000", status.RootCapBitPerSec)
	}

	// Turning the node cap back off must be a real instruction, not a no-op the
	// panel silently swallows.
	if err := api.SetNodeBandwidth(NodeFairShare{}); err != nil {
		t.Fatalf("SetNodeBandwidth(off): %v", err)
	}
	status, err = api.GetFairShareStatus()
	if err != nil {
		t.Fatalf("GetFairShareStatus after off: %v", err)
	}
	if status.RootCapBitPerSec != 0 {
		t.Fatalf("root cap after clearing = %d, want 0", status.RootCapBitPerSec)
	}
}
