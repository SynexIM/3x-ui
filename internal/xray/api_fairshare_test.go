package xray

import (
	"context"
	"net"
	"testing"

	fairShareService "github.com/xtls/xray-core/app/fairshare/command"
	"google.golang.org/grpc"
)

type fakeFairShareServer struct {
	fairShareService.UnimplementedFairShareServiceServer
	node    *fairShareService.SetNodeBandwidthRequest
	classes []*fairShareService.ClassPolicy
	status  *fairShareService.GetStatusResponse
}

func (f *fakeFairShareServer) SetNodeBandwidth(
	_ context.Context,
	request *fairShareService.SetNodeBandwidthRequest,
) (*fairShareService.SetNodeBandwidthResponse, error) {
	f.node = request
	return &fairShareService.SetNodeBandwidthResponse{}, nil
}

func (f *fakeFairShareServer) SetClassPolicy(
	_ context.Context,
	request *fairShareService.SetClassPolicyRequest,
) (*fairShareService.SetClassPolicyResponse, error) {
	f.classes = request.Classes
	return &fairShareService.SetClassPolicyResponse{}, nil
}

func (f *fakeFairShareServer) GetStatus(
	_ context.Context,
	_ *fairShareService.GetStatusRequest,
) (*fairShareService.GetStatusResponse, error) {
	return f.status, nil
}

func dialFakeFairShare(t *testing.T) (*XrayAPI, *fakeFairShareServer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	fake := &fakeFairShareServer{}
	fairShareService.RegisterFairShareServiceServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	api := &XrayAPI{}
	if err := api.Init(listener.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	return api, fake
}

// The panel counts bit/s, the fairshare proto counts byte/s, and both spell the
// suffix "bps". Every rate crossing this boundary must lose exactly a factor of
// 8, and everything that is not a rate must cross unchanged.
func TestFairShareRatesCrossTheBoundaryAsBytes(t *testing.T) {
	api, fake := dialFakeFairShare(t)

	if err := api.SetNodeBandwidth(NodeFairShare{
		AvailBitPerSec:         800_000_000,
		SoftFloorBitPerSec:     4_000_000,
		HardFloorBitPerSec:     800_000,
		CongestionEnterPercent: 85,
		CongestionExitPercent:  70,
		CongestionExitTicks:    5,
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		field string
		got   uint64
		want  uint64
	}{
		{"avail_bps", fake.node.AvailBps, 100_000_000},
		{"soft_floor_bps", fake.node.SoftFloorBps, 500_000},
		{"hard_floor_bps", fake.node.HardFloorBps, 100_000},
		{"congestion_enter_percent", uint64(fake.node.CongestionEnterPercent), 85},
		{"congestion_exit_percent", uint64(fake.node.CongestionExitPercent), 70},
		{"congestion_exit_ticks", uint64(fake.node.CongestionExitTicks), 5},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.field, tc.got, tc.want)
		}
	}

	if err := api.SetClassPolicy([]ClassFairShare{{
		Name:               "live",
		Weight:             3,
		NormalCapBitPerSec: 160_000_000,
		BurstCapBitPerSec:  400_000_000,
		BurstCreditBytes:   1_000_000_000,
		FloorRatioPercent:  20,
	}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.classes) != 1 {
		t.Fatalf("classes = %d, want 1", len(fake.classes))
	}
	class := fake.classes[0]
	for _, tc := range []struct {
		field string
		got   uint64
		want  uint64
	}{
		{"normal_cap_byte_per_sec", class.NormalCapBytePerSec, 20_000_000},
		{"burst_cap_byte_per_sec", class.BurstCapBytePerSec, 50_000_000},
		// A credit is a size, not a rate: bytes on both sides, no factor of 8.
		{"burst_credit_bytes", class.BurstCreditBytes, 1_000_000_000},
		{"weight", uint64(class.Weight), 3},
		{"floor_ratio_percent", uint64(class.FloorRatioPercent), 20},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.field, tc.got, tc.want)
		}
	}
	if class.Name != "live" {
		t.Errorf("name = %q, want live", class.Name)
	}
}

// An empty class list is a real instruction — clear the table — so it must still
// reach the core rather than be optimised away into "no call".
func TestSetClassPolicySendsAnEmptyTable(t *testing.T) {
	api, fake := dialFakeFairShare(t)
	fake.classes = []*fairShareService.ClassPolicy{{Name: "stale"}}
	if err := api.SetClassPolicy(nil); err != nil {
		t.Fatal(err)
	}
	if len(fake.classes) != 0 {
		t.Fatalf("classes = %d, want the table cleared", len(fake.classes))
	}
}

func TestGetFairShareStatusReadsBytesBackAsBits(t *testing.T) {
	api, fake := dialFakeFairShare(t)
	fake.status = &fairShareService.GetStatusResponse{
		RootCapBytePerSec:       100_000_000,
		Congested:               true,
		ActiveMembers:           12,
		FillTruncated:           true,
		FillUnresolvedMembers:   4,
		FillTruncatedTicks:      7,
		FillTruncatedTotalTicks: 91,
		FillRounds:              8,
	}
	status, err := api.GetFairShareStatus()
	if err != nil {
		t.Fatal(err)
	}
	want := &FairShareStatus{
		RootCapBitPerSec:        800_000_000,
		Congested:               true,
		ActiveMembers:           12,
		FillTruncated:           true,
		FillUnresolvedMembers:   4,
		FillTruncatedTicks:      7,
		FillTruncatedTotalTicks: 91,
		FillRounds:              8,
	}
	if *status != *want {
		t.Fatalf("status = %+v, want %+v", *status, *want)
	}
}
