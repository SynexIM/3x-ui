package xray

import (
	"os"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

type provisioningProtocol struct {
	name     string
	protocol string
	base     string
	added    string
	updated  string
	disabled string
}

// TestProvisioningMutationsNeverRequireRestart is the executable availability
// contract for an order. It covers create, credential/rate update, suspend and
// release across every protocol IPAero sells. A future ComputeHotDiff change
// that moves any row to the restart path makes CI red.
func TestProvisioningMutationsNeverRequireRestart(t *testing.T) {
	protocols := []provisioningProtocol{
		{
			name: "vless", protocol: "vless",
			base:     `{"decryption":"none","clients":[{"email":"a@x","id":"11111111-1111-1111-1111-111111111111","enable":true}]}`,
			added:    `{"decryption":"none","clients":[{"email":"a@x","id":"11111111-1111-1111-1111-111111111111","enable":true},{"email":"b@x","id":"22222222-2222-2222-2222-222222222222","enable":true,"bandwidth_bps":100000000,"committed_bps":10000000,"committed_burst_bytes":50000000}]}`,
			updated:  `{"decryption":"none","clients":[{"email":"a@x","id":"aaaaaaaa-1111-1111-1111-111111111111","enable":true,"bandwidth_bps":200000000,"committed_bps":20000000,"committed_burst_bytes":90000000}]}`,
			disabled: `{"decryption":"none","clients":[]}`,
		},
		{
			name: "vmess", protocol: "vmess",
			base:     `{"clients":[{"email":"a@x","id":"11111111-1111-1111-1111-111111111111","security":"auto","enable":true}]}`,
			added:    `{"clients":[{"email":"a@x","id":"11111111-1111-1111-1111-111111111111","security":"auto","enable":true},{"email":"b@x","id":"22222222-2222-2222-2222-222222222222","security":"auto","enable":true,"bandwidth_bps":100000000}]}`,
			updated:  `{"clients":[{"email":"a@x","id":"aaaaaaaa-1111-1111-1111-111111111111","security":"auto","enable":true,"committed_bps":10000000}]}`,
			disabled: `{"clients":[]}`,
		},
		{
			name: "mixed", protocol: "mixed",
			base:     `{"auth":"password","accounts":[{"user":"a@x","pass":"old"}]}`,
			added:    `{"auth":"password","accounts":[{"user":"a@x","pass":"old"},{"user":"b@x","pass":"new","bandwidth_bps":100000000}]}`,
			updated:  `{"auth":"password","accounts":[{"user":"a@x","pass":"rotated","committed_bps":10000000}]}`,
			disabled: `{"auth":"password","accounts":[]}`,
		},
		{
			name: "shadowsocks", protocol: "shadowsocks",
			base:     `{"method":"aes-256-gcm","clients":[{"email":"a@x","password":"old","enable":true}]}`,
			added:    `{"method":"aes-256-gcm","clients":[{"email":"a@x","password":"old","enable":true},{"email":"b@x","password":"new","enable":true,"bandwidth_bps":100000000}]}`,
			updated:  `{"method":"aes-256-gcm","clients":[{"email":"a@x","password":"rotated","enable":true,"committed_bps":10000000}]}`,
			disabled: `{"method":"aes-256-gcm","clients":[]}`,
		},
		{
			name: "hysteria2", protocol: "hysteria",
			base:     `{"version":2,"clients":[{"email":"a@x","auth":"old","enable":true}]}`,
			added:    `{"version":2,"clients":[{"email":"a@x","auth":"old","enable":true},{"email":"b@x","auth":"new","enable":true,"bandwidth_bps":100000000}]}`,
			updated:  `{"version":2,"clients":[{"email":"a@x","auth":"rotated","enable":true,"committed_bps":10000000}]}`,
			disabled: `{"version":2,"clients":[]}`,
		},
	}

	for _, protocol := range protocols {
		for _, mutation := range []struct {
			name     string
			settings string
		}{
			{"create", protocol.added},
			{"update credentials and tier", protocol.updated},
			{"suspend", protocol.disabled},
			{"release", emptyUsers(protocol)},
		} {
			t.Run(protocol.name+"/"+mutation.name, func(t *testing.T) {
				oldCfg := provisioningConfig(protocol.protocol, protocol.base)
				newCfg := provisioningConfig(protocol.protocol, mutation.settings)
				if os.Getenv("IPAERO_PROVE_RESTART_REDLINE") == "1" &&
					protocol.name == "vless" && mutation.name == "create" {
					// Acceptance-only fault injection: this static section has
					// no runtime API, so the hot-only assertion below must fail.
					newCfg.LogConfig = json_util.RawMessage(`{"loglevel":"debug"}`)
				}
				diff, hot := ComputeHotDiff(oldCfg, newCfg)
				if !hot {
					t.Fatalf(
						"%s now requires an Xray restart; this order path would disconnect every line on the node",
						mutation.name,
					)
				}
				if len(diff.RemovedInboundTags) != 0 || len(diff.AddedInbounds) != 0 {
					t.Fatalf(
						"%s replaces the whole inbound instead of one user; other lines on the listener would drop",
						mutation.name,
					)
				}
				if len(diff.RemovedUsers)+len(diff.AddedUsers) == 0 {
					t.Fatalf("%s produced no user operation; the runtime would stay stale", mutation.name)
				}
			})
		}
	}
}

// This is the deliberately bad path required by the redline acceptance: adding
// a static Xray section has no runtime API. The negative control proves
// ComputeHotDiff detects it; if the provisioning matrix accidentally used this
// mutation, the assertion above would fail CI.
func TestKnownRestartingPathTripsProvisioningRedline(t *testing.T) {
	oldCfg := provisioningConfig("vless", `{"decryption":"none","clients":[]}`)
	newCfg := provisioningConfig("vless", `{"decryption":"none","clients":[]}`)
	newCfg.LogConfig = json_util.RawMessage(`{"loglevel":"debug"}`)
	if _, hot := ComputeHotDiff(oldCfg, newCfg); hot {
		t.Fatal("negative control stopped triggering the restart redline")
	}
}

func provisioningConfig(protocol, settings string) *Config {
	config := matrixConfig()
	config.InboundConfigs[1].Protocol = protocol
	config.InboundConfigs[1].Settings = json_util.RawMessage(settings)
	return config
}

func emptyUsers(protocol provisioningProtocol) string {
	switch protocol.name {
	case "vless":
		return `{"decryption":"none","clients":[]}`
	case "mixed":
		return `{"auth":"password","accounts":[]}`
	case "shadowsocks":
		return `{"method":"aes-256-gcm","clients":[]}`
	case "hysteria2":
		return `{"version":2,"clients":[]}`
	default:
		return `{"clients":[]}`
	}
}
