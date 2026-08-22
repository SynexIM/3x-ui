package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

// withXrayGeoAssets puts a minimal geoip.dat where xray-core's loader looks.
// The panel's own default outbound blocks geoip:private, so without one every
// config check fails on the asset rather than on the config.
func withXrayGeoAssets(t *testing.T) {
	t.Helper()
	list := &geodata.GeoIPList{Entry: []*geodata.GeoIP{{
		Code: "PRIVATE",
		Cidr: []*geodata.CIDR{{Ip: []byte{10, 0, 0, 0}, Prefix: 8}},
	}}}
	encoded, err := proto.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "geoip.dat"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_LOCATION_ASSET", dir)
}

// seedAppliedTemplate puts a node in the state a successful apply leaves behind:
// the compiled template stored, and the config that produced it recorded.
func seedAppliedTemplate(t *testing.T, config DeclarativeNodeConfig, revision int) string {
	t.Helper()
	template, err := (&DeclarativeProvisioningService{}).buildTemplate(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&XraySettingService{}).SaveXraySetting(template); err != nil {
		t.Fatalf("the seeded template must be one xray accepts: %v", err)
	}
	stored, err := (&SettingService{}).GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	seedDeclarativeState(t, config, revision)
	return stored
}

// The reason inbound pre-validation exists: a config xray would refuse used to
// be saved happily, overwriting the previous one, and only failed when the core
// started — by which time every other inbound on the node was down too.
func TestAnIllegalInboundNeverReplacesWhatTheNodeIsRunning(t *testing.T) {
	setupConflictDB(t)
	withXrayGeoAssets(t)
	applied := fiveProtocolConfig(t)
	previousTemplate := seedAppliedTemplate(t, applied, 7)

	broken := fiveProtocolConfig(t)
	broken.Inbounds[3].Settings["method"] = "rot13-please"

	svc := &DeclarativeProvisioningService{}
	_, err := svc.Apply(&DeclarativeApplyRequest{Revision: 8, Config: broken})
	if err == nil {
		t.Fatal("an inbound xray-core refuses must not be applied")
	}
	if !strings.Contains(err.Error(), "line-ss") {
		t.Fatalf("the refusal must name the inbound the control plane has to fix; got %q", err.Error())
	}

	stored, storeErr := (&SettingService{}).GetXrayConfigTemplate()
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	if stored != previousTemplate {
		t.Fatal("a refused apply must leave the running template byte-identical")
	}
	state, stateErr := svc.loadState()
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if state.Request.Revision != 7 {
		t.Fatalf("a refused apply must not move the applied revision; now %d", state.Request.Revision)
	}
}

// A config xray-core builds but cannot run gets as far as being written. The
// node then has to be put back, and the caller has to hear about both failures
// — the original one and anything that went wrong on the way back.
func TestAnApplyThatCannotStartTheCoreRollsTheTemplateBack(t *testing.T) {
	setupConflictDB(t)
	withXrayGeoAssets(t)
	applied := fiveProtocolConfig(t)
	previousTemplate := seedAppliedTemplate(t, applied, 7)

	// Valid, and different: a second account on every inbound. There is no xray
	// binary in a test tree, so starting the core is what fails.
	next := fiveProtocolConfig(t)
	for i := range next.Inbounds {
		second := next.Inbounds[i].Clients[0]
		second.Email = "line-002@line.invalid"
		second.UUID = "22222222-2222-2222-2222-222222222222"
		next.Inbounds[i].Clients = append(next.Inbounds[i].Clients, second)
	}

	svc := &DeclarativeProvisioningService{}
	_, err := svc.Apply(&DeclarativeApplyRequest{Revision: 8, RequiresRestart: true, Config: next})
	if err == nil {
		t.Fatal("an apply whose core never starts must not be reported as applied")
	}
	if !strings.Contains(err.Error(), "apply declarative xray config") {
		t.Fatalf("the caller must hear what actually failed; got %q", err.Error())
	}

	stored, storeErr := (&SettingService{}).GetXrayConfigTemplate()
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	if stored != previousTemplate {
		t.Fatal("the rollback must restore the template the node was running")
	}
	state, stateErr := svc.loadState()
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if state.Request.Revision != 7 {
		t.Fatalf("a rolled-back apply must not move the applied revision; now %d", state.Request.Revision)
	}
}
