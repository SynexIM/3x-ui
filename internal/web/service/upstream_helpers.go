package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"

	"gorm.io/gorm"
)

// Helpers carried from upstream v3.8.5 whose original files the fork replaced
// with its normalized-client versions; upstream callers elsewhere still use them.

// Rejected rather than coerced: an unknown cycle would leave the operator with
// a field that reads as configured while no job ever selects the client.
func validateClientTrafficReset(period string, day int) error {
	switch period {
	case "", "never", "hourly", "daily", "weekly", "monthly":
	default:
		return common.NewError("client trafficReset must be never, hourly, daily, weekly or monthly, got:", period)
	}
	if day < 0 || day > 31 {
		return common.NewError("client trafficResetDay must be between 0 and 31, got:", day)
	}
	return nil
}

// Rejected rather than clamped: nextCalendarRenewal would silently move an
// out-of-range day, and a negative one drops out of the renewal query entirely.
func validateClientResetDay(day int) error {
	if day < 0 || day > 31 {
		return common.NewError("client resetDay must be between 0 and 31, got:", day)
	}
	return nil
}

// Rejected rather than coerced: a negative cap reads as "unlimited" to a caller
// but selects nothing, so the client would silently stop renewing.
func validateClientResetMax(resetMax int) error {
	if resetMax < 0 {
		return common.NewError("client resetMax must not be negative, got:", resetMax)
	}
	return nil
}

// inboundFanoutConcurrency caps how many inbounds one client op applies at
// once, so a client spanning many of them can't start an unbounded RPC burst.
const inboundFanoutConcurrency = 4

// inboundApply is one inbound's share of a client op, ready to run.
type inboundApply struct {
	id  int
	run func() (bool, error)
}

// fanoutInboundApplies runs the applies with the node pushes overlapping, so a
// client spanning several nodes no longer costs one RPC round-trip per node.
func fanoutInboundApplies(applies []inboundApply) (bool, error) {
	var needRestart atomic.Bool
	errs := make([]error, len(applies))
	sem := make(chan struct{}, inboundFanoutConcurrency)
	var wg sync.WaitGroup
	for i := range applies {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// Off the request goroutine gin's Recovery no longer covers this,
			// so an unrecovered panic here would take the whole panel down.
			defer func() {
				if r := recover(); r != nil {
					// The apply may already have committed, so ask for the
					// restart the lost return value can no longer report.
					needRestart.Store(true)
					errs[i] = fmt.Errorf("inbound %d: panic: %v", applies[i].id, r)
					logger.Errorf("panic applying client change to inbound %d: %v\n%s", applies[i].id, r, debug.Stack())
				}
			}()
			nr, err := applies[i].run()
			if nr {
				needRestart.Store(true)
			}
			if err != nil {
				errs[i] = fmt.Errorf("inbound %d: %w", applies[i].id, err)
			}
		}()
	}
	wg.Wait()

	return needRestart.Load(), errors.Join(errs...)
}

// fanoutInboundResults runs one job per inbound with the node pushes
// overlapping, so a bulk op costs one RPC round-trip instead of one per node.
// limit is the caller's own cap: an op that allocates tunnel addresses passes 1,
// because allocation reads a cross-inbound used-set before it writes.
func fanoutInboundResults[T any](inboundIds []int, limit int, run func(i int) T) ([]T, []error) {
	if limit < 1 {
		limit = 1
	}
	out := make([]T, len(inboundIds))
	errs := make([]error, len(inboundIds))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range inboundIds {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// Off the request goroutine gin's Recovery no longer covers this,
			// so an unrecovered panic here would take the whole panel down.
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("inbound %d: panic: %v", inboundIds[i], r)
					logger.Errorf("panic applying bulk client change to inbound %d: %v\n%s", inboundIds[i], r, debug.Stack())
				}
			}()
			out[i] = run(i)
		}()
	}
	wg.Wait()
	return out, errs
}

func ParseInboundSettingsClients(settings string) ([]model.Client, error) {
	trimmed := strings.TrimSpace(settings)
	if trimmed == "" || trimmed == "null" {
		return nil, common.NewError("inbound settings is empty")
	}

	var payload struct {
		Clients json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, err
	}
	if len(payload.Clients) == 0 || string(payload.Clients) == "null" {
		return nil, nil
	}

	var clients []model.Client
	if err := json.Unmarshal(payload.Clients, &clients); err != nil {
		return nil, err
	}
	return clients, nil
}

// ClientResetCycle is the slice of a client the reset job needs: enough to know
// whether it is due, and whether its disable is the quota's doing or the operator's.
type ClientResetCycle struct {
	Email           string
	TrafficResetDay int
	Enable          bool
	Total           int64
	Used            int64
}

// Depleted reports a client the quota switched off. A reset restores that one;
// a client disabled below its quota was switched off by hand and stays off.
func (c ClientResetCycle) Depleted() bool {
	return c.Total > 0 && c.Used >= c.Total
}

