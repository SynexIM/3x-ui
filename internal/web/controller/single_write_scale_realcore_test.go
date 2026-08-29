package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

/*
What one single-object write costs on a machine that already holds N of them.

The deliverable is the shape of the curve, not the absolute milliseconds: if
adding a client is O(N), the ten-thousandth customer waits ten thousand times
longer than the first for the very same operation, on hardware that has not
changed. A real core is required because the write path ends in gRPC calls to
it — a mock would hide the part being measured.

	go build -o /tmp/xray github.com/xtls/xray-core/main
	XRAY_E2E_BINARY=/tmp/xray XUI_SCALE_SIZES=100,5000,50000 \
	  go test ./internal/web/controller -run RealCoreSingleObjectWriteScale -v -timeout 60m
*/
func TestRealCoreSingleObjectWriteScale(t *testing.T) {
	for _, n := range scaleWriteSizes(t) {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			var inboundId int
			f := newRealCoreFixtureSeeded(t, func(t *testing.T, template map[string]any) {
				inboundId = seedScaleClients(t, n)
				seedScaleRules(t, template, n)
			})

			// A connection opened before the writes: the measurement is
			// worthless if the "fast" path got fast by restarting the core.
			live := dialThroughSocks(t, f.socksPort, f.echoAddr)
			defer live.Close()
			echoOver(t, live, "before")
			pidBefore := f.pid()

			// Four writes, not one: the first pays cold caches, the rest are
			// what a machine provisioning all day actually sees.
			const writes = 4
			clientAdds := make([]time.Duration, writes)
			ruleAdds := make([]time.Duration, writes)
			for i := range writes {
				body := fmt.Sprintf(
					`{"client":{"email":"probe-%d-%d@example.com","enable":true,"totalGB":0,"expiryTime":0},"inboundIds":[%d],"normalizedOnly":true}`,
					i, time.Now().UnixNano(), inboundId)
				start := time.Now()
				status, payload := f.call(http.MethodPost, "/panel/api/clients/add", body)
				clientAdds[i] = time.Since(start)
				if status != http.StatusOK || !strings.Contains(payload, `"success":true`) {
					t.Fatalf("clients/add answered %d: %s", status, payload)
				}

				rule := fmt.Sprintf(
					`{"type":"field","ruleTag":"probe-rule-%d-%d","user":["probe@example.com"],"outboundTag":"direct"}`,
					i, time.Now().UnixNano())
				start = time.Now()
				result := f.mustApply(http.MethodPost, "/panel/api/routing/rules", rule)
				ruleAdds[i] = time.Since(start)
				if !result.HotApplied {
					t.Fatalf("the rule was not hot-applied: %+v", result)
				}
			}

			start := time.Now()
			f.mustApply(http.MethodGet, "/panel/api/outbounds", "")
			outboundList := time.Since(start)

			echoOver(t, live, "after")
			if pid := f.pid(); pid != pidBefore {
				t.Fatalf("the core restarted during the measurement: pid %d -> %d", pidBefore, pid)
			}

			t.Logf("MEASURED N=%-6d  clients/add=%-28s  routing/rules=%-28s  GET outbounds=%v",
				n, roundAll(clientAdds), roundAll(ruleAdds), outboundList.Round(time.Millisecond))

			// Where the time goes on a real mutating write, so a regression
			// can be attributed instead of guessed at.
			var xraySvc service.XrayService
			var clientSvc service.ClientService
			var inboundSvc service.InboundService
			start = time.Now()
			if _, err := clientSvc.Create(&inboundSvc, &service.ClientCreatePayload{
				Client:     model.Client{Email: fmt.Sprintf("split-%d@example.com", time.Now().UnixNano()), Enable: true},
				InboundIds: []int{inboundId},
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			persist := time.Since(start)
			start = time.Now()
			if err := xraySvc.ApplyDesiredConfigHotOnly(); err != nil {
				t.Fatalf("ApplyDesiredConfigHotOnly: %v", err)
			}
			reconcile := time.Since(start)

			start = time.Now()
			desired, err := xraySvc.GetXrayConfig()
			if err != nil {
				t.Fatalf("GetXrayConfig: %v", err)
			}
			buildConfig := time.Since(start)
			running := service.XrayProcess().GetConfig()
			start = time.Now()
			running.Equals(desired)
			equals := time.Since(start)
			t.Logf("SPLIT    N=%-6d  persist=%-9v reconcile=%-9v (of which GetXrayConfig=%-9v Equals=%v)",
				n, persist.Round(time.Millisecond), reconcile.Round(time.Millisecond),
				buildConfig.Round(time.Millisecond), equals.Round(time.Millisecond))

			// This is the regression gate for the bug that started this test:
			// a known single-object write must not pay for the whole runtime
			// rebuild. Use the same machine and dataset as the baseline instead
			// of a brittle absolute millisecond threshold. The first write is
			// intentionally excluded because it warms SQLite and gRPC caches.
			for label, average := range map[string]time.Duration{
				"clients/add":   steadyAverage(clientAdds),
				"routing/rules": steadyAverage(ruleAdds),
			} {
				if average*2 >= reconcile {
					t.Errorf("%s steady average %v is not materially cheaper than whole reconcile %v",
						label, average.Round(time.Millisecond), reconcile.Round(time.Millisecond))
				}
			}
		})
	}
}

