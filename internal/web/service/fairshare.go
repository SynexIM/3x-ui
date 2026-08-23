package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const fairSharePolicySettingKey = "fairSharePolicy"

// ErrFairShareManaged is returned to a panel-side fair-share write while a
// control plane owns this node: it re-pushes its own node bandwidth on every
// status poll, so a local edit would be silently reverted minutes later.
var ErrFairShareManaged = errors.New("this node's fair-share policy is managed by the control plane and is read-only in the panel")

// FairShareClassPolicy is one class (= one SKU) contention policy.
// Every rate is bit/s and 0 means "not enabled", never "use a default".
type FairShareClassPolicy struct {
	Name               string `json:"name" example:"live"`
	Weight             uint32 `json:"weight" example:"3"`
	NormalCapBitPerSec uint64 `json:"normalCapBitPerSec" example:"20000000"`
	BurstCapBitPerSec  uint64 `json:"burstCapBitPerSec" example:"50000000"`
	BurstCreditBytes   uint64 `json:"burstCreditBytes" example:"1000000000"`
	FloorRatioPercent  uint32 `json:"floorRatioPercent" example:"20"`
}

// FairSharePolicy is the whole node-level fair-share configuration the panel owns.
type FairSharePolicy struct {
	AvailBitPerSec         uint64                 `json:"availBitPerSec" example:"1000000000"`
	SoftFloorBitPerSec     uint64                 `json:"softFloorBitPerSec" example:"500000"`
	HardFloorBitPerSec     uint64                 `json:"hardFloorBitPerSec" example:"0"`
	CongestionEnterPercent uint32                 `json:"congestionEnterPercent" example:"85"`
	CongestionExitPercent  uint32                 `json:"congestionExitPercent" example:"70"`
	CongestionExitTicks    uint32                 `json:"congestionExitTicks" example:"5"`
	Classes                []FairShareClassPolicy `json:"classes"`
}

// FairSharePolicyView is what the node page reads: the policy plus the reason
// its controls may be read-only.
type FairSharePolicyView struct {
	Policy FairSharePolicy `json:"policy"`
	// DeclarativelyManaged mirrors the server-side refusal so the form can grey
	// itself out instead of failing on submit.
	DeclarativelyManaged bool `json:"declarativelyManaged" example:"false"`
}

// FairShareStatusView is the scheduler's runtime state, in panel units.
type FairShareStatusView struct {
	Running                 bool   `json:"running" example:"true"`
	RootCapBitPerSec        uint64 `json:"rootCapBitPerSec" example:"1000000000"`
	Congested               bool   `json:"congested" example:"false"`
	ActiveMembers           uint32 `json:"activeMembers" example:"12"`
	FillTruncated           bool   `json:"fillTruncated" example:"false"`
	FillUnresolvedMembers   uint32 `json:"fillUnresolvedMembers" example:"0"`
	FillTruncatedTicks      uint64 `json:"fillTruncatedTicks" example:"0"`
	FillTruncatedTotalTicks uint64 `json:"fillTruncatedTotalTicks" example:"0"`
	FillRounds              uint32 `json:"fillRounds" example:"3"`
}

type FairShareService struct {
	settingService SettingService
	xrayService    XrayService
}

// GetPolicy returns the stored policy. An unset key is the zero policy, which
// is "nothing enabled" — the panel never invents a default bandwidth (FR-079c).
func (s *FairShareService) GetPolicy() (*FairSharePolicy, error) {
	setting, err := s.settingService.getSetting(fairSharePolicySettingKey)
	if database.IsNotFound(err) {
		return &FairSharePolicy{}, nil
	}
	if err != nil {
		return nil, err
	}
	policy := &FairSharePolicy{}
	if setting.Value == "" {
		return policy, nil
	}
	if err := json.Unmarshal([]byte(setting.Value), policy); err != nil {
		return nil, err
	}
	return policy, nil
}

func (s *FairShareService) GetPolicyView() (*FairSharePolicyView, error) {
	policy, err := s.GetPolicy()
	if err != nil {
		return nil, err
	}
	view := &FairSharePolicyView{Policy: *policy, DeclarativelyManaged: IsDeclarativelyManaged()}
	// While managed, the control plane's bandwidth is the one the core actually
	// runs on; showing the stale local number would be the greyed-out lie.
	if view.DeclarativelyManaged {
		if state, err := (&DeclarativeProvisioningService{}).loadState(); err == nil && state != nil {
			view.Policy = FairSharePolicy{AvailBitPerSec: state.Request.Config.NodeBandwidthBps}
		}
	}
	return view, nil
}

