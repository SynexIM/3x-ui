package service

// Single-object hot apply.
//
// A write path that already knows which object it changed does not need the
// whole desired config rebuilt and diffed to prove the change reached the
// core: it can drive the core's own per-object gRPC primitive and read the
// acknowledgement. This removes the runtime-reconciliation O(N) cost. Legacy
// JSON persistence still scales with the stored array and is measured
// separately until those objects have first-class storage.
//
// Two properties every operation here keeps, because the callers persist
// before they apply and must stay retryable:
//
//  1. A retry after a failed runtime write still applies. Nothing here reads
//     "the database already says so" and skips — the primitive is always sent,
//     and the reconciling helpers turn an "already exists" into a replace.
//  2. Success is never reported unless the core acknowledged every operation.
//
// The process's proven-config snapshot is deliberately NOT advanced here, so
// it stays a lower bound on what the core holds. That is safe because every
// operation a snapshot-to-desired diff can emit is idempotent: a later whole
// config reconcile re-sends what is already there and the core accepts it.
// Advancing it would mean re-marshalling every client or rule on the machine,
// which is the cost this file exists to remove.

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

// errNeedsFullReconcile means this change cannot be proven one object at a
// time — the machine has drifted further than the object that just changed.
var errNeedsFullReconcile = errors.New("this change needs a whole-config reconcile")

// ApplyOutboundHotOnly makes the running core hold raw under tag, or drops the
// tag when raw is nil. Both directions are safe to repeat: an existing tag is
// replaced, a missing one is not an error.
func (s *XrayService) ApplyOutboundHotOnly(tag string, raw json.RawMessage) error {
	lock.Lock()
	defer lock.Unlock()
	return s.withRunningAPI(func(api *xray.XrayAPI) error {
		if raw == nil {
			if err := api.DelOutbound(tag); err != nil && !xray.IsMissingHandlerErr(err) {
				return err
			}
			return nil
		}
		return addOutboundReconciling(api, raw)
	})
}

// AppendRoutingRulesHotOnly puts rules at the end of the running core's rule
// list, which is where the first-match router reads the most specific
// overrides. A rule tagged like one the core already holds replaces it.
//
// The catch-all rule that makes the first outbound the effective default is
// lifted out of the way first: it matches everything, so anything appended
// after it would never fire.
func (s *XrayService) AppendRoutingRulesHotOnly(rules []json.RawMessage) error {
	if len(rules) == 0 {
		return nil
	}
	lock.Lock()
	defer lock.Unlock()

	process := currentXrayProcess()
	if process == nil || !process.IsRunning() {
		return errors.New("xray is not running; refusing to report the routing change as applied")
	}
	catchAll := process.GetConfig().HotDefaultRule

	return s.withRunningAPI(func(api *xray.XrayAPI) error {
		for _, rule := range rules {
			if tag := ruleTagOf(rule); tag != "" {
				if err := api.RemoveRule(tag); err != nil && !xray.IsMissingHandlerErr(err) {
					return err
				}
			}
		}
		if len(catchAll) > 0 {
			if err := api.RemoveRule(hotDefaultRuleTag); err != nil && !xray.IsMissingHandlerErr(err) {
				return err
			}
		}
		failed, addErr := api.AppendRules(rules)
		if addErr != nil {
			for i := 0; i < failed && i < len(rules); i++ {
				_ = api.RemoveRule(ruleTagOf(rules[i]))
			}
			restoreHotDefaultRule(api, catchAll)
			return addErr
		}
		return restoreHotDefaultRule(api, catchAll)
	})
}

// RemoveRoutingRuleHotOnly drops one rule from the running core. A rule the
// core no longer has is the state the caller asked for, not a failure.
func (s *XrayService) RemoveRoutingRuleHotOnly(tag string) error {
	lock.Lock()
	defer lock.Unlock()
	return s.withRunningAPI(func(api *xray.XrayAPI) error {
		if err := api.RemoveRule(tag); err != nil && !xray.IsMissingHandlerErr(err) {
			return err
		}
		return nil
	})
}

// restoreHotDefaultRule puts the catch-all back last. Failing here leaves the
// core with no default route, so it is loud and schedules a restart rather
// than being folded into the caller's error.
func restoreHotDefaultRule(api *xray.XrayAPI, catchAll []byte) error {
	if len(catchAll) == 0 {
		return nil
	}
	if _, err := api.AppendRules([]json.RawMessage{catchAll}); err != nil {
		logger.Error("hot apply: the default route could not be restored, the core is blackholing unmatched traffic until it restarts:", err)
		isNeedXrayRestart.Store(true)
		return common.NewError("the default route could not be restored:", err)
	}
	return nil
}