// GetClientsByTrafficReset returns the clients whose own reset cycle matches the
// period, independent of the cycle configured on the inbounds they belong to.
func (s *ClientService) GetClientsByTrafficReset(period string) ([]ClientResetCycle, error) {
	var cycles []ClientResetCycle
	err := database.GetDB().Table("clients c").
		Select("c.email, c.traffic_reset_day, c.enable, COALESCE(ct.total, 0) AS total, COALESCE(ct.up, 0) + COALESCE(ct.down, 0) AS used").
		Joins("LEFT JOIN client_traffics ct ON ct.email = c.email").
		Where("c.traffic_reset = ?", period).
		Scan(&cycles).Error
	if err != nil {
		return nil, err
	}
	return cycles, nil
}

// SetInboundSubSortIndex changes only the subscription sort order, so a
// reorder cannot carry a stale settings/client payload over another edit.
func (s *InboundService) SetInboundSubSortIndex(id int, index int) error {
	index = normalizeSubSortIndex(index)
	inbound, err := s.GetInbound(id)
	if err != nil {
		return err
	}
	if inbound.SubSortIndex == index {
		return nil
	}

	db := database.GetDB()
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(model.Inbound{}).Where("id = ?", id).
			Update("sub_sort_index", index).Error; err != nil {
			return err
		}
		if inbound.NodeID != nil {
			return (&NodeService{}).MarkNodeDirtyTx(tx, *inbound.NodeID)
		}
		return nil
	}); err != nil {
		return err
	}
	inbound.SubSortIndex = index

	if inbound.NodeID == nil {
		return nil
	}
	rt, push, _, perr := s.nodePushPlan(inbound)
	if perr != nil {
		return perr
	}
	if push {
		narrow, ok := rt.(interface {
			SetInboundSubSortIndex(context.Context, *model.Inbound, int) error
		})
		if !ok {
			return fmt.Errorf("runtime %s does not support narrow subscription ordering updates", rt.Name())
		}
		if err := narrow.SetInboundSubSortIndex(context.Background(), inbound, index); err != nil {
			logger.Warning("SetInboundSubSortIndex: remote metadata update on", rt.Name(), "failed:", err)
		}
	}
	return nil
}

// checkInboundKeyPair parses the pair the way Xray does. Xray drops a pair it
// cannot parse with only a warning, leaving the listener certificate-less.
// Inline PEM is always checked; a file path is checked only when both files
// are readable here, since a node's paths live on the node.
func checkInboundKeyPair(certFile, keyFile string, certLines, keyLines []string) error {
	var certPEM, keyPEM []byte
	if certFile != "" || keyFile != "" {
		c, errC := os.ReadFile(certFile)
		k, errK := os.ReadFile(keyFile)
		if errC != nil || errK != nil {
			return nil
		}
		certPEM, keyPEM = c, k
	} else {
		certPEM, keyPEM = []byte(strings.Join(certLines, "\n")), []byte(strings.Join(keyLines, "\n"))
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	_, err = x509.ParseCertificate(pair.Certificate[0])
	return err
}

// validateInboundTLSCertificates rejects incomplete TLS credentials before a save
// can restart Xray.
func validateInboundTLSCertificates(streamSettings string) error {
	if strings.TrimSpace(streamSettings) == "" {
		return nil
	}
	var stream struct {
		Security    string          `json:"security"`
		TLSSettings json.RawMessage `json:"tlsSettings"`
	}
	if err := json.Unmarshal([]byte(streamSettings), &stream); err != nil {
		return common.NewError("Invalid inbound stream settings: ", err)
	}
	if !strings.EqualFold(stream.Security, "tls") {
		return nil
	}
	var settings struct {
		Certificates []struct {
			CertificateFile string   `json:"certificateFile"`
			KeyFile         string   `json:"keyFile"`
			Certificate     []string `json:"certificate"`
			Key             []string `json:"key"`
			Usage           string   `json:"usage"`
		} `json:"certificates"`
	}
	if len(stream.TLSSettings) > 0 {
		if err := json.Unmarshal(stream.TLSSettings, &settings); err != nil {
			return common.NewError("Invalid inbound TLS settings: ", err)
		}
	}
	hasServerCertificate := false
	for i, cert := range settings.Certificates {
		// Match Xray's file-over-inline precedence for each credential.
		certificate := cert.CertificateFile
		if certificate == "" {
			certificate = strings.Join(cert.Certificate, "\n")
		}
		if strings.TrimSpace(certificate) == "" {
			return common.NewErrorf("TLS certificate %d is missing. Configure a certificate file path or certificate content before saving the inbound.", i+1)
		}
		if strings.EqualFold(cert.Usage, "verify") {
			continue
		}
		key := cert.KeyFile
		if key == "" {
			key = strings.Join(cert.Key, "\n")
		}
		if strings.TrimSpace(key) == "" {
			return common.NewErrorf("TLS certificate %d is missing its private key. Configure a private key file path or private key content before saving the inbound.", i+1)
		}
		if err := checkInboundKeyPair(cert.CertificateFile, cert.KeyFile, cert.Certificate, cert.Key); err != nil {
			return common.NewErrorf("TLS certificate %d cannot be loaded by Xray: %v. Xray would skip it and every handshake would fail with \"unrecognized name\"; EC keys must use a named curve (e.g. openssl ecparam -name prime256v1 -param_enc named_curve).", i+1, err)
		}
		hasServerCertificate = true
	}
	if !hasServerCertificate {
		return common.NewError("TLS requires a server certificate and private key. Configure an encipherment or issue certificate before saving the inbound.")
	}
	return nil
}
