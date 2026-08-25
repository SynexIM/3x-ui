package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"github.com/gin-gonic/gin"
)

/*
Single-object outbound and routing writes against a REAL xray-core process.

What this proves and a mock cannot: the core's pid does not change across a
write, a connection opened before the write still carries bytes after it, and
the new object appears in the core's own ListOutbounds / ListRuleFull answer
rather than only in the panel's database.

Skipped unless XRAY_E2E_BINARY points at an xray executable built from the
xray-core version in go.mod:

	go build -o /tmp/xray github.com/xtls/xray-core/main
	XRAY_E2E_BINARY=/tmp/xray go test ./internal/web/controller -run RealCore -v
*/

type realCoreFixture struct {
	t         *testing.T
	xray      service.XrayService
	objects   service.XrayObjectService
	server    *httptest.Server
	socksPort int
	echoAddr  string
}

func newRealCoreFixture(t *testing.T) *realCoreFixture {
	t.Helper()
	bin := os.Getenv("XRAY_E2E_BINARY")
	if bin == "" {
		t.Skip("set XRAY_E2E_BINARY to an xray binary to run this test")
	}

	binDir := t.TempDir()
	source, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read %s: %v", bin, err)
	}
	if err := os.WriteFile(filepath.Join(binDir, xray.GetBinaryName()), source, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XUI_BIN_FOLDER", binDir)

	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	f := &realCoreFixture{t: t}
	f.echoAddr = startEchoServer(t)
	apiPort := freeLocalPort(t)
	f.socksPort = freeLocalPort(t)

	template := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"api": map[string]any{"tag": "api", "services": []string{"HandlerService", "RoutingService", "StatsService"}},
		"inbounds": []any{
			map[string]any{
				"tag": "api", "listen": "127.0.0.1", "port": apiPort,
				"protocol": "tunnel", "settings": map[string]any{"rewriteAddress": "127.0.0.1"},
			},
			map[string]any{
				"tag": "socks-in", "listen": "127.0.0.1", "port": f.socksPort,
				"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false},
			},
		},
		"outbounds": []any{
			map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{}},
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules": []any{
				map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
			},
		},
		"policy": map[string]any{"system": map[string]any{"statsInboundUplink": true, "statsInboundDownlink": true}},
		"stats":  map[string]any{},
	}
	encoded, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&service.XraySettingService{}).SaveXraySetting(string(encoded)); err != nil {
		t.Fatalf("save template: %v", err)
	}
	if err := f.xray.RestartXray(true); err != nil {
		t.Fatalf("start xray: %v", err)
	}
	t.Cleanup(func() { _ = f.xray.StopXray() })
	if !f.xray.IsXrayRunning() {
		t.Fatal("xray did not come up")
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	NewXrayObjectController(engine.Group("/panel/api"))
	f.server = httptest.NewServer(engine)
	t.Cleanup(f.server.Close)
	return f
}

func (f *realCoreFixture) pid() int {
	f.t.Helper()
	pid := f.xray.GetXrayPID()
	if pid == 0 {
		f.t.Fatal("no xray process")
	}
	return pid
}

func (f *realCoreFixture) runtimeOutboundTags() []string {
	f.t.Helper()
	view, err := f.objects.ListOutbounds()
	if err != nil {
		f.t.Fatalf("list outbounds: %v", err)
	}
	if view.RuntimeError != "" {
		f.t.Fatalf("the core did not answer ListOutbounds: %s", view.RuntimeError)
	}
	tags := make([]string, 0, len(view.Runtime))
	for _, entry := range view.Runtime {
		tags = append(tags, entry.Tag)
	}
	return tags
}

func (f *realCoreFixture) runtimeRuleTags() []string {
	f.t.Helper()
	view, err := f.objects.ListRoutingRules()
	if err != nil {
		f.t.Fatalf("list rules: %v", err)
	}
	if view.RuntimeError != "" {
		f.t.Fatalf("the core did not answer ListRuleFull: %s", view.RuntimeError)
	}
	tags := make([]string, 0, len(view.Runtime))
	for _, entry := range view.Runtime {
		tags = append(tags, entry.RuleTag)
	}
	return tags
}

