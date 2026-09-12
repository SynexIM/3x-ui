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
	} {
		if value != nil {
			updates[key] = *value
		}
	}
	if patch.ConnLimit != nil {
		updates["conn_limit"] = *patch.ConnLimit
	}
	if patch.EgressTag != nil {
		updates["egress_tag"] = strings.TrimSpace(*patch.EgressTag)
	}
	if len(updates) == 0 {
		return errors.New("at least one runtime field is required")
	}
	updates["updated_at"] = time.Now().UnixMilli()

	var record model.ClientRecord
	var links []model.ClientInbound
	var inbounds []model.Inbound
	if err := runSerializedTx(func(tx *gorm.DB) error {
		tx = tx.WithContext(ctx)
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email = ?", email).First(&record).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ClientRecord{}).Where("id = ?", record.Id).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Where("client_id = ?", record.Id).Find(&links).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN (?)", tx.Model(&model.ClientInbound{}).Select("inbound_id").Where("client_id = ?", record.Id)).Find(&inbounds).Error; err != nil {
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
