package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

func initObjectDB(t *testing.T) *XrayObjectService {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	template := `{
		"inbounds": [],
		"outbounds": [{"tag":"direct","protocol":"freedom","settings":{}}],
		"routing": {"domainStrategy":"AsIs","rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"}]}
	}`
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", template); err != nil {
		t.Fatal(err)
	}
	return &XrayObjectService{}
}

func storedOutboundTags(t *testing.T, service *XrayObjectService) []string {
	t.Helper()
	view, err := service.ListOutbounds()
	if err != nil {
		t.Fatal(err)
	}
	tags := make([]string, 0, len(view.Outbounds))
	for _, raw := range view.Outbounds {
		tags = append(tags, tagOf(raw))
	}
	return tags
}

func storedRuleTags(t *testing.T, service *XrayObjectService) []string {
	t.Helper()
	view, err := service.ListRoutingRules()
	if err != nil {
		t.Fatal(err)
	}
	tags := make([]string, 0, len(view.Rules))
	for _, raw := range view.Rules {
		tags = append(tags, ruleTagOf(raw))
	}
	return tags
}

// The template is the authority, so a write has to be there afterwards — with
// a stopped core there is nowhere else the object could have gone.
func TestOutboundWritesLandInTheStoredTemplate(t *testing.T) {
	service := initObjectDB(t)

	result, err := service.AddOutbound(json.RawMessage(`{"tag":"proxy-jp","protocol":"freedom","settings":{}}`))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if result.XrayRunning || result.HotApplied {
		t.Fatalf("with no core running nothing can be hot-applied: %+v", result)
	}
	if got := storedOutboundTags(t, service); len(got) != 2 || got[1] != "proxy-jp" {
		t.Fatalf("outbounds are %v, want the new one appended after direct", got)
	}

	if _, err := service.UpdateOutbound("proxy-jp", json.RawMessage(`{"tag":"proxy-jp","protocol":"blackhole","settings":{}}`)); err != nil {
		t.Fatalf("update: %v", err)
	}
	view, err := service.ListOutbounds()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(view.Outbounds[1]), "blackhole") {
		t.Fatalf("the update did not replace the object: %s", view.Outbounds[1])
	}

	if _, err := service.DeleteOutbound("proxy-jp"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := storedOutboundTags(t, service); len(got) != 1 || got[0] != "direct" {
		t.Fatalf("outbounds are %v, want only direct left", got)
	}
}

// Appending, never inserting: xray matches routes first-hit, so an override
// silently placed before an existing rule would change what the earlier rule
// does without anyone editing it.
func TestRoutingRulesAreAppendedInOrder(t *testing.T) {
	service := initObjectDB(t)

	for _, tag := range []string{"first", "second", "third"} {
		rule := json.RawMessage(`{"type":"field","ruleTag":"` + tag + `","user":["` + tag + `@example.com"],"outboundTag":"direct"}`)
		if _, err := service.AddRoutingRules([]json.RawMessage{rule}); err != nil {
			t.Fatalf("add %s: %v", tag, err)
		}
	}
	got := storedRuleTags(t, service)
	want := []string{"", "first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("rules are %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rules are %v, want %v", got, want)
		}
	}

	if _, err := service.DeleteRoutingRule("second"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := storedRuleTags(t, service); len(got) != 3 || got[1] != "first" || got[2] != "third" {
		t.Fatalf("rules after delete are %v", got)
	}
}

