package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func deltaBaseConfig() DeclarativeNodeConfig {
	password := "line-secret"
	config := DeclarativeNodeConfig{
		NodeBandwidthBps: 1_000_000_000,
		Inbounds: []DeclarativeInbound{{
			Tag:            "entry-vless",
			Protocol:       "vless",
			ListenPort:     30800,
			ShareAddr:      DeclarativeShareAddress{Strategy: "custom", Host: "entry.example.com", Port: 443},
			Settings:       map[string]any{},
			StreamSettings: map[string]any{"network": "tcp"},
			Clients: []DeclarativeClient{{
				Email:  "line-001@line.invalid",
				UUID:   "11111111-1111-1111-1111-111111111111",
				PirBps: 100_000_000,
			}},
		}, {
			Tag:            "entry-trojan",
			Protocol:       "trojan",
			ListenPort:     30801,
			ShareAddr:      DeclarativeShareAddress{Strategy: "custom", Host: "entry.example.com", Port: 8443},
			Settings:       map[string]any{},
			StreamSettings: map[string]any{"network": "tcp"},
			Clients: []DeclarativeClient{{
				Email:    "line-001@line.invalid",
				UUID:     "11111111-1111-1111-1111-111111111111",
				Password: &password,
				PirBps:   100_000_000,
			}},
		}},
		Outbounds: []DeclarativeOutbound{{
			Tag:      "egress-hk-1",
			Protocol: "socks",
			Server:   DeclarativeSocksServer{Host: "10.0.0.1", Port: 1080},
		}},
	}
	config.Routing.Rules = []DeclarativeRule{{
		AccountEmail: "line-001@line.invalid",
		OutboundTag:  "egress-hk-1",
	}}
	return config
}

// Folding is the whole feature: whatever the ops describe, the result must be
// the complete desired state the full apply would have received.
func TestFoldDeltaProducesTheSameStateAsAFullApply(t *testing.T) {
	base := deltaBaseConfig()
	password := "second-secret"
	newClientVless := DeclarativeClient{
		Email:  "line-002@line.invalid",
		UUID:   "22222222-2222-2222-2222-222222222222",
		PirBps: 50_000_000,
	}
	newClientTrojan := newClientVless
	newClientTrojan.Password = &password

	folded, err := foldDeclarativeDelta(base, []DeclarativeDeltaOp{
		{Op: DeltaOpAddClient, InboundTag: "entry-vless", Client: &newClientVless},
		{Op: DeltaOpAddClient, InboundTag: "entry-trojan", Client: &newClientTrojan},
		{Op: DeltaOpSetOutbound, Outbound: &DeclarativeOutbound{
			Tag: "egress-hk-2", Protocol: "socks",
			Server: DeclarativeSocksServer{Host: "10.0.0.2", Port: 1080},
		}},
		{Op: DeltaOpSetRule, Rule: &DeclarativeRule{AccountEmail: "line-002@line.invalid", OutboundTag: "egress-hk-2"}},
	})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}

	// The same state, authored directly as a full config.
	expected := deltaBaseConfig()
	expected.Inbounds[0].Clients = append(expected.Inbounds[0].Clients, newClientVless)
	expected.Inbounds[1].Clients = append(expected.Inbounds[1].Clients, newClientTrojan)
	expected.Outbounds = append(expected.Outbounds, DeclarativeOutbound{
		Tag: "egress-hk-2", Protocol: "socks",
		Server: DeclarativeSocksServer{Host: "10.0.0.2", Port: 1080},
	})
	expected.Routing.Rules = append(expected.Routing.Rules, DeclarativeRule{
		AccountEmail: "line-002@line.invalid", OutboundTag: "egress-hk-2",
	})

	foldedHash, err := hashDeclarativeConfig(folded)
	if err != nil {
		t.Fatal(err)
	}
	expectedHash, err := hashDeclarativeConfig(expected)
	if err != nil {
		t.Fatal(err)
	}
	if foldedHash != expectedHash {
		gotJSON, _ := json.Marshal(folded)
		wantJSON, _ := json.Marshal(expected)
		t.Fatalf("a delta must land on the same state as the equivalent full apply\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}

	// And that state has to be a config the full apply would accept.
	if err := validateDeclarativeRequest(&DeclarativeApplyRequest{Revision: 2, Config: folded}); err != nil {
		t.Fatalf("the folded state must pass the ordinary validation: %v", err)
	}
}

// Folding must not mutate the state that is still applied — the caller reads it
// from persisted settings and a failed delta has to leave it untouched.
func TestFoldDeltaLeavesTheAppliedConfigAlone(t *testing.T) {
	base := deltaBaseConfig()
	before, err := hashDeclarativeConfig(base)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := foldDeclarativeDelta(base, []DeclarativeDeltaOp{
		{Op: DeltaOpRemoveClient, InboundTag: "entry-vless", Email: "line-001@line.invalid"},
	}); err != nil {
		t.Fatalf("fold: %v", err)
	}

	after, err := hashDeclarativeConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("folding mutated the configuration it was folding from")
	}
}

