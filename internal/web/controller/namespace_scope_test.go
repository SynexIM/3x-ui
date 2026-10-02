package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
)

// A scoped token is what replaces the read-only lock: instead of freezing the
// panel the moment automation touches it, the automation is confined to the
// namespaces it declared and the operator keeps the whole node.
type scopeFixture struct {
	t      *testing.T
	server *httptest.Server
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	template := `{"inbounds":[],"outbounds":[{"tag":"direct","protocol":"freedom","settings":{}}],"routing":{"rules":[]}}`
	if err := (&service.XraySettingService{}).SaveXraySetting(template); err != nil {
		t.Fatalf("save template: %v", err)
	}

	// The real API router, so the test covers the actual middleware chain and
	// not a reconstruction of it. ServerController schedules a ticker at
	// construction time, which needs a cron to attach to.
	previous := global.GetWebServer()
	stub := &stubWebServer{cron: cron.New(cron.WithSeconds())}
	stub.ctx, stub.cancel = context.WithCancel(context.Background())
	global.SetWebServer(stub)
	t.Cleanup(func() {
		stub.cancel()
		stub.cron.Stop()
		global.SetWebServer(previous)
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	NewAPIController(engine.Group(""))
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return &scopeFixture{t: t, server: server}
}

type stubWebServer struct {
	cron   *cron.Cron
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *stubWebServer) GetCron() *cron.Cron     { return s.cron }
func (s *stubWebServer) GetCtx() context.Context { return s.ctx }
func (s *stubWebServer) GetWSHub() any           { return nil }

func (f *scopeFixture) newToken(name string, namespaces []string) string {
	f.t.Helper()
	view, err := (&panel.ApiTokenService{}).Create(name, "", 0, namespaces)
	if err != nil {
		f.t.Fatalf("create token: %v", err)
	}
	return view.Token
}

func (f *scopeFixture) call(token, method, path, body string) (int, string) {
	f.t.Helper()
	request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return response.StatusCode, string(payload)
}

func succeeded(t *testing.T, payload string) bool {
	t.Helper()
	var envelope struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("unparsable response: %s", payload)
	}
	return envelope.Success
}

func TestAScopedTokenIsConfinedToItsOwnNamespaces(t *testing.T) {
	f := newScopeFixture(t)
	scoped := f.newToken("automation", []string{"ipl_"})

	t.Run("inside its namespace it works", func(t *testing.T) {
		status, body := f.call(scoped, http.MethodPost, "/panel/api/outbounds",
			`{"tag":"ipl_jp","protocol":"freedom","settings":{}}`)
		if status != http.StatusOK || !succeeded(t, body) {
			t.Fatalf("a token must be able to write inside its own namespace; got %d %s", status, body)
		}
	})

	t.Run("creating an object outside it is refused", func(t *testing.T) {
		status, body := f.call(scoped, http.MethodPost, "/panel/api/outbounds",
			`{"tag":"hand-made","protocol":"freedom","settings":{}}`)
		if status != http.StatusForbidden {
			t.Fatalf("creating outside the namespace answered %d, want 403: %s", status, body)
		}
		if !strings.Contains(body, "hand-made") {
			t.Fatalf("the refusal must name the object it would not touch: %s", body)
		}
	})

	t.Run("deleting an object outside it is refused", func(t *testing.T) {
		status, body := f.call(scoped, http.MethodDelete, "/panel/api/outbounds/direct", "")
		if status != http.StatusForbidden {
			t.Fatalf("deleting outside the namespace answered %d, want 403: %s", status, body)
		}
	})

	t.Run("an object it cannot even identify is refused", func(t *testing.T) {
		// Refusing here is the point: "I cannot tell what this touches" must not
		// be a way around the scope.
		status, body := f.call(scoped, http.MethodPost, "/panel/api/clients/resetAllTraffics", `{}`)
		if status != http.StatusForbidden {
			t.Fatalf("an unidentifiable write answered %d, want 403: %s", status, body)
		}
	})

	t.Run("reading the whole node is still allowed", func(t *testing.T) {
		status, _ := f.call(scoped, http.MethodGet, "/panel/api/outbounds", "")
		if status != http.StatusOK {
			t.Fatalf("a scoped token must still be able to read the node; got %d", status)
		}
	})

	t.Run("a nested identity is found too", func(t *testing.T) {
		// A whole-node payload names its objects inside nested arrays; a check
		// that only read the top level would wave the whole thing through.
		status, body := f.call(scoped, http.MethodPost, "/panel/api/routing/rules",
			`{"type":"field","ruleTag":"ipl_r1","user":["someone-elses@example.com"],"outboundTag":"ipl_jp"}`)
		if status != http.StatusForbidden {
			t.Fatalf("a rule naming a client outside the namespace answered %d, want 403: %s", status, body)
		}
	})

	t.Run("a client cannot point at an unowned egress", func(t *testing.T) {
		status, body := f.call(scoped, http.MethodPost, "/panel/api/clients/add",
			`{"client":{"email":"ipl_line@example.invalid","egress_tag":"hand-made"},"inboundIds":[]}`)
		if status != http.StatusForbidden {
			t.Fatalf("an unowned egress tag answered %d, want 403: %s", status, body)
		}
	})
}

// Every token created before namespaces existed has none, and must keep
// working exactly as it did.
func TestATokenWithoutNamespacesIsUnrestricted(t *testing.T) {
	f := newScopeFixture(t)
	open := f.newToken("legacy", nil)

	status, body := f.call(open, http.MethodPost, "/panel/api/outbounds",
		`{"tag":"hand-made","protocol":"freedom","settings":{}}`)
	if status != http.StatusOK || !succeeded(t, body) {
		t.Fatalf("an unscoped token must be able to write anywhere; got %d %s", status, body)
	}
}

// The namespaces a token owns are what the pages read to mark objects a
// reconciliation may overwrite.
func TestManagedNamespacesAreTheUnionOfEnabledTokens(t *testing.T) {
	f := newScopeFixture(t)
	f.newToken("a", []string{"ipl_", "shared_"})
	f.newToken("b", []string{"shared_", "fleet-"})
	f.newToken("c", nil)

	got := service.ManagedNamespaces()
	want := []string{"fleet-", "ipl_", "shared_"}
	if len(got) != len(want) {
		t.Fatalf("managed namespaces = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("managed namespaces = %v, want %v", got, want)
		}
	}

	// A disabled token owns nothing: its objects are nobody's to restore.
	tokens, err := (&panel.ApiTokenService{}).List()
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		if token.Name == "b" {
			if err := (&panel.ApiTokenService{}).SetEnabled(token.Id, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	got = service.ManagedNamespaces()
	if len(got) != 2 || got[0] != "ipl_" || got[1] != "shared_" {
		t.Fatalf("after disabling b the namespaces are %v, want [ipl_ shared_]", got)
	}
}

// A prefix that owns almost everything is a footgun, not a namespace.
func TestNamespacePrefixesAreValidated(t *testing.T) {
	if _, err := service.JoinNamespaces([]string{"i"}); err == nil {
		t.Fatal("a one-character prefix must be refused")
	}
	if _, err := service.JoinNamespaces([]string{"a,b"}); err == nil {
		t.Fatal("a prefix containing the list separator must be refused")
	}
	stored, err := service.JoinNamespaces([]string{" ipl_ ", "ipl_", "", "fleet-"})
	if err != nil {
		t.Fatal(err)
	}
	if stored != "ipl_,fleet-" {
		t.Fatalf("stored %q, want the list trimmed and deduplicated", stored)
	}
}

func seedScopedInbound(t *testing.T, tag string, port int) *model.Inbound {
	t.Helper()
	row := &model.Inbound{Tag: tag, Port: port, Protocol: model.VMESS, Settings: `{"clients":[]}`, StreamSettings: `{"network":"tcp","security":"none"}`}
	if err := database.GetDB().Create(row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestScopedInboundIDsAndHostGroups(t *testing.T) {
	f := newScopeFixture(t)
	token := f.newToken("scoped", []string{"ipl_"})
	own := seedScopedInbound(t, "ipl_owned", 21001)
	foreign := seedScopedInbound(t, "operator_manual", 21002)
	t.Run("foreign numeric inbound denied", func(t *testing.T) {
		status, body := f.call(token, "POST", fmt.Sprintf("/panel/api/inbounds/del/%d", foreign.Id), "")
		if status != 403 || !strings.Contains(body, "ipl_") || !strings.Contains(body, foreign.Tag) {
			t.Fatalf("delete: %d %s", status, body)
		}
		var count int64
		database.GetDB().Model(&model.Inbound{}).Where("id = ?", foreign.Id).Count(&count)
		if count != 1 {
			t.Fatal("foreign inbound was deleted")
		}
	})
	t.Run("mixed bulk ids are atomic denial", func(t *testing.T) {
		status, body := f.call(token, "POST", "/panel/api/inbounds/bulkDel", fmt.Sprintf(`{"ids":[%d,%d]}`, own.Id, foreign.Id))
		if status != 403 || !strings.Contains(body, foreign.Tag) {
			t.Fatalf("bulk delete: %d %s", status, body)
		}
	})
	t.Run("missing inbound cannot pass", func(t *testing.T) {
		status, body := f.call(token, "POST", "/panel/api/inbounds/del/99999", "")
		if status != 403 || !strings.Contains(body, "99999") {
			t.Fatalf("missing inbound: %d %s", status, body)
		}
	})
	host := &model.Host{GroupId: "foreign-host", InboundId: foreign.Id, Remark: "manual", Address: "example.invalid"}
	if err := database.GetDB().Create(host).Error; err != nil {
		t.Fatal(err)
	}
	t.Run("host old association denied even when moved to own inbound", func(t *testing.T) {
		status, body := f.call(token, "POST", "/panel/api/hosts/update/foreign-host", fmt.Sprintf(`{"inboundIds":[%d],"remark":"move","hosts":["own.invalid"]}`, own.Id))
		if status != 403 || !strings.Contains(body, foreign.Tag) {
			t.Fatalf("host update: %d %s", status, body)
		}
		var stored model.Host
		if err := database.GetDB().First(&stored, host.Id).Error; err != nil || stored.InboundId != foreign.Id {
			t.Fatalf("host changed: %+v %v", stored, err)
		}
	})
	t.Run("host create on own inbound works", func(t *testing.T) {
		status, body := f.call(token, "POST", "/panel/api/hosts/add", fmt.Sprintf(`{"inboundIds":[%d],"remark":"owned","hosts":["own.invalid"]}`, own.Id))
		if status != 200 || !succeeded(t, body) {
			t.Fatalf("own host create: %d %s", status, body)
		}
	})
	t.Run("own numeric inbound allowed", func(t *testing.T) {
		status, body := f.call(token, "POST", fmt.Sprintf("/panel/api/inbounds/del/%d", own.Id), "")
		if status != 200 || !succeeded(t, body) {
			t.Fatalf("own delete: %d %s", status, body)
		}
		var count int64
		database.GetDB().Model(&model.Inbound{}).Where("id = ?", own.Id).Count(&count)
		if count != 0 {
			t.Fatal("own inbound remains")
		}
	})
}

func TestScopedBulkClientEmails(t *testing.T) {
	f := newScopeFixture(t)
	token := f.newToken("scoped", []string{"ipl_"})
	for _, email := range []string{"ipl_alice", "manual-bob"} {
		if err := database.GetDB().Create(&model.ClientRecord{Email: email}).Error; err != nil {
			t.Fatal(err)
		}
	}
	status, body := f.call(token, "POST", "/panel/api/clients/bulkDel", `{"emails":["ipl_alice","manual-bob"]}`)
	if status != 403 || !strings.Contains(body, "manual-bob") {
		t.Fatalf("foreign bulk client: %d %s", status, body)
	}
	var count int64
	database.GetDB().Model(&model.ClientRecord{}).Count(&count)
	if count != 2 {
		t.Fatalf("denial changed client count to %d", count)
	}
	status, body = f.call(token, "POST", "/panel/api/clients/bulkDel", `{"emails":["ipl_alice"]}`)
	if status != 200 || !succeeded(t, body) {
		t.Fatalf("own bulk client: %d %s", status, body)
	}
	database.GetDB().Model(&model.ClientRecord{}).Count(&count)
	if count != 1 {
		t.Fatalf("own delete left %d clients", count)
	}
}

func TestScopedFairShareMergeAndNodeAuthorization(t *testing.T) {
	f := newScopeFixture(t)
	token := f.newToken("scoped", []string{"ipl_"})
	svc := &service.FairShareService{}
	initial := &service.FairSharePolicy{AvailBitPerSec: 1000, CongestionEnterPercent: 85, CongestionExitPercent: 70, CongestionExitTicks: 5, Classes: []service.FairShareClassPolicy{{Name: "manual", Weight: 7}, {Name: "other_blue", Weight: 3}, {Name: "ipl_old", Weight: 1}}}
	if err := svc.SavePolicy(initial); err != nil {
		t.Fatal(err)
	}
	payload := `{"availBitPerSec":1000,"congestionEnterPercent":85,"congestionExitPercent":70,"congestionExitTicks":5,"classes":[{"name":"ipl_new","weight":2}]}`
	status, body := f.call(token, "POST", "/panel/api/nodes/fairshare", payload)
	if status != 200 || !succeeded(t, body) {
		t.Fatalf("merge: %d %s", status, body)
	}
	got, err := svc.GetPolicy()
	if err != nil {
		t.Fatal(err)
	}
	want := []service.FairShareClassPolicy{{Name: "ipl_new", Weight: 2}, initial.Classes[0], initial.Classes[1]}
	if !reflect.DeepEqual(got.Classes, want) {
		t.Fatalf("classes = %+v, want %+v", got.Classes, want)
	}
	for _, tc := range []struct{ name, payload, reason string }{
		{"foreign class", strings.Replace(payload, "ipl_new", "manual", 1), "manual"},
		{"bandwidth", strings.Replace(payload, "1000", "2000", 1), "nodeSettings"},
		{"congestion", strings.Replace(payload, "85", "90", 1), "nodeSettings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := f.call(token, "POST", "/panel/api/nodes/fairshare", tc.payload)
			if status != 403 || !strings.Contains(body, tc.reason) {
				t.Fatalf("denial: %d %s", status, body)
			}
			saved, err := svc.GetPolicy()
			if err != nil || !reflect.DeepEqual(saved, got) {
				t.Fatalf("denial changed policy: %+v %v", saved, err)
			}
		})
	}
	authorized, err := (&panel.ApiTokenService{}).Create("node-writer", "", 0, []string{"ipl_"}, true)
	if err != nil {
		t.Fatal(err)
	}
	status, body = f.call(authorized.Token, "POST", "/panel/api/nodes/fairshare", strings.Replace(payload, "1000", "2000", 1))
	if status != 200 || !succeeded(t, body) {
		t.Fatalf("authorized node change: %d %s", status, body)
	}
	got, err = svc.GetPolicy()
	if err != nil || got.AvailBitPerSec != 2000 || !reflect.DeepEqual(got.Classes, want) {
		t.Fatalf("authorized saved policy: %+v %v", got, err)
	}
	status, body = f.call(authorized.Token, "POST", "/panel/api/nodes/fairshare", `{"availBitPerSec":2000,"congestionEnterPercent":85,"congestionExitPercent":70,"congestionExitTicks":5,"classes":[]}`)
	if status != 200 || !succeeded(t, body) {
		t.Fatalf("empty owned class replacement: %d %s", status, body)
	}
	got, err = svc.GetPolicy()
	if err != nil || !reflect.DeepEqual(got.Classes, initial.Classes[:2]) {
		t.Fatalf("foreign classes after empty replacement: %+v %v", got, err)
	}
}

func TestValidateInboundsReportsPathsWithoutSaving(t *testing.T) {
	f := newScopeFixture(t)
	token := f.newToken("scoped", []string{"ipl_"})
	seedScopedInbound(t, "operator_manual", 21101)
	before := map[string]int64{}
	for _, table := range []string{"inbounds", "clients", "client_inbounds", "hosts", "settings"} {
		var count int64
		if err := database.GetDB().Table(table).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		before[table] = count
	}
	payload := `[
 {"protocol":"vmess","tag":"not-owned-new","port":21102,"settings":{"clients":[]},"streamSettings":{"network":"tcp","security":"none"}},
 {"protocol":"vmess","tag":"operator_manual","port":21101,"settings":{"clients":[]},"streamSettings":{"network":"tcp","security":"none"}},
 {"protocol":"vmess","tag":"conflict","port":21101,"settings":{"clients":[]}},
 {"protocol":"vmess","tag":"bad-core","port":21103,"settings":{"clients":[]},"streamSettings":{"network":"no-such-transport","security":"none"}},
 {"protocol":"invalid","tag":"bad-panel","port":99999,"settings":{}},
 {"protocol":"vmess","tag":"not-owned-new","port":21102,"settings":{"clients":[]}}
 ]`
	status, body := f.call(token, "POST", "/panel/api/inbounds/validate", payload)
	var envelope struct {
		Success bool                              `json:"success"`
		Obj     []service.InboundValidationResult `json:"obj"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("validation response: %s %v", body, err)
	}
	if status != 200 || !envelope.Success || len(envelope.Obj) != 6 {
		t.Fatalf("validate: %d %s", status, body)
	}
	if !envelope.Obj[0].OK || !envelope.Obj[1].OK {
		t.Fatalf("valid draft / same-tag update rejected: %s", body)
	}
	for _, index := range []int{2, 3, 4, 5} {
		result := envelope.Obj[index]
		if result.OK || len(result.Errors) == 0 {
			t.Fatalf("bad draft %d passed: %s", index, body)
		}
		for _, issue := range result.Errors {
			if issue.Path == "" || issue.Message == "" {
				t.Fatalf("missing path/message: %+v", issue)
			}
		}
	}
	for table, want := range before {
		var got int64
		if err := database.GetDB().Table(table).Count(&got).Error; err != nil || got != want {
			t.Fatalf("validate changed %s: %d -> %d (%v)", table, want, got, err)
		}
	}
	status, body = f.call(token, "POST", "/panel/api/inbounds/validate", `{"protocol":"vmess","port":21104,"settings":{"clients":[]}}`)
	if status != 200 || !succeeded(t, body) || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("single add-shaped payload: %d %s", status, body)
	}
}
