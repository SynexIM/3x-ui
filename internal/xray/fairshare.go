package xray

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/util/common"

	fairShareService "github.com/xtls/xray-core/app/fairshare/command"
)

// NodeFairShare is the node-level fair-share policy in the unit the panel and
// its API speak everywhere: bit/s. 0 means "not enabled", never "use a default".
type NodeFairShare struct {
	AvailBitPerSec         uint64
	CongestionEnterPercent uint32
	CongestionExitPercent  uint32
	CongestionExitTicks    uint32
}

// ClassFairShare is one set of contention parameters, rates in bit/s.
type ClassFairShare struct {
	Name                      string
	Weight                    uint32
	FloorBitPerSec            uint64
	UploadReservedBitPerSec   uint64
	DownloadReservedBitPerSec uint64
	HeavyWindowSeconds        uint32
	HeavyPercent              uint32
}

// FairShareStatus is the scheduler's runtime state, rates converted back to bit/s.
type FairShareStatus struct {
	RootCapBitPerSec        uint64
	Congested               bool
	ActiveMembers           uint32
	FillTruncated           bool
	FillUnresolvedMembers   uint32
	FillTruncatedTicks      uint64
	FillTruncatedTotalTicks uint64
	FillRounds              uint32
	HeavyMembers            uint32
	UsedUploadBitPerSec     uint64
	UsedDownloadBitPerSec   uint64
}

// bitToBytePerSec is the ONLY place the panel's bit/s becomes the fairshare
// proto's byte/s. Both sides spell the suffix "bps"; they differ by 8 (FR-079d).
func bitToBytePerSec(bitPerSec uint64) uint64 { return bitPerSec / 8 }

// byteToBitPerSec is its inverse, used to read GetStatus back into panel units.
func byteToBitPerSec(bytePerSec uint64) uint64 { return bytePerSec * 8 }

func (x *XrayAPI) SetNodeBandwidth(policy NodeFairShare) error {
	if x.FairShareServiceClient == nil {
		return common.NewError("xray FairShareServiceClient is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), handlerRPCTimeout)
	defer cancel()
	_, err := x.FairShareServiceClient.SetNodeBandwidth(ctx, &fairShareService.SetNodeBandwidthRequest{
		AvailBps:               bitToBytePerSec(policy.AvailBitPerSec),
		CongestionEnterPercent: policy.CongestionEnterPercent,
		CongestionExitPercent:  policy.CongestionExitPercent,
		CongestionExitTicks:    policy.CongestionExitTicks,
	})
	return err
}

// SetClassPolicy replaces the whole class table: a class missing from classes
// is deleted from the scheduler, so callers must always send the full set.
func (x *XrayAPI) SetClassPolicy(classes []ClassFairShare) error {
	if x.FairShareServiceClient == nil {
		return common.NewError("xray FairShareServiceClient is not initialized")
	}
	request := &fairShareService.SetClassPolicyRequest{Classes: make([]*fairShareService.ClassPolicy, 0, len(classes))}
	for _, class := range classes {
		request.Classes = append(request.Classes, &fairShareService.ClassPolicy{
			Name:                       class.Name,
			Weight:                     class.Weight,
			FloorBytePerSec:            bitToBytePerSec(class.FloorBitPerSec),
			UploadReservedBytePerSec:   bitToBytePerSec(class.UploadReservedBitPerSec),
			DownloadReservedBytePerSec: bitToBytePerSec(class.DownloadReservedBitPerSec),
			HeavyWindowSeconds:         class.HeavyWindowSeconds,
			HeavyPercent:               class.HeavyPercent,
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), handlerRPCTimeout)
	defer cancel()
	_, err := x.FairShareServiceClient.SetClassPolicy(ctx, request)
	return err
}

func (x *XrayAPI) GetFairShareStatus() (*FairShareStatus, error) {
	if x.FairShareServiceClient == nil {
		return nil, common.NewError("xray FairShareServiceClient is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), handlerRPCTimeout)
	defer cancel()
	response, err := x.FairShareServiceClient.GetStatus(ctx, &fairShareService.GetStatusRequest{})
	if err != nil {
		return nil, err
	}
	return &FairShareStatus{
		RootCapBitPerSec:        byteToBitPerSec(response.RootCapBytePerSec),
		Congested:               response.Congested,
		ActiveMembers:           response.ActiveMembers,
		FillTruncated:           response.FillTruncated,
		FillUnresolvedMembers:   response.FillUnresolvedMembers,
		FillTruncatedTicks:      response.FillTruncatedTicks,
		FillTruncatedTotalTicks: response.FillTruncatedTotalTicks,
		FillRounds:              response.FillRounds,
		HeavyMembers:            response.HeavyMembers,
		UsedUploadBitPerSec:     byteToBitPerSec(response.UsedUploadBytePerSec),
		UsedDownloadBitPerSec:   byteToBitPerSec(response.UsedDownloadBytePerSec),
	}, nil
}
