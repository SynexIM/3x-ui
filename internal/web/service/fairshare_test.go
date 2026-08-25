package service

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

func initFairShareDB(t *testing.T) *FairShareService {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	return &FairShareService{}
}

// Nothing was ever configured, so nothing may be enabled. A default bandwidth
// invented here would shape every client on a node the operator never touched.
func TestUnsetPolicyIsEntirelyOff(t *testing.T) {
	service := initFairShareDB(t)
	policy, err := service.GetPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*policy, FairSharePolicy{}) {
		t.Fatalf("unset policy = %+v, want every field zero", *policy)
	}
}

func TestPolicySurvivesASaveWhileXrayIsDown(t *testing.T) {
	service := initFairShareDB(t)
	want := &FairSharePolicy{
		AvailBitPerSec:         1_000_000_000,
		SoftFloorBitPerSec:     500_000,
		CongestionEnterPercent: 85,
		CongestionExitPercent:  70,
		CongestionExitTicks:    5,
		Classes: []FairShareClassPolicy{{
			Name:               "live",
			Weight:             3,
			NormalCapBitPerSec: 20_000_000,
			BurstCapBitPerSec:  50_000_000,
			BurstCreditBytes:   1_000_000_000,
			FloorRatioPercent:  20,
		}},
	}
	if err := service.SavePolicy(want); err != nil {
		t.Fatalf("save with no core running: %v", err)
	}
	got, err := service.GetPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if got.AvailBitPerSec != want.AvailBitPerSec || len(got.Classes) != 1 || got.Classes[0] != want.Classes[0] {
		t.Fatalf("read back %+v, want %+v", *got, *want)
	}
}

// An API client applying desired state must not turn the panel into its
// attachment: the operator keeps the local edit, and the view says out loud that
// a later reconciliation may put the automated value back.
func TestSaveStillWorksWhileAnApiClientManagesTheNode(t *testing.T) {
	service := initFairShareDB(t)
	if err := (&SettingService{}).saveSetting(declarativeProvisioningStateKey, `{"Request":{"revision":1}}`); err != nil {
		t.Fatal(err)
	}
	if err := service.SavePolicy(&FairSharePolicy{AvailBitPerSec: 1_000_000, SoftFloorBitPerSec: 250_000}); err != nil {
		t.Fatalf("save while managed = %v, want it to go through", err)
	}
	got, err := service.GetPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if got.SoftFloorBitPerSec != 250_000 {
		t.Fatalf("soft floor read back as %d, want the 250000 that was just saved", got.SoftFloorBitPerSec)
	}
	view, err := service.GetPolicyView()
	if err != nil {
		t.Fatal(err)
	}
	if !view.DeclarativelyManaged {
		t.Fatal("the view must warn that automation manages this node before anything is clicked")
	}
}

func TestValidationRejectsWhatTheCoreWouldIgnore(t *testing.T) {
	cases := []struct {
		name   string
		policy FairSharePolicy
		want   string
	}{
		{
			name:   "exit above enter",
			policy: FairSharePolicy{CongestionEnterPercent: 70, CongestionExitPercent: 85},
			want:   "above enter",
		},
		{
			name:   "percent over 100",
			policy: FairSharePolicy{CongestionEnterPercent: 120},
			want:   "above 100%",
		},
		{
			name:   "hard floor above soft floor",
			policy: FairSharePolicy{SoftFloorBitPerSec: 500_000, HardFloorBitPerSec: 1_000_000},
			want:   "hard floor is above the soft floor",
		},
		{
			name:   "burst cap not above normal cap",
			policy: FairSharePolicy{Classes: []FairShareClassPolicy{{Name: "live", NormalCapBitPerSec: 20, BurstCapBitPerSec: 20}}},
			want:   "never burst",
		},
		{
			name:   "burst credit with no burst cap",
			policy: FairSharePolicy{Classes: []FairShareClassPolicy{{Name: "live", BurstCreditBytes: 1}}},
			want:   "never spent",
		},
		{
			name:   "duplicate class",
			policy: FairSharePolicy{Classes: []FairShareClassPolicy{{Name: "live"}, {Name: "live"}}},
			want:   "duplicate class",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFairSharePolicy(&tc.policy)
			if err == nil {
				t.Fatalf("accepted %+v", tc.policy)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A blank field is 0 everywhere, and 0 must stay a legal saved value: it is how
// the operator turns something back off.
func TestAnAllBlankPolicyIsValid(t *testing.T) {
	if err := validateFairSharePolicy(&FairSharePolicy{}); err != nil {
		t.Fatalf("all-blank policy rejected: %v", err)
	}
}