func scaleWriteSizes(t *testing.T) []int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("XUI_SCALE_SIZES"))
	if raw == "" {
		return []int{100, 5000, 50000}
	}
	out := make([]int, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v <= 0 {
			t.Fatalf("XUI_SCALE_SIZES: bad size %q", part)
		}
		out = append(out, v)
	}
	return out
}

// seedScaleClients writes n healthy clients onto one local VLESS inbound the
// same way the panel stores them — settings JSON, clients, client_inbounds and
// client_traffics — and returns the inbound id.
func seedScaleClients(t *testing.T, n int) int {
	t.Helper()
	db := database.GetDB()

	clients := make([]model.Client, n)
	entries := make([]map[string]any, n)
	expiry := time.Now().AddDate(1, 0, 0).UnixMilli()
	for i := range n {
		email := fmt.Sprintf("seed-%06d@example.com", i)
		clients[i] = model.Client{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), Email: email,
			Enable: true, TotalGB: 100 << 30, ExpiryTime: expiry,
			SubID: fmt.Sprintf("sub%06d", i),
		}
		entries[i] = map[string]any{"id": clients[i].ID, "email": email}
	}
	settings, err := json.Marshal(map[string]any{"clients": entries, "decryption": "none"})
	if err != nil {
		t.Fatal(err)
	}

	ib := &model.Inbound{
		UserId: 1, Tag: "scale-in", Enable: true, Listen: "127.0.0.1",
		Port: freeLocalPort(t), Protocol: model.VLESS, Settings: string(settings),
	}
	tx := db.Begin()
	if err := tx.Create(ib).Error; err != nil {
		tx.Rollback()
		t.Fatalf("seed inbound: %v", err)
	}
	records := make([]*model.ClientRecord, n)
	traffics := make([]xray.ClientTraffic, n)
	for i := range clients {
		records[i] = clients[i].ToRecord()
		traffics[i] = xray.ClientTraffic{
			InboundId: ib.Id, Email: clients[i].Email, Enable: true,
			Total: clients[i].TotalGB, ExpiryTime: clients[i].ExpiryTime,
		}
	}
	if n > 0 {
		if err := tx.CreateInBatches(records, 500).Error; err != nil {
			tx.Rollback()
			t.Fatalf("seed clients: %v", err)
		}
		links := make([]model.ClientInbound, n)
		for i := range records {
			links[i] = model.ClientInbound{ClientId: records[i].Id, InboundId: ib.Id}
		}
		if err := tx.CreateInBatches(links, 1000).Error; err != nil {
			tx.Rollback()
			t.Fatalf("seed client_inbounds: %v", err)
		}
		if err := tx.CreateInBatches(traffics, 1000).Error; err != nil {
			tx.Rollback()
			t.Fatalf("seed client_traffics: %v", err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	db.Exec("ANALYZE")
	return ib.Id
}

// seedScaleRules appends n routing rules to the template the core will start
// from, one per seeded client, which is what a per-customer routing policy
// looks like at scale.
func seedScaleRules(t *testing.T, template map[string]any, n int) {
	t.Helper()
	routing, _ := template["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	for i := range n {
		rules = append(rules, map[string]any{
			"type":        "field",
			"ruleTag":     fmt.Sprintf("seed-rule-%06d", i),
			"user":        []string{fmt.Sprintf("seed-%06d@example.com", i)},
			"outboundTag": "direct",
		})
	}
	routing["rules"] = rules
}

// roundAll renders a run of measurements at millisecond resolution; the spread
// between the first and the rest is the cold-cache share.
func roundAll(values []time.Duration) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = v.Round(time.Millisecond).String()
	}
	return strings.Join(parts, " ")
}

func steadyAverage(values []time.Duration) time.Duration {
	if len(values) <= 1 {
		return 0
	}
	var total time.Duration
	for _, value := range values[1:] {
		total += value
	}
	return total / time.Duration(len(values)-1)
}
