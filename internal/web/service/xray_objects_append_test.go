package service

import (
	"encoding/json"
	"fmt"
	"testing"
)

// The splice path must be indistinguishable from the decoding one, and must
// decline rather than guess whenever it cannot be sure.

func templateRulesOf(t *testing.T, cfg map[string]json.RawMessage) []json.RawMessage {
	t.Helper()
	got, err := templateRules(cfg)
	if err != nil {
		t.Fatalf("templateRules: %v", err)
	}
	return got
}

func cfgWithRules(body string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"routing": json.RawMessage(`{"domainStrategy":"AsIs","rules":` + body + `}`),
	}
}

func TestAppendTemplateRulesOntoEmptyArray(t *testing.T) {
	cfg := cfgWithRules(`[]`)
	rule := json.RawMessage(`{"type":"field","ruleTag":"a","outboundTag":"direct"}`)
	done, err := appendTemplateRules(cfg, []json.RawMessage{rule}, []string{"a"})
	if err != nil || !done {
		t.Fatalf("expected the fast path to take it: done=%v err=%v", done, err)
	}
	got := templateRulesOf(t, cfg)
	if len(got) != 1 || ruleTagOf(got[0]) != "a" {
		t.Fatalf("got %d rules, first tag %q", len(got), ruleTagOf(got[0]))
	}
	// The sibling key must survive: we rewrite routing, not replace it.
	var routing map[string]any
	if err := json.Unmarshal(cfg["routing"], &routing); err != nil {
		t.Fatal(err)
	}
	if routing["domainStrategy"] != "AsIs" {
		t.Fatalf("domainStrategy was lost: %v", routing["domainStrategy"])
	}
}

func TestAppendTemplateRulesKeepsOrderAndCount(t *testing.T) {
	existing := make([]json.RawMessage, 3)
	for i := range existing {
		existing[i] = json.RawMessage(fmt.Sprintf(`{"type":"field","ruleTag":"old-%d"}`, i))
	}
	encoded, _ := json.Marshal(existing)
	cfg := cfgWithRules(string(encoded))

	added := []json.RawMessage{
		json.RawMessage(`{"type":"field","ruleTag":"new-1"}`),
		json.RawMessage(`{"type":"field","ruleTag":"new-2"}`),
	}
	done, err := appendTemplateRules(cfg, added, []string{"new-1", "new-2"})
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	got := templateRulesOf(t, cfg)
	want := []string{"old-0", "old-1", "old-2", "new-1", "new-2"}
	if len(got) != len(want) {
		t.Fatalf("got %d rules, want %d", len(got), len(want))
	}
	for i, tag := range want {
		if ruleTagOf(got[i]) != tag {
			t.Fatalf("rule %d is %q, want %q — appends go at the end, order is what first-match means", i, ruleTagOf(got[i]), tag)
		}
	}
}

func TestAppendTemplateRulesDeclinesOnPossibleCollision(t *testing.T) {
	cfg := cfgWithRules(`[{"type":"field","ruleTag":"taken"}]`)
	done, err := appendTemplateRules(cfg, []json.RawMessage{
		json.RawMessage(`{"type":"field","ruleTag":"taken"}`),
	}, []string{"taken"})
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("the fast path must decline when the tag's bytes are present; only the slow path can tell a real collision from a coincidence")
	}
}

func TestAppendTemplateRulesDeclinesOnCoincidentalBytes(t *testing.T) {
	// "vip" is not a ruleTag here — it is inside an email. The fast path cannot
	// tell the difference, so it must decline and let the exact walk allow it.
	cfg := cfgWithRules(`[{"type":"field","ruleTag":"other","user":["vip@example.com"]}]`)
	done, err := appendTemplateRules(cfg, []json.RawMessage{
		json.RawMessage(`{"type":"field","ruleTag":"vip"}`),
	}, []string{"vip"})
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("a coincidental byte match must fall through to the exact check, not be treated as a collision or as a free pass")
	}
}

func TestAppendTemplateRulesDeclinesWithoutAnArray(t *testing.T) {
	for name, cfg := range map[string]map[string]json.RawMessage{
		"no routing section": {},
		"routing without rules": {
			"routing": json.RawMessage(`{"domainStrategy":"AsIs"}`),
		},
		"rules is not an array": {
			"routing": json.RawMessage(`{"rules":{"oops":true}}`),
		},
	} {
		done, err := appendTemplateRules(cfg, []json.RawMessage{
			json.RawMessage(`{"type":"field","ruleTag":"a"}`),
		}, []string{"a"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if done {
			t.Fatalf("%s: the fast path must decline shapes it does not recognise instead of guessing", name)
		}
	}
}