// ApplyClientMutationHotOnly proves that the running core matches the database
// for the named client identities, one runtime user operation at a time.
//
// With no emails it reconciles the whole machine, which is what a batch write
// wants: one whole-config pass beats one pass per client.
func (s *XrayService) ApplyClientMutationHotOnly(emails ...string) error {
	if len(emails) == 0 {
		return s.ApplyDesiredConfigHotOnly()
	}
	// Same housekeeping GetXrayConfig runs, so an expired client is disabled
	// before its runtime state is decided rather than after.
	_, _, _ = s.inboundService.AddTraffic(nil, nil)

	ops, err := s.planClientUserOps(emails)
	if errors.Is(err, errNeedsFullReconcile) {
		return s.ApplyDesiredConfigHotOnly()
	}
	if err != nil {
		return err
	}
	if err := s.runClientUserOps(ops); err != nil {
		if errors.Is(err, errNeedsFullReconcile) {
			return s.ApplyDesiredConfigHotOnly()
		}
		return err
	}
	return nil
}

// clientUserOp is one AddUser or RemoveUser the core must acknowledge. User is
// nil for a removal.
type clientUserOp struct {
	tag      string
	protocol model.Protocol
	email    string
	user     map[string]any
}

// hotInbound is an inbound addressed by tag only — the columns a per-user
// operation needs, without the settings blob that holds every client on it.
type hotInbound struct {
	Id       int            `gorm:"column:id"`
	Tag      string         `gorm:"column:tag"`
	Protocol model.Protocol `gorm:"column:protocol"`
	Enable   bool           `gorm:"column:enable"`
	NodeID   *int           `gorm:"column:node_id"`
}

// clientCarryingProtocols are the inbound protocols that have clients at all;
// the others (tunnel, tun) can never hold one, so they are simply not part of
// a client's runtime state.
var clientCarryingProtocols = map[model.Protocol]bool{
	model.VMESS: true, model.VLESS: true, model.Trojan: true, model.Shadowsocks: true,
	model.WireGuard: true, model.Hysteria: true, model.HTTP: true, model.Mixed: true,
}

// planClientUserOps works out, for each identity, where the core must hold it
// and where it must not. It reads only rows keyed by this client, so its cost
// follows the number of inbounds on the machine, not the number of clients.
func (s *XrayService) planClientUserOps(emails []string) ([]clientUserOp, error) {
	db := database.GetDB()
	var rows []hotInbound
	if err := db.Model(&model.Inbound{}).
		Select("id", "tag", "protocol", "enable", "node_id").
		Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}

	local := make([]hotInbound, 0, len(rows))
	for _, ib := range rows {
		// A node's inbounds are not part of this core's config at all, and an
		// mtproto inbound is served by its own sidecar.
		if ib.NodeID != nil || !ib.Enable || ib.Protocol == model.MTProto {
			continue
		}
		if !clientCarryingProtocols[ib.Protocol] {
			continue
		}
		if !xray.UserDiffableProtocol(string(ib.Protocol)) {
			// wireguard peers, socks and http accounts only reload as a whole
			// handler, which drops that inbound's live connections.
			return nil, errNeedsFullReconcile
		}
		local = append(local, ib)
	}

	ops := make([]clientUserOp, 0, len(emails)*len(local))
	seen := make(map[string]bool, len(emails))
	for _, email := range emails {
		email = strings.TrimSpace(email)
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		emailOps, err := s.planOneClientUserOps(db, email, local)
		if err != nil {
			return nil, err
		}
		ops = append(ops, emailOps...)
	}
	return ops, nil
}