// A batch either lands whole or not at all: half a batch leaves the caller
// unable to say which clients now route where.
func TestABatchWithADuplicateTagWritesNothing(t *testing.T) {
	service := initObjectDB(t)
	before := storedRuleTags(t, service)

	batch := []json.RawMessage{
		json.RawMessage(`{"type":"field","ruleTag":"a","user":["a@example.com"],"outboundTag":"direct"}`),
		json.RawMessage(`{"type":"field","ruleTag":"a","user":["b@example.com"],"outboundTag":"direct"}`),
	}
	if _, err := service.AddRoutingRules(batch); err == nil {
		t.Fatal("a batch repeating a ruleTag must be refused")
	}
	if after := storedRuleTags(t, service); len(after) != len(before) {
		t.Fatalf("a refused batch was partly written: %v -> %v", before, after)
	}

	// Same for a tag that only collides with what is already stored.
	if _, err := service.AddRoutingRules([]json.RawMessage{batch[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddRoutingRules([]json.RawMessage{
		json.RawMessage(`{"type":"field","ruleTag":"new","user":["c@example.com"],"outboundTag":"direct"}`),
		json.RawMessage(`{"type":"field","ruleTag":"a","user":["d@example.com"],"outboundTag":"direct"}`),
	}); err == nil {
		t.Fatal("a batch colliding with a stored ruleTag must be refused")
	}
	for _, tag := range storedRuleTags(t, service) {
		if tag == "new" {
			t.Fatal("the first half of a refused batch was written")
		}
	}
}

func TestObjectsWithoutAnIdentityAreRefused(t *testing.T) {
	service := initObjectDB(t)

	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{
			name: "outbound without a tag",
			run: func() error {
				_, err := service.AddOutbound(json.RawMessage(`{"protocol":"freedom","settings":{}}`))
				return err
			},
			want: "has no tag",
		},
		{
			name: "outbound on the panel's reserved tag",
			run: func() error {
				_, err := service.AddOutbound(json.RawMessage(`{"tag":"` + internalDefaultOutboundTag + `","protocol":"freedom","settings":{}}`))
				return err
			},
			want: "reserved by the panel",
		},
		{
			name: "second outbound on a tag already taken",
			run: func() error {
				_, err := service.AddOutbound(json.RawMessage(`{"tag":"direct","protocol":"freedom","settings":{}}`))
				return err
			},
			want: "already exists",
		},
		{
			name: "renaming through PATCH",
			run: func() error {
				_, err := service.UpdateOutbound("direct", json.RawMessage(`{"tag":"renamed","protocol":"freedom","settings":{}}`))
				return err
			},
			want: "delete and re-add to rename",
		},
		{
			name: "routing rule without a ruleTag",
			run: func() error {
				_, err := service.AddRoutingRules([]json.RawMessage{json.RawMessage(`{"type":"field","outboundTag":"direct"}`)})
				return err
			},
			want: "has no ruleTag",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.run()
			if err == nil {
				t.Fatal("must be refused")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("the refusal must say why; got %q, want it to mention %q", err.Error(), testCase.want)
			}
		})
	}
}

// "Never existed" and "the core refused it" are different problems with
// different fixes, so they must not arrive as the same error.
func TestAnUnknownTagIsItsOwnError(t *testing.T) {
	service := initObjectDB(t)

	if _, err := service.DeleteOutbound("nope"); !errors.Is(err, ErrXrayObjectNotFound) {
		t.Fatalf("delete unknown outbound = %v, want ErrXrayObjectNotFound", err)
	}
	if _, err := service.UpdateOutbound("nope", json.RawMessage(`{"tag":"nope","protocol":"freedom","settings":{}}`)); !errors.Is(err, ErrXrayObjectNotFound) {
		t.Fatalf("patch unknown outbound = %v, want ErrXrayObjectNotFound", err)
	}
	if _, err := service.DeleteRoutingRule("nope"); !errors.Is(err, ErrXrayObjectNotFound) {
		t.Fatalf("delete unknown rule = %v, want ErrXrayObjectNotFound", err)
	}
}

// A single-object write must leave the rest of the template exactly as it was;
// the whole point is not having to resend everything else.
func TestASingleWriteDoesNotDisturbTheRestOfTheTemplate(t *testing.T) {
	service := initObjectDB(t)
	before, err := (&SettingService{}).GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal([]byte(before), &original); err != nil {
		t.Fatal(err)
	}

	if _, err := service.AddOutbound(json.RawMessage(`{"tag":"extra","protocol":"freedom","settings":{}}`)); err != nil {
		t.Fatal(err)
	}

	after, err := (&SettingService{}).GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var updated map[string]json.RawMessage
	if err := json.Unmarshal([]byte(after), &updated); err != nil {
		t.Fatal(err)
	}
	for key, value := range original {
		if key == "outbounds" {
			continue
		}
		// Compared as values, not as text: the write re-indents the document,
		// which is formatting, not a change to what the section says.
		if canonical(t, updated[key]) != canonical(t, value) {
			t.Fatalf("section %q changed: %s -> %s", key, value, updated[key])
		}
	}
}

func canonical(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
