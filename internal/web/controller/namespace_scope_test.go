package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
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
	view, err := (&panel.ApiTokenService{}).Create(name, namespaces)
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
