package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Omitted fields stay unchanged; explicit zero clears only that runtime limit.
type ClientRuntimePatch struct {
	BandwidthBps         *uint64 `json:"bandwidth_bps,omitempty"`
	CommittedBps         *uint64 `json:"committed_bps,omitempty"`
	CommittedBurstBytes  *uint64 `json:"committed_burst_bytes,omitempty"`
	UploadBandwidthBps   *uint64 `json:"upload_bandwidth_bps,omitempty"`
	UploadPeakBps        *uint64 `json:"upload_peak_bps,omitempty"`
	UploadBurstBytes     *uint64 `json:"upload_burst_bytes,omitempty"`
	DownloadBandwidthBps *uint64 `json:"download_bandwidth_bps,omitempty"`
	DownloadPeakBps      *uint64 `json:"download_peak_bps,omitempty"`
	DownloadBurstBytes   *uint64 `json:"download_burst_bytes,omitempty"`
	ConnLimit            *uint32 `json:"conn_limit,omitempty"`
	EgressTag            *string `json:"egress_tag,omitempty"`
	// Pool shaping over the upload/download standard rates (symmetric).
	BurstBps         *uint64 `json:"burst_bps,omitempty"`
	BurstCreditBytes *uint64 `json:"burst_credit_bytes,omitempty"`
	SustainedBps     *uint64 `json:"sustained_bps,omitempty"`
	// Pool and class are opaque names set by the caller; empty clears them.
	Pool  *string `json:"pool,omitempty"`
	Class *string `json:"class,omitempty"`
}

type ClientRuntimeReceipt struct {
	HotApplied      bool `json:"hotApplied" example:"true"`
	RequiresRestart bool `json:"requiresRestart" example:"false"`
	NodePending     bool `json:"nodePending" example:"false"`
}

// UpdateRuntime never rewrites credentials, validity, traffic, or per-inbound flow.
func (s *ClientService) UpdateRuntime(ctx context.Context, inboundSvc *InboundService, email string, patch ClientRuntimePatch) error {
	updates := map[string]any{}
	for key, value := range map[string]*uint64{
		"bandwidth_bps":          patch.BandwidthBps,
		"committed_bps":          patch.CommittedBps,
		"committed_burst_bytes":  patch.CommittedBurstBytes,
		"upload_bandwidth_bps":   patch.UploadBandwidthBps,
		"upload_peak_bps":        patch.UploadPeakBps,
		"upload_burst_bytes":     patch.UploadBurstBytes,
		"download_bandwidth_bps": patch.DownloadBandwidthBps,
		"download_peak_bps":      patch.DownloadPeakBps,
		"download_burst_bytes":   patch.DownloadBurstBytes,
		"burst_bps":              patch.BurstBps,
		"burst_credit_bytes":     patch.BurstCreditBytes,
		"sustained_bps":          patch.SustainedBps,
	} {
		if value != nil {
			updates[key] = *value
		}
	}
	if patch.ConnLimit != nil {
		updates["conn_limit"] = *patch.ConnLimit
	}
	if patch.Pool != nil {
		updates["pool"] = strings.TrimSpace(*patch.Pool)
	}
	if patch.Class != nil {
		updates["class"] = strings.TrimSpace(*patch.Class)
	}
	if patch.EgressTag != nil {
		updates["egress_tag"] = strings.TrimSpace(*patch.EgressTag)
	}
	if len(updates) == 0 {
		return errors.New("at least one runtime field is required")
	}
	updates["updated_at"] = time.Now().UnixMilli()
	return s.updateClientRecordLive(ctx, inboundSvc, email, updates, func(_ *gorm.DB, record model.ClientRecord, _ []model.Inbound) error {
		return validateClientTiers(record, updates)
	})
}

// updateClientRecordLive writes one client row, then pushes the result to the
// node inbounds it is attached to. Local inbounds are left to the caller's
// hot-apply, which rebuilds the full runtime user; check runs inside the tx.
func (s *ClientService) updateClientRecordLive(
	ctx context.Context,
	inboundSvc *InboundService,
	email string,
	updates map[string]any,
	check func(tx *gorm.DB, record model.ClientRecord, inbounds []model.Inbound) error,
) error {
	var record model.ClientRecord
	var links []model.ClientInbound
	var inbounds []model.Inbound
	if err := runSerializedTx(func(tx *gorm.DB) error {
		tx = tx.WithContext(ctx)
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email = ?", email).First(&record).Error; err != nil {
			return err
		}
		if err := tx.Where("client_id = ?", record.Id).Find(&links).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN (?)", tx.Model(&model.ClientInbound{}).Select("inbound_id").Where("client_id = ?", record.Id)).Find(&inbounds).Error; err != nil {
			return err
		}
		if err := check(tx, record, inbounds); err != nil {
			return err
		}
		if err := tx.Model(&model.ClientRecord{}).Where("id = ?", record.Id).Updates(updates).Error; err != nil {
			return err
		}
		nodes := map[int]bool{}
		for _, inbound := range inbounds {
			if inbound.NodeID == nil || nodes[*inbound.NodeID] {
				continue
			}
			nodes[*inbound.NodeID] = true
			if err := (&NodeService{}).MarkNodeDirtyTx(tx, *inbound.NodeID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := database.GetDB().WithContext(ctx).First(&record, record.Id).Error; err != nil {
		return err
	}
	flows := make(map[int]string, len(links))
	for _, link := range links {
		flows[link.InboundId] = link.FlowOverride
	}
	for i := range inbounds {
		inbound := &inbounds[i]
		// The local runtime's own UpdateUser would re-add the user without limits first.
		if inbound.NodeID == nil {
			continue
		}
		rt, push, _, err := inboundSvc.nodePushPlan(inbound)
		if err != nil {
			return err
		}
		if !push {
			continue
		}
		client := record.ToClient()
		client.Flow = flows[inbound.Id]
		if err := rt.UpdateUser(ctx, inbound, email, *client); err != nil {
			return err
		}
	}
	return nil
}

// ErrClientTierOrder rejects a tier set that violates burst >= standard >= sustained.
var ErrClientTierOrder = errors.New("three-tier limits must satisfy burst_bps >= standard >= sustained_bps")

// validateClientTiers checks the record as it will look after updates.
func validateClientTiers(current model.ClientRecord, updates map[string]any) error {
	pick := func(key string, value uint64) uint64 {
		if v, ok := updates[key].(uint64); ok {
			return v
		}
		return value
	}
	up := pick("upload_bandwidth_bps", current.UploadBandwidthBps)
	down := pick("download_bandwidth_bps", current.DownloadBandwidthBps)
	burst := pick("burst_bps", current.BurstBps)
	sustained := pick("sustained_bps", current.SustainedBps)
	for _, standard := range []uint64{up, down} {
		if standard == 0 {
			continue
		}
		if burst != 0 && burst < standard || sustained > standard {
			return ErrClientTierOrder
		}
	}
	return nil
}
