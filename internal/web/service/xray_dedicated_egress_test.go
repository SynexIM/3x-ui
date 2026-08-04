package service

import (
	"encoding/json"
	"testing"
)

func TestUpsertDedicatedEgressConfigIsIdempotentAndRoutesOneUser(t *testing.T) {
	initial := `{"outbounds":[{"tag":"direct","protocol":"freedom"}],"routing":{"rules":[{"type":"field","outboundTag":"direct"}]}}`
	spec := DedicatedEgressSpec{
		Tag:        "dedicated-order-1",
		InboundTag: "sv-1",
		User:       "order-1@dedicated.local",
		Address:    "198.51.100.10",
		Port:       1080,
		Username:   "user",
		Password:   "password",
	}
	first, err := upsertDedicatedEgressConfig(initial, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := upsertDedicatedEgressConfig(first, spec)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(second), &config); err != nil {
		t.Fatal(err)
	}
	outbounds := config["outbounds"].([]interface{})
	if len(outbounds) != 2 {
		t.Fatalf("outbound count = %d, want 2", len(outbounds))
	}
	routing := config["routing"].(map[string]interface{})
	rules := routing["rules"].([]interface{})
	if len(rules) != 2 {
		t.Fatalf("route count = %d, want 2", len(rules))
	}
	if err := validateDedicatedEgressSpec(spec); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveDedicatedEgressConfigLeavesUnrelatedRoutes(t *testing.T) {
	initial := `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"dedicated-order-1","protocol":"socks"}],"routing":{"rules":[{"type":"field","outboundTag":"dedicated-order-1"},{"type":"field","outboundTag":"direct"}]}}`
	result, err := removeDedicatedEgressConfig(initial, "dedicated-order-1")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(result), &config); err != nil {
		t.Fatal(err)
	}
	outbounds := config["outbounds"].([]interface{})
	rules := config["routing"].(map[string]interface{})["rules"].([]interface{})
	if len(outbounds) != 1 || len(rules) != 1 {
		t.Fatalf("unexpected remaining config: %s", result)
	}
}

func TestDedicatedEgressPresenceRequiresBothOutboundAndRoute(t *testing.T) {
	complete := `{"outbounds":[{"tag":"dedicated-order-1","protocol":"socks"}],"routing":{"rules":[{"type":"field","outboundTag":"dedicated-order-1"}]}}`
	outboundOnly := `{"outbounds":[{"tag":"dedicated-order-1","protocol":"socks"}],"routing":{"rules":[]}}`

	outboundPresent, routePresent, err := dedicatedEgressPresence(complete, "dedicated-order-1")
	if err != nil || !outboundPresent || !routePresent {
		t.Fatalf("complete presence = (%v, %v, %v), want (true, true, nil)", outboundPresent, routePresent, err)
	}
	outboundPresent, routePresent, err = dedicatedEgressPresence(outboundOnly, "dedicated-order-1")
	if err != nil || !outboundPresent || routePresent {
		t.Fatalf("partial presence = (%v, %v, %v), want (true, false, nil)", outboundPresent, routePresent, err)
	}
}
