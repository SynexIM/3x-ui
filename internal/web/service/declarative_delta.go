package service

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The delta operations the control plane may send. They cover what changes
// often — who is on the line and where their traffic leaves — and nothing else.
// Anything outside this set (adding an inbound, changing a listen port, moving
// node bandwidth) goes through the full Apply, which stays the reconciliation
// and repair path.
//
// There is no addOutbound: setOutbound upserts by tag, so adding an egress is
// already one op. removeOutbound is the half that was missing, and its absence
// was expensive. configHash covers outbounds, so releasing an expired line left
// an orphan egress behind, the folded hash no longer matched what the control
// plane computed, and the whole delta was refused — pushing an everyday event
// onto the full-apply path, which on a node holding 50k lines means resending
// 250k clients.
const (
	DeltaOpAddClient      = "addClient"
	DeltaOpRemoveClient   = "removeClient"
	DeltaOpUpdateClient   = "updateClient"
	DeltaOpSetRule        = "setRule"
	DeltaOpSetOutbound    = "setOutbound"
	DeltaOpRemoveOutbound = "removeOutbound"
)

// DeclarativeDeltaOp is one edit against the applied configuration.
type DeclarativeDeltaOp struct {
	Op string `json:"op"`
	// InboundTag names the inbound a client operation applies to. One account
	// holds a client in every inbound of the line (one identity, N inbounds),
	// so adding an account is one op per inbound.
	InboundTag string `json:"inboundTag"`
	// Email identifies the client for removeClient.
	Email string `json:"email"`
	// Client carries the full client for addClient and updateClient.
	Client *DeclarativeClient `json:"client"`
	// Rule upserts a routing rule keyed by its account email. An empty
	// outboundTag removes the rule, putting the account back on the default
	// egress.
	Rule *DeclarativeRule `json:"rule"`
	// Outbound upserts an egress keyed by its tag.
	Outbound *DeclarativeOutbound `json:"outbound"`
	// OutboundTag names the egress removeOutbound drops.
	OutboundTag string `json:"outboundTag"`
}

// DeclarativeDeltaRequest is the incremental form of DeclarativeApplyRequest.
//
// It exists purely to keep the wire small: a full apply repeats every client
// inside every inbound, so a line with five protocols carries each account five
// times and a busy node eventually meets the request body limit. The panel was
// already doing the diffing internally — tryHotApply adds and removes users
// over the core's gRPC API without restarting — so only the upload was ever
// full.
//
// It is a transport optimisation and not a second code path: once the ops are
// folded into a complete desired state, validation, hashing, template building
// and hot reload are the existing Apply, unchanged.
type DeclarativeDeltaRequest struct {
	// BaseHash is the configuration this delta was computed against. A
	// mismatch means the two sides disagree about the starting point, and the
	// delta is refused rather than guessed at.
	BaseHash string               `json:"baseHash"`
	Ops      []DeclarativeDeltaOp `json:"ops"`
	// ResultHash is what the control plane expects the folded state to hash to.
	// It is the end-to-end check that both sides built the same world; without
	// it a subtly different fold would be applied and silently disagree with
	// what the control plane believes is on the node.
	ResultHash string `json:"resultHash"`
}

// DeclarativeDeltaBaseMismatchError says the delta was computed against a
// configuration this node is no longer running. It carries the current identity
// so the control plane can either recompute or fall back to a full apply
// without a second round trip.
type DeclarativeDeltaBaseMismatchError struct {
	CurrentHash     string
	CurrentRevision int
}

func (e *DeclarativeDeltaBaseMismatchError) Error() string {
	if e.CurrentHash == "" {
		return "no declarative configuration has been applied to this node yet; send a full apply"
	}
	return fmt.Sprintf("delta was computed against a different configuration; this node is on revision %d (%s)", e.CurrentRevision, e.CurrentHash)
}

// ApplyDelta folds ops onto the applied configuration and hands the result to
// the ordinary apply path.
func (s *DeclarativeProvisioningService) ApplyDelta(request *DeclarativeDeltaRequest) (*DeclarativeApplyReceipt, error) {
	declarativeProvisioningLock.Lock()
	defer declarativeProvisioningLock.Unlock()

	if request == nil {
		return nil, errors.New("delta request is empty")
	}
	if request.BaseHash == "" {
		return nil, errors.New("baseHash is required")
	}
	if request.ResultHash == "" {
		return nil, errors.New("resultHash is required")
	}
	if len(request.Ops) == 0 {
		return nil, errors.New("a delta with no operations changes nothing")
	}

	current, err := s.loadState()
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, &DeclarativeDeltaBaseMismatchError{}
	}
	if current.Hash != request.BaseHash {
		return nil, &DeclarativeDeltaBaseMismatchError{
			CurrentHash:     current.Hash,
			CurrentRevision: current.Request.Revision,
		}
	}

	desired, err := foldDeclarativeDelta(current.Request.Config, request.Ops)
	if err != nil {
		return nil, err
	}
	hash, err := hashDeclarativeConfig(desired)
	if err != nil {
		return nil, err
	}
	if hash != request.ResultHash {
		return nil, fmt.Errorf("folded configuration hashes to %s but %s was expected; the two sides disagree about this node's state", hash, request.ResultHash)
	}

	// A delta only ever touches clients, routing and outbounds — exactly what
	// the core applies over its API — so it never asks for a restart. When a
	// fold turns out not to be hot-appliable, the apply path falls back to a
	// restart on its own.
	return s.apply(&DeclarativeApplyRequest{
		Revision: current.Request.Revision + 1,
		Config:   desired,
	})
}