func TestFoldDeltaOperations(t *testing.T) {
	replacement := DeclarativeClient{
		Email:  "line-001@line.invalid",
		UUID:   "11111111-1111-1111-1111-111111111111",
		PirBps: 200_000_000,
		CirBps: 20_000_000,
	}

	t.Run("removeClient drops exactly one account from one inbound", func(t *testing.T) {
		got, err := foldDeclarativeDelta(deltaBaseConfig(), []DeclarativeDeltaOp{
			{Op: DeltaOpRemoveClient, InboundTag: "entry-vless", Email: "line-001@line.invalid"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Inbounds[0].Clients) != 0 {
			t.Fatalf("client not removed: %#v", got.Inbounds[0].Clients)
		}
		if len(got.Inbounds[1].Clients) != 1 {
			t.Fatal("the other inbound must be untouched")
		}
	})

	t.Run("updateClient replaces by email", func(t *testing.T) {
		got, err := foldDeclarativeDelta(deltaBaseConfig(), []DeclarativeDeltaOp{
			{Op: DeltaOpUpdateClient, InboundTag: "entry-vless", Client: &replacement},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Inbounds[0].Clients) != 1 || got.Inbounds[0].Clients[0].PirBps != 200_000_000 {
			t.Fatalf("client not updated in place: %#v", got.Inbounds[0].Clients)
		}
	})

	t.Run("setRule repoints an account without duplicating the rule", func(t *testing.T) {
		got, err := foldDeclarativeDelta(deltaBaseConfig(), []DeclarativeDeltaOp{
			{Op: DeltaOpSetOutbound, Outbound: &DeclarativeOutbound{
				Tag: "egress-hk-2", Protocol: "socks",
				Server: DeclarativeSocksServer{Host: "10.0.0.2", Port: 1080},
			}},
			{Op: DeltaOpSetRule, Rule: &DeclarativeRule{AccountEmail: "line-001@line.invalid", OutboundTag: "egress-hk-2"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Routing.Rules) != 1 || got.Routing.Rules[0].OutboundTag != "egress-hk-2" {
			t.Fatalf("rule not repointed: %#v", got.Routing.Rules)
		}
	})

	t.Run("setRule with an empty outbound removes the override", func(t *testing.T) {
		got, err := foldDeclarativeDelta(deltaBaseConfig(), []DeclarativeDeltaOp{
			{Op: DeltaOpSetRule, Rule: &DeclarativeRule{AccountEmail: "line-001@line.invalid"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Routing.Rules) != 0 {
			t.Fatalf("rule not removed: %#v", got.Routing.Rules)
		}
	})

	t.Run("setOutbound replaces an egress in place", func(t *testing.T) {
		got, err := foldDeclarativeDelta(deltaBaseConfig(), []DeclarativeDeltaOp{
			{Op: DeltaOpSetOutbound, Outbound: &DeclarativeOutbound{
				Tag: "egress-hk-1", Protocol: "socks",
				Server: DeclarativeSocksServer{Host: "10.9.9.9", Port: 1080},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Outbounds) != 1 || got.Outbounds[0].Server.Host != "10.9.9.9" {
			t.Fatalf("outbound not replaced: %#v", got.Outbounds)
		}
	})
}

// A delta whose ops don't fit the applied state is a sign the two sides
// disagree, and guessing would apply something neither of them described.
func TestFoldDeltaRefusesOpsThatDoNotFit(t *testing.T) {
	cases := []struct {
		name string
		op   DeclarativeDeltaOp
	}{
		{"unknown inbound", DeclarativeDeltaOp{Op: DeltaOpRemoveClient, InboundTag: "nope", Email: "line-001@line.invalid"}},
		{"missing inbound tag", DeclarativeDeltaOp{Op: DeltaOpRemoveClient, Email: "line-001@line.invalid"}},
		{"removing an absent client", DeclarativeDeltaOp{Op: DeltaOpRemoveClient, InboundTag: "entry-vless", Email: "ghost@line.invalid"}},
		{"updating an absent client", DeclarativeDeltaOp{Op: DeltaOpUpdateClient, InboundTag: "entry-vless", Client: &DeclarativeClient{Email: "ghost@line.invalid"}}},
		{"adding a client twice", DeclarativeDeltaOp{Op: DeltaOpAddClient, InboundTag: "entry-vless", Client: &DeclarativeClient{Email: "line-001@line.invalid"}}},
		{"rule without an account", DeclarativeDeltaOp{Op: DeltaOpSetRule, Rule: &DeclarativeRule{OutboundTag: "egress-hk-1"}}},
		{"outbound without a tag", DeclarativeDeltaOp{Op: DeltaOpSetOutbound, Outbound: &DeclarativeOutbound{Protocol: "socks"}}},
		{"an operation that does not exist", DeclarativeDeltaOp{Op: "rewriteEverything"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := foldDeclarativeDelta(deltaBaseConfig(), []DeclarativeDeltaOp{c.op}); err == nil {
				t.Fatal("an op that does not fit the applied state must be refused")
			}
		})
	}
}

// seedDeclarativeState persists an applied configuration and returns its hash.
func seedDeclarativeState(t *testing.T, config DeclarativeNodeConfig, revision int) string {
	t.Helper()
	hash, err := hashDeclarativeConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(persistedDeclarativeState{
		Request: DeclarativeApplyRequest{Revision: revision, Config: config},
		Hash:    hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&SettingService{}).saveSetting(declarativeProvisioningStateKey, string(encoded)); err != nil {
		t.Fatal(err)
	}
	return hash
}

// A delta computed against a configuration the node is no longer running must
// be refused with the node's current identity, so the control plane can either
// recompute or fall back to a full apply.
func TestApplyDeltaRefusesAStaleBaseHash(t *testing.T) {
	setupConflictDB(t)
	hash := seedDeclarativeState(t, deltaBaseConfig(), 7)

	svc := &DeclarativeProvisioningService{}
	_, err := svc.ApplyDelta(&DeclarativeDeltaRequest{
		BaseHash:   "0000000000000000000000000000000000000000000000000000000000000000",
		Ops:        []DeclarativeDeltaOp{{Op: DeltaOpRemoveClient, InboundTag: "entry-vless", Email: "line-001@line.invalid"}},
		ResultHash: "whatever",
	})

	var mismatch *DeclarativeDeltaBaseMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("a stale baseHash must be reported as a base mismatch, got %v", err)
	}
	if mismatch.CurrentHash != hash || mismatch.CurrentRevision != 7 {
		t.Fatalf("the mismatch must carry the node's current identity; got %s / %d", mismatch.CurrentHash, mismatch.CurrentRevision)
	}
}

// Nothing has been applied yet, so there is no base to build on.
func TestApplyDeltaRefusesWhenNothingHasBeenApplied(t *testing.T) {
	setupConflictDB(t)

	svc := &DeclarativeProvisioningService{}
	_, err := svc.ApplyDelta(&DeclarativeDeltaRequest{
		BaseHash:   "abc",
		Ops:        []DeclarativeDeltaOp{{Op: DeltaOpRemoveClient, InboundTag: "entry-vless", Email: "x"}},
		ResultHash: "def",
	})

	var mismatch *DeclarativeDeltaBaseMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("a delta against a virgin node must be a base mismatch, got %v", err)
	}
	if mismatch.CurrentHash != "" {
		t.Fatalf("there is no current configuration to report; got %q", mismatch.CurrentHash)
	}
}

// resultHash is the end-to-end agreement check. When the fold does not land
// where the control plane expected, the delta must be refused before anything
// is written — the two sides no longer describe the same node.
func TestApplyDeltaRefusesAFoldThatLandsSomewhereElse(t *testing.T) {
	setupConflictDB(t)
	base := deltaBaseConfig()
	hash := seedDeclarativeState(t, base, 7)

	svc := &DeclarativeProvisioningService{}
	_, err := svc.ApplyDelta(&DeclarativeDeltaRequest{
		BaseHash:   hash,
		Ops:        []DeclarativeDeltaOp{{Op: DeltaOpRemoveClient, InboundTag: "entry-vless", Email: "line-001@line.invalid"}},
		ResultHash: "1111111111111111111111111111111111111111111111111111111111111111",
	})
	if err == nil {
		t.Fatal("a fold that does not match resultHash must be refused")
	}
	if !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("the refusal should say the two sides disagree; got %q", err.Error())
	}

	// And the applied state must be exactly where it was.
	state, loadErr := svc.loadState()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Hash != hash || state.Request.Revision != 7 {
		t.Fatalf("a refused delta must not move the applied state; now %s / %d", state.Hash, state.Request.Revision)
	}
}

func TestApplyDeltaRejectsIncompleteRequests(t *testing.T) {
	setupConflictDB(t)
	seedDeclarativeState(t, deltaBaseConfig(), 7)

	svc := &DeclarativeProvisioningService{}
	cases := map[string]*DeclarativeDeltaRequest{
		"no baseHash":   {ResultHash: "x", Ops: []DeclarativeDeltaOp{{Op: DeltaOpSetRule}}},
		"no resultHash": {BaseHash: "x", Ops: []DeclarativeDeltaOp{{Op: DeltaOpSetRule}}},
		"no ops":        {BaseHash: "x", ResultHash: "y"},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.ApplyDelta(request); err == nil {
				t.Fatal("an incomplete delta request must be refused")
			}
		})
	}
}
