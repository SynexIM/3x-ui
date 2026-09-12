package controller

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestRealCoreRuntimePatchPreservesConnectionAndValidity(t *testing.T) {
	const email = "seed-000000@example.com"
	var before model.ClientRecord
	fixture := newRealCoreFixtureSeeded(t, func(t *testing.T, template map[string]any) {
		// The test's echo target is loopback; the core blocks private proxy targets by default.
		template["outbounds"].([]any)[0].(map[string]any)["settings"] = map[string]any{
			"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.1/32"}}},
		}
		id := seedScaleClients(t, 1)
		var inbound model.Inbound
		if err := database.GetDB().First(&inbound, id).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.GetDB().Where("email = ?", email).First(&before).Error; err != nil {
			t.Fatal(err)
		}
		template["outbounds"] = append(template["outbounds"].([]any), map[string]any{
			"tag": "test-vless-client", "protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": "127.0.0.1", "port": inbound.Port,
				"users": []any{map[string]any{"id": before.UUID, "encryption": "none"}},
			}}},
		})
		routing := template["routing"].(map[string]any)
		routing["rules"] = append(routing["rules"].([]any), map[string]any{
			"type": "field", "inboundTag": []string{"socks-in"}, "outboundTag": "test-vless-client",
		})
	})
	live := dialThroughSocks(t, fixture.socksPort, fixture.echoAddr)
	defer live.Close()
	echoOver(t, live, "before runtime patch")
	pid := fixture.pid()
	body := `{"upload_bandwidth_bps":8000000,"download_bandwidth_bps":24000000,"conn_limit":3}`
	for i := range 2 {
		status, payload := fixture.call(http.MethodPost, "/panel/api/clients/runtime/"+email, body)
		if status != http.StatusOK || !strings.Contains(payload, `"hotApplied":true`) || !strings.Contains(payload, `"nodePending":false`) {
			t.Fatalf("runtime update %d did not complete: %d %s", i, status, payload)
		}
		echoOver(t, live, fmt.Sprintf("existing connection after patch %d", i))
		added := dialThroughSocks(t, fixture.socksPort, fixture.echoAddr)
		echoOver(t, added, "new connection uses the original identity")
		added.Close()
	}
	if fixture.pid() != pid {
		t.Fatal("runtime patch restarted the process")
	}
	var after model.ClientRecord
	if err := database.GetDB().First(&after, before.Id).Error; err != nil {
		t.Fatal(err)
	}
	before.UploadBandwidthBps, before.DownloadBandwidthBps, before.ConnLimit = 8_000_000, 24_000_000, 3
	before.UpdatedAt = after.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("runtime patch changed identity, validity or unrelated configuration")
	}
	if err := fixture.xray.StopXray(); err != nil {
		t.Fatal(err)
	}
	_, payload := fixture.call(http.MethodPost, "/panel/api/clients/runtime/"+email, body)
	if strings.Contains(payload, `"success":true`) {
		t.Fatal("stopped core was reported as applied")
	}
}