// foldDeclarativeDelta applies ops to a copy of base and returns the complete
// desired state.
//
// The copy is made by round-tripping through JSON: it detaches the result from
// the persisted state (the inbound settings maps are shared otherwise) and it
// guarantees the folded value is in the same representation the hash is taken
// over.
func foldDeclarativeDelta(base DeclarativeNodeConfig, ops []DeclarativeDeltaOp) (DeclarativeNodeConfig, error) {
	encoded, err := json.Marshal(base)
	if err != nil {
		return DeclarativeNodeConfig{}, err
	}
	var config DeclarativeNodeConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		return DeclarativeNodeConfig{}, err
	}

	for index, op := range ops {
		if err := applyDeltaOp(&config, op); err != nil {
			return DeclarativeNodeConfig{}, fmt.Errorf("op %d (%s): %w", index, op.Op, err)
		}
	}
	return config, nil
}

func applyDeltaOp(config *DeclarativeNodeConfig, op DeclarativeDeltaOp) error {
	switch op.Op {
	case DeltaOpAddClient, DeltaOpUpdateClient:
		if op.Client == nil {
			return errors.New("client is required")
		}
		inbound, err := inboundByTag(config, op.InboundTag)
		if err != nil {
			return err
		}
		at := clientIndexByEmail(inbound.Clients, op.Client.Email)
		if op.Op == DeltaOpAddClient {
			if at >= 0 {
				return fmt.Errorf("client %q is already on inbound %q", op.Client.Email, op.InboundTag)
			}
			inbound.Clients = append(inbound.Clients, *op.Client)
			return nil
		}
		if at < 0 {
			return fmt.Errorf("client %q is not on inbound %q", op.Client.Email, op.InboundTag)
		}
		inbound.Clients[at] = *op.Client
		return nil

	case DeltaOpRemoveClient:
		if op.Email == "" {
			return errors.New("email is required")
		}
		inbound, err := inboundByTag(config, op.InboundTag)
		if err != nil {
			return err
		}
		at := clientIndexByEmail(inbound.Clients, op.Email)
		if at < 0 {
			return fmt.Errorf("client %q is not on inbound %q", op.Email, op.InboundTag)
		}
		inbound.Clients = append(inbound.Clients[:at], inbound.Clients[at+1:]...)
		return nil

	case DeltaOpSetRule:
		if op.Rule == nil || op.Rule.AccountEmail == "" {
			return errors.New("rule with an accountEmail is required")
		}
		rules := config.Routing.Rules
		at := -1
		for i := range rules {
			if rules[i].AccountEmail == op.Rule.AccountEmail {
				at = i
				break
			}
		}
		if op.Rule.OutboundTag == "" {
			if at >= 0 {
				config.Routing.Rules = append(rules[:at], rules[at+1:]...)
			}
			return nil
		}
		if at >= 0 {
			rules[at] = *op.Rule
			return nil
		}
		config.Routing.Rules = append(rules, *op.Rule)
		return nil

	case DeltaOpSetOutbound:
		if op.Outbound == nil || op.Outbound.Tag == "" {
			return errors.New("outbound with a tag is required")
		}
		for i := range config.Outbounds {
			if config.Outbounds[i].Tag == op.Outbound.Tag {
				config.Outbounds[i] = *op.Outbound
				return nil
			}
		}
		config.Outbounds = append(config.Outbounds, *op.Outbound)
		return nil

	case DeltaOpRemoveOutbound:
		if op.OutboundTag == "" {
			return errors.New("outboundTag is required")
		}
		at := -1
		for i := range config.Outbounds {
			if config.Outbounds[i].Tag == op.OutboundTag {
				at = i
				break
			}
		}
		if at < 0 {
			return fmt.Errorf("outbound %q is not part of the applied configuration", op.OutboundTag)
		}
		// An outbound a rule still points at is not removable: the folded state
		// would fail validation ("routing accounts must target a declared
		// outbound") several steps later, reported as a hash the control plane
		// cannot explain. Say it here, naming the account still on it.
		for _, rule := range config.Routing.Rules {
			if rule.OutboundTag == op.OutboundTag {
				return fmt.Errorf("outbound %q still carries account %q; drop the rule first", op.OutboundTag, rule.AccountEmail)
			}
		}
		config.Outbounds = append(config.Outbounds[:at], config.Outbounds[at+1:]...)
		return nil
	}
	return fmt.Errorf("unknown operation %q", op.Op)
}

func inboundByTag(config *DeclarativeNodeConfig, tag string) (*DeclarativeInbound, error) {
	if tag == "" {
		return nil, errors.New("inboundTag is required")
	}
	for i := range config.Inbounds {
		if config.Inbounds[i].Tag == tag {
			return &config.Inbounds[i], nil
		}
	}
	return nil, fmt.Errorf("inbound %q is not part of the applied configuration", tag)
}

func clientIndexByEmail(clients []DeclarativeClient, email string) int {
	for i := range clients {
		if clients[i].Email == email {
			return i
		}
	}
	return -1
}