func (s *XrayService) planOneClientUserOps(db *gorm.DB, email string, local []hotInbound) ([]clientUserOp, error) {
	record, err := s.inboundService.clientService.GetRecordByEmail(nil, email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// A reverse-tagged client rebuilds the inbound's reverse plumbing, which
	// no per-user operation covers.
	if record != nil && strings.TrimSpace(record.Reverse) != "" {
		return nil, errNeedsFullReconcile
	}

	flowByInbound := map[int]string{}
	if record != nil {
		var links []model.ClientInbound
		if err := db.Where("client_id = ?", record.Id).Find(&links).Error; err != nil {
			return nil, err
		}
		for _, link := range links {
			flowByInbound[link.InboundId] = link.FlowOverride
		}
	}

	// One traffic row per email; only the inbound that owns it can disable the
	// client there, which is what GetXrayConfig's enable map says too.
	var stats []xray.ClientTraffic
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", email).Find(&stats).Error; err != nil {
		return nil, err
	}
	depletedOn := map[int]bool{}
	for i := range stats {
		if !stats[i].Enable {
			depletedOn[stats[i].InboundId] = true
		}
	}

	ops := make([]clientUserOp, 0, len(local))
	for i := range local {
		ib := local[i]
		flow, attached := flowByInbound[ib.Id]
		present := attached && record != nil && record.Enable && !depletedOn[ib.Id]
		op := clientUserOp{tag: ib.Tag, protocol: ib.Protocol, email: email}
		if present {
			user, err := s.hotUserMap(db, ib, record, flow)
			if err != nil {
				return nil, err
			}
			op.user = user
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// hotUserMap builds the runtime user the core needs for this protocol. It
// mirrors what GetXrayConfig emits into settings.clients for the same client.
func (s *XrayService) hotUserMap(db *gorm.DB, ib hotInbound, record *model.ClientRecord, flow string) (map[string]any, error) {
	client := record.ToClient()
	if flow == "xtls-rprx-vision-udp443" {
		flow = "xtls-rprx-vision"
	}
	user := map[string]any{"email": record.Email}
	// Limits ride on protocol.User, so one assignment covers every protocol
	// that has users.
	user["bandwidth_bps"] = client.BandwidthBps
	user["committed_bps"] = client.CommittedBps
	user["committed_burst_bytes"] = client.CommittedBurstBytes
	user["conn_limit"] = uint64(client.ConnLimit)
	if client.EgressTag != "" {
		user["egress_tag"] = client.EgressTag
	}

	switch ib.Protocol {
	case model.VLESS:
		user["id"] = client.ID
		user["flow"] = flow
	case model.VMESS:
		user["id"] = client.ID
	case model.Trojan:
		user["password"] = client.Password
	case model.Hysteria:
		user["auth"] = client.Auth
	case model.Shadowsocks:
		user["password"] = client.Password
		method, err := shadowsocksMethodForInbound(db, ib.Id)
		if err != nil {
			return nil, err
		}
		user["cipher"] = method
	case model.Mixed:
		// Xray's mixed inbound uses the SOCKS account type at runtime. The
		// route/traffic identity is still email, while authentication needs the
		// same username plus the panel's unified client password.
		user["user"] = record.Email
		user["pass"] = client.Password
	default:
		return nil, errNeedsFullReconcile
	}
	return user, nil
}

// shadowsocksMethodForInbound reads the inbound-level cipher, which the core
// needs to build the right account type for a live user operation.
func shadowsocksMethodForInbound(db *gorm.DB, inboundId int) (string, error) {
	var settings []string
	if err := db.Model(&model.Inbound{}).Where("id = ?", inboundId).
		Pluck("settings", &settings).Error; err != nil {
		return "", err
	}
	if len(settings) == 0 {
		return "", errNeedsFullReconcile
	}
	return shadowsocksMethodFromSettings(settings[0]), nil
}

// runClientUserOps sends every operation and fails on the first one the core
// does not acknowledge, so "applied" is never reported for a half-applied
// client. A handler the core does not have means the machine drifted past this
// one client, so the whole config is reconciled instead.
//
// It talks to the local core directly, like tryHotApply does and for the same
// reason: this is the reconciler for this panel's own process. Inbounds that
// belong to a node are excluded above and keep their own dispatch.
func (s *XrayService) runClientUserOps(ops []clientUserOp) error {
	if len(ops) == 0 {
		return nil
	}
	lock.Lock()
	defer lock.Unlock()

	return s.withRunningAPI(func(api *xray.XrayAPI) error {
		for i := range ops {
			op := &ops[i]
			if op.user == nil {
				if err := api.RemoveUser(op.tag, op.email); err != nil && !xray.IsMissingHandlerErr(err) {
					return err
				}
				continue
			}
			if err := addUserReconciling(api, xray.UserOp{
				Tag: op.tag, Protocol: string(op.protocol), Email: op.email, User: op.user,
			}); err != nil {
				if xray.IsMissingHandlerErr(err) {
					return errNeedsFullReconcile
				}
				return err
			}
		}
		return nil
	})
}