func (s *FairShareService) SavePolicy(policy *FairSharePolicy) error {
	if IsDeclarativelyManaged() {
		return ErrFairShareManaged
	}
	if err := validateFairSharePolicy(policy); err != nil {
		return err
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if err := s.settingService.saveSetting(fairSharePolicySettingKey, string(encoded)); err != nil {
		return err
	}
	// A stopped core is not a failed save: Reapply pushes the stored policy the
	// moment one starts, so refusing here would only block configuring ahead.
	if !s.xrayService.IsXrayRunning() {
		return nil
	}
	return s.push(policy)
}

// GetStatus reads the running scheduler. A stopped core is reported as
// Running=false rather than an error: "no numbers yet" is a state of the node
// page, not a failure of the request.
func (s *FairShareService) GetStatus() (*FairShareStatusView, error) {
	if !s.xrayService.IsXrayRunning() {
		return &FairShareStatusView{}, nil
	}
	status, err := s.xrayService.GetFairShareStatus()
	if err != nil {
		return nil, err
	}
	return &FairShareStatusView{
		Running:                 true,
		RootCapBitPerSec:        status.RootCapBitPerSec,
		Congested:               status.Congested,
		ActiveMembers:           status.ActiveMembers,
		FillTruncated:           status.FillTruncated,
		FillUnresolvedMembers:   status.FillUnresolvedMembers,
		FillTruncatedTicks:      status.FillTruncatedTicks,
		FillTruncatedTotalTicks: status.FillTruncatedTotalTicks,
		FillRounds:              status.FillRounds,
	}, nil
}

// Reapply pushes the stored policy into a freshly started core. A new process
// starts with an empty scheduler, so without this the saved policy quietly
// stops applying after any restart.
func (s *FairShareService) Reapply() {
	// Called from a goroutine off the restart path, which some tests drive with
	// no database at all; a nil handle there would panic the whole process.
	if database.GetDB() == nil || IsDeclarativelyManaged() {
		return
	}
	policy, err := s.GetPolicy()
	if err != nil {
		logger.Warning("fair-share policy could not be read back after restart:", err)
		return
	}
	// A fresh core already starts with fair sharing off, so an unconfigured node
	// has nothing to re-push — and pushing anyway would make every restart wait
	// on the core's API port for nothing.
	if isEmptyFairSharePolicy(policy) {
		return
	}
	if err := s.push(policy); err != nil {
		logger.Warning("fair-share policy could not be re-applied after restart:", err)
	}
}

func (s *FairShareService) push(policy *FairSharePolicy) error {
	if err := s.xrayService.SetNodeBandwidth(xray.NodeFairShare{
		AvailBitPerSec:         policy.AvailBitPerSec,
		SoftFloorBitPerSec:     policy.SoftFloorBitPerSec,
		HardFloorBitPerSec:     policy.HardFloorBitPerSec,
		CongestionEnterPercent: policy.CongestionEnterPercent,
		CongestionExitPercent:  policy.CongestionExitPercent,
		CongestionExitTicks:    policy.CongestionExitTicks,
	}); err != nil {
		return fmt.Errorf("apply node fair-share: %w", err)
	}
	classes := make([]xray.ClassFairShare, 0, len(policy.Classes))
	for _, class := range policy.Classes {
		classes = append(classes, xray.ClassFairShare{
			Name:               class.Name,
			Weight:             class.Weight,
			NormalCapBitPerSec: class.NormalCapBitPerSec,
			BurstCapBitPerSec:  class.BurstCapBitPerSec,
			BurstCreditBytes:   class.BurstCreditBytes,
			FloorRatioPercent:  class.FloorRatioPercent,
		})
	}
	if err := s.xrayService.SetClassPolicy(classes); err != nil {
		return fmt.Errorf("apply class policy: %w", err)
	}
	return nil
}

// isEmptyFairSharePolicy reports a policy where nothing at all is enabled.
func isEmptyFairSharePolicy(policy *FairSharePolicy) bool {
	return len(policy.Classes) == 0 &&
		policy.AvailBitPerSec == 0 &&
		policy.SoftFloorBitPerSec == 0 &&
		policy.HardFloorBitPerSec == 0 &&
		policy.CongestionEnterPercent == 0 &&
		policy.CongestionExitPercent == 0 &&
		policy.CongestionExitTicks == 0
}

func validateFairSharePolicy(policy *FairSharePolicy) error {
	for _, percent := range []uint32{policy.CongestionEnterPercent, policy.CongestionExitPercent} {
		if percent > 100 {
			return fmt.Errorf("congestion threshold %d%% is above 100%%", percent)
		}
	}
	if policy.CongestionExitPercent > policy.CongestionEnterPercent {
		return fmt.Errorf("congestion exit %d%% is above enter %d%%, which the core ignores", policy.CongestionExitPercent, policy.CongestionEnterPercent)
	}
	if policy.HardFloorBitPerSec > 0 && policy.SoftFloorBitPerSec > 0 && policy.HardFloorBitPerSec > policy.SoftFloorBitPerSec {
		return fmt.Errorf("hard floor is above the soft floor, which makes the soft floor unreachable")
	}
	seen := make(map[string]bool, len(policy.Classes))
	for _, class := range policy.Classes {
		if seen[class.Name] {
			return fmt.Errorf("duplicate class %q: the class table is replaced whole, so one of the two would be lost", class.Name)
		}
		seen[class.Name] = true
		if class.FloorRatioPercent > 100 {
			return fmt.Errorf("class %q floor ratio %d%% is above 100%%", class.Name, class.FloorRatioPercent)
		}
		if class.BurstCapBitPerSec > 0 && class.BurstCapBitPerSec <= class.NormalCapBitPerSec {
			return fmt.Errorf("class %q burst cap is not above its normal cap, so it would never burst", class.Name)
		}
		if class.BurstCreditBytes > 0 && class.BurstCapBitPerSec == 0 {
			return fmt.Errorf("class %q has burst credit but no burst cap, so the credit is never spent", class.Name)
		}
	}
	return nil
}