// TestRealCoreObjectWritesKeepTheProcessAndItsConnections is the acceptance for
// "add and remove an outbound and a routing rule without restarting xray and
// without dropping a live connection", driven through the HTTP handlers.
func TestRealCoreObjectWritesKeepTheProcessAndItsConnections(t *testing.T) {
	f := newRealCoreFixture(t)

	live := dialThroughSocks(t, f.socksPort, f.echoAddr)
	defer live.Close()
	echoOver(t, live, "before-any-write")
	pidBefore := f.pid()

	result := f.mustApply(http.MethodPost, "/panel/api/outbounds",
		`{"tag":"probe-out","protocol":"freedom","settings":{}}`)
	if !result.HotApplied || result.RequiresRestart {
		t.Fatalf("adding an outbound must be hot: %+v", result)
	}
	if pid := f.pid(); pid != pidBefore {
		t.Fatalf("xray restarted: pid %d -> %d", pidBefore, pid)
	}
	if tags := f.runtimeOutboundTags(); !contains(tags, "probe-out") {
		t.Fatalf("the core does not have the outbound; it has %v", tags)
	}
	echoOver(t, live, "after-outbound-add")

	result = f.mustApply(http.MethodPost, "/panel/api/routing/rules",
		`{"type":"field","ruleTag":"probe-rule","user":["probe@example.com"],"outboundTag":"probe-out"}`)
	if !result.HotApplied || result.RequiresRestart {
		t.Fatalf("adding a routing rule must be hot: %+v", result)
	}
	if tags := f.runtimeRuleTags(); !contains(tags, "probe-rule") {
		t.Fatalf("the core does not have the rule; it has %v", tags)
	}
	echoOver(t, live, "after-rule-add")

	// A PATCH must land in the core too, not only in the stored template.
	f.mustApply(http.MethodPatch, "/panel/api/outbounds/probe-out",
		`{"tag":"probe-out","protocol":"blackhole","settings":{}}`)
	if tags := f.runtimeOutboundTags(); !contains(tags, "probe-out") {
		t.Fatalf("the patched outbound left the core: %v", tags)
	}
	echoOver(t, live, "after-outbound-patch")

	f.mustApply(http.MethodDelete, "/panel/api/routing/rules/probe-rule", "")
	if tags := f.runtimeRuleTags(); contains(tags, "probe-rule") {
		t.Fatalf("the rule is still in the core: %v", tags)
	}
	f.mustApply(http.MethodDelete, "/panel/api/outbounds/probe-out", "")
	if tags := f.runtimeOutboundTags(); contains(tags, "probe-out") {
		t.Fatalf("the outbound is still in the core: %v", tags)
	}
	echoOver(t, live, "after-deletes")

	if pid := f.pid(); pid != pidBefore {
		t.Fatalf("xray restarted somewhere in the sequence: pid %d -> %d", pidBefore, pid)
	}
	t.Logf("MEASURED: pid stayed %d across 5 single-object writes; the connection opened before the first one still echoed after the last", pidBefore)
}

// A tag nobody created must answer 404, not a generic failure: that is the
// difference between "retry" and "stop".
func TestRealCoreUnknownTagIsNotFound(t *testing.T) {
	f := newRealCoreFixture(t)
	status, body := f.call(http.MethodDelete, "/panel/api/outbounds/never-existed", "")
	if status != http.StatusNotFound {
		t.Fatalf("deleting an unknown tag answered %d, want 404: %s", status, body)
	}
	status, body = f.call(http.MethodDelete, "/panel/api/routing/rules/never-existed", "")
	if status != http.StatusNotFound {
		t.Fatalf("deleting an unknown rule answered %d, want 404: %s", status, body)
	}
}

// An object xray-core would refuse must be refused before it is persisted, or
// the panel saves a config the core cannot start from.
func TestRealCoreRefusesAnInvalidOutboundBeforeSaving(t *testing.T) {
	f := newRealCoreFixture(t)
	before := f.storedOutboundTags()
	status, body := f.call(http.MethodPost, "/panel/api/outbounds",
		`{"tag":"broken","protocol":"nonsense","settings":{}}`)
	if status != http.StatusOK || !strings.Contains(body, `"success":false`) {
		t.Fatalf("an invalid outbound must be refused; got %d %s", status, body)
	}
	if after := f.storedOutboundTags(); len(after) != len(before) {
		t.Fatalf("a refused outbound was persisted anyway: %v -> %v", before, after)
	}
}

// TestRealCoreThousandRuleBatch is the measurement the plan asks for. The
// number is the deliverable, but every rule still has to reach the core: a
// fast call that applied nothing is not a faster call.
func TestRealCoreThousandRuleBatch(t *testing.T) {
	f := newRealCoreFixture(t)
	pidBefore := f.pid()
	f.mustApply(http.MethodPost, "/panel/api/outbounds", `{"tag":"bulk-out","protocol":"freedom","settings":{}}`)

	const count = 1000
	rules := make([]json.RawMessage, 0, count)
	for i := range count {
		rules = append(rules, json.RawMessage(fmt.Sprintf(
			`{"type":"field","ruleTag":"bulk-%04d","user":["bulk-%04d@example.com"],"outboundTag":"bulk-out"}`, i, i)))
	}
	body, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	result := f.mustApply(http.MethodPost, "/panel/api/routing/rules:batch", string(body))
	elapsed := time.Since(start)
	if result.Count != count || !result.HotApplied {
		t.Fatalf("batch result %+v", result)
	}

	present := 0
	for _, tag := range f.runtimeRuleTags() {
		if strings.HasPrefix(tag, "bulk-") {
			present++
		}
	}
	if present != count {
		t.Fatalf("the core holds %d of the %d rules", present, count)
	}
	if pid := f.pid(); pid != pidBefore {
		t.Fatalf("xray restarted: pid %d -> %d", pidBefore, pid)
	}
	t.Logf("MEASURED: %d routing rules appended in %d ms (%.3f ms/rule end to end, HTTP included), core pid unchanged at %d",
		count, elapsed.Milliseconds(), float64(elapsed.Microseconds())/1000.0/float64(count), pidBefore)
}

