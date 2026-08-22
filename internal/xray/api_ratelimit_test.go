package xray

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/common/protocol"
)

// A hot-added client must carry its limits to the core. Without this the client
// runs unlimited until the next full restart, and nothing in the panel says so.
func TestApplyUserRateLimitsFromPanelClient(t *testing.T) {
	// Values arrive as float64 after a JSON round-trip through settings.
	var src map[string]any
	raw := `{"email":"line-042","bandwidth_bps":100000000,"committed_bps":20000000,"committed_burst_bytes":50000000,"conn_limit":4}`
	if err := json.Unmarshal([]byte(raw), &src); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	u := applyUserRateLimits(&protocol.User{Email: "line-042"}, src)
	if u.BandwidthBps != 100_000_000 {
		t.Errorf("bandwidth_bps = %d, want 100000000", u.BandwidthBps)
	}
	if u.CommittedBps != 20_000_000 {
		t.Errorf("committed_bps = %d, want 20000000", u.CommittedBps)
	}
	if u.CommittedBurstBytes != 50_000_000 {
		t.Errorf("committed_burst_bytes = %d, want 50000000", u.CommittedBurstBytes)
	}
	if u.ConnLimit != 4 {
		t.Errorf("conn_limit = %d, want 4", u.ConnLimit)
	}
}

func TestApplyUserRateLimitsLeavesUnlimitedAlone(t *testing.T) {
	u := applyUserRateLimits(&protocol.User{Email: "free"}, map[string]any{"email": "free"})
	if u.BandwidthBps != 0 || u.CommittedBps != 0 || u.CommittedBurstBytes != 0 || u.ConnLimit != 0 {
		t.Errorf("a client with no limits gained one: %d/%d/%d/%d", u.BandwidthBps, u.CommittedBps, u.CommittedBurstBytes, u.ConnLimit)
	}
}

func TestUint64FieldAcceptsEveryShapeTheseValuesArriveIn(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want uint64
	}{
		{"float64 from JSON", float64(100_000_000), 100_000_000},
		{"uint64 from Go", uint64(42), 42},
		{"int64", int64(42), 42},
		{"int", 42, 42},
		{"json.Number", json.Number("42"), 42},
		{"zero", float64(0), 0},
		{"negative is not a limit", float64(-1), 0},
		{"string is not a number", "100", 0},
		{"missing", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := map[string]any{}
			if c.val != nil {
				src["bandwidth_bps"] = c.val
			}
			if got := uint64Field(src, "bandwidth_bps"); got != c.want {
				t.Errorf("uint64Field = %d, want %d", got, c.want)
			}
		})
	}
}