// TestRealCoreWholeTemplateSaveStillWorks pins the "only adds, removes
// nothing" promise: the pre-existing whole-config path keeps working next to
// the new single-object one, and neither erases the other's objects.
func TestRealCoreWholeTemplateSaveStillWorks(t *testing.T) {
	f := newRealCoreFixture(t)
	pidBefore := f.pid()
	f.mustApply(http.MethodPost, "/panel/api/outbounds", `{"tag":"kept-by-object-api","protocol":"freedom","settings":{}}`)

	template, err := (&service.SettingService{}).GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(template), &cfg); err != nil {
		t.Fatal(err)
	}
	var outbounds []json.RawMessage
	if err := json.Unmarshal(cfg["outbounds"], &outbounds); err != nil {
		t.Fatal(err)
	}
	outbounds = append(outbounds, json.RawMessage(`{"tag":"added-by-whole-save","protocol":"freedom","settings":{}}`))
	if cfg["outbounds"], err = json.Marshal(outbounds); err != nil {
		t.Fatal(err)
	}
	whole, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&service.XraySettingService{}).SaveXraySetting(string(whole)); err != nil {
		t.Fatalf("whole-template save: %v", err)
	}
	if err := f.xray.RestartXray(false); err != nil {
		t.Fatalf("apply whole template: %v", err)
	}

	tags := f.runtimeOutboundTags()
	if !contains(tags, "added-by-whole-save") || !contains(tags, "kept-by-object-api") {
		t.Fatalf("the whole-template save must keep both objects; core has %v", tags)
	}
	if pid := f.pid(); pid != pidBefore {
		t.Fatalf("the whole-template save restarted the core: pid %d -> %d", pidBefore, pid)
	}
}

// ------------------------------------------------------------------ helpers

func (f *realCoreFixture) call(method, path, body string) (int, string) {
	f.t.Helper()
	request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
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

func (f *realCoreFixture) mustApply(method, path, body string) service.ObjectApplyResult {
	f.t.Helper()
	status, payload := f.call(method, path, body)
	if status != http.StatusOK {
		f.t.Fatalf("%s %s answered %d: %s", method, path, status, payload)
	}
	var envelope struct {
		Success bool                      `json:"success"`
		Msg     string                    `json:"msg"`
		Obj     service.ObjectApplyResult `json:"obj"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		f.t.Fatalf("%s %s answered unparsable JSON: %s", method, path, payload)
	}
	if !envelope.Success {
		f.t.Fatalf("%s %s failed: %s", method, path, envelope.Msg)
	}
	return envelope.Obj
}

func (f *realCoreFixture) storedOutboundTags() []string {
	f.t.Helper()
	view, err := f.objects.ListOutbounds()
	if err != nil {
		f.t.Fatal(err)
	}
	tags := make([]string, 0, len(view.Outbounds))
	for _, raw := range view.Outbounds {
		var tagged struct {
			Tag string `json:"tag"`
		}
		_ = json.Unmarshal(raw, &tagged)
		tags = append(tags, tagged.Tag)
	}
	return tags
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// startEchoServer gives the live connection something real to talk to, so
// "the connection survived" means bytes moved, not that a socket looked open.
func startEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

// dialThroughSocks opens a connection through the running core and leaves it
// open, which is the only way to tell a hot reload from a fast restart.
func dialThroughSocks(t *testing.T, socksPort int, target string) net.Conn {
	t.Helper()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), time.Second)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the core's socks inbound never accepted a connection: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatalf("socks greeting: %v", err)
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		t.Fatalf("socks greeting %v", greeting)
	}
	request := []byte{0x05, 0x01, 0x00, 0x01}
	request = append(request, net.ParseIP(host).To4()...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("socks reply: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("socks refused the connection: %v", reply)
	}
	return conn
}

// echoOver writes and reads on the long-lived connection. A restarted core
// would have closed it, so this failing is the restart detector.
func echoOver(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("the live connection died before %q: %v", payload, err)
	}
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("the live connection died during %q: %v", payload, err)
	}
	if string(buffer) != payload {
		t.Fatalf("the live connection returned %q, want %q", buffer, payload)
	}
}
