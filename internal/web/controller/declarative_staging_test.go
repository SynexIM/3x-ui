package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/web/locale"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// hashOfStagedConfig reproduces the panel's config identity from the public
// struct alone — the same thing a caller has to do to send a commit.
func hashOfStagedConfig(t *testing.T, document []byte) string {
	t.Helper()
	request := &service.DeclarativeApplyRequest{}
	if err := json.Unmarshal(document, request); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request.Config)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func declarativeRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("I18n", func(_ locale.I18nType, key string, _ ...string) string { return key })
		c.Next()
	})
	NewXraySettingController(engine.Group("/panel/api"))
	return engine
}

func post(t *testing.T, engine *gin.Engine, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	return recorder
}

// stageAll uploads a document in small pieces, which is the whole point of the
// endpoint: no single request has to carry the node.
func stageAll(t *testing.T, engine *gin.Engine, uploadID string, document []byte, pieceSize int) {
	t.Helper()
	for seq := 0; seq*pieceSize < len(document); seq++ {
		start := seq * pieceSize
		end := min(start+pieceSize, len(document))
		got := post(t, engine, fmt.Sprintf("/panel/api/declarative/stage?uploadId=%s&seq=%d", uploadID, seq), document[start:end])
		if got.Code != http.StatusOK {
			t.Fatalf("chunk %d: %d %s", seq, got.Code, got.Body.String())
		}
	}
}

// A line config the panel accepts as far as validation, so a refusal can only
// come from the staging and commit layer.
func stagedApplyDocument(t *testing.T) []byte {
	t.Helper()
	document, err := json.Marshal(map[string]any{
		"revision": 3,
		"config": map[string]any{
			"nodeBandwidthBps": 480_000_000,
			"inbounds": []any{map[string]any{
				"tag": "line-vless", "protocol": "vless", "listenPort": 30800,
				"shareAddr":      map[string]any{"strategy": "custom", "host": "entry.line.invalid", "port": 30800},
				"settings":       map[string]any{},
				"streamSettings": map[string]any{"network": "tcp"},
				"clients": []any{map[string]any{
					"email": "line-001@line.invalid",
					"uuid":  "11111111-1111-1111-1111-111111111111", "pirBps": 100_000_000,
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestStagedFullApplyIsRefusedWhenItIsNotWhatWasPromised(t *testing.T) {
	engine := declarativeRouter(t)
	document := stagedApplyDocument(t)

	t.Run("a hash that does not match what was staged", func(t *testing.T) {
		stageAll(t, engine, "upload-a", document, 64)
		body, _ := json.Marshal(map[string]string{
			"uploadId":     "upload-a",
			"expectedHash": "1111111111111111111111111111111111111111111111111111111111111111",
		})
		got := post(t, engine, "/panel/api/declarative/commit", body)
		if got.Code != http.StatusUnprocessableEntity {
			t.Fatalf("got %d %s, want 422", got.Code, got.Body.String())
		}
		if !strings.Contains(got.Body.String(), "was expected") {
			t.Fatalf("the refusal must say the upload is not what was meant; got %s", got.Body.String())
		}
	})

	t.Run("the upload is consumed whatever the outcome", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"uploadId": "upload-a", "expectedHash": "whatever"})
		got := post(t, engine, "/panel/api/declarative/commit", body)
		if got.Code != http.StatusNotFound {
			t.Fatalf("got %d %s, want 404", got.Code, got.Body.String())
		}
	})

	t.Run("a chunk that would leave a hole", func(t *testing.T) {
		stageAll(t, engine, "upload-b", document[:64], 64)
		got := post(t, engine, "/panel/api/declarative/stage?uploadId=upload-b&seq=9", []byte("x"))
		if got.Code != http.StatusConflict {
			t.Fatalf("got %d %s, want 409", got.Code, got.Body.String())
		}
		if !strings.Contains(got.Body.String(), "waiting for chunk 1") {
			t.Fatalf("the refusal must say which chunk is wanted; got %s", got.Body.String())
		}
	})

	t.Run("a resumed upload the panel never saw", func(t *testing.T) {
		got := post(t, engine, "/panel/api/declarative/stage?uploadId=ghost&seq=4", []byte("x"))
		if got.Code != http.StatusNotFound {
			t.Fatalf("got %d %s, want 404", got.Code, got.Body.String())
		}
	})

	t.Run("aborting frees the upload", func(t *testing.T) {
		stageAll(t, engine, "upload-c", document, 64)
		if got := post(t, engine, "/panel/api/declarative/abort?uploadId=upload-c", nil); got.Code != http.StatusNoContent {
			t.Fatalf("abort: got %d", got.Code)
		}
		body, _ := json.Marshal(map[string]string{"uploadId": "upload-c", "expectedHash": "whatever"})
		if got := post(t, engine, "/panel/api/declarative/commit", body); got.Code != http.StatusNotFound {
			t.Fatalf("an aborted upload must be gone; got %d %s", got.Code, got.Body.String())
		}
	})
}

// A staged config that is illegal must be refused exactly like a single-shot
// one: 422, not 500, and with a reason the caller can act on.
func TestAStagedIllegalConfigIsRefusedTheSameWayAWholeOneIs(t *testing.T) {
	engine := declarativeRouter(t)
	document, err := json.Marshal(map[string]any{
		"revision": 3,
		"config": map[string]any{
			"inbounds": []any{map[string]any{
				"tag": "line-telepathy", "protocol": "telepathy", "listenPort": 30800,
				"shareAddr":      map[string]any{"strategy": "custom", "host": "entry.line.invalid", "port": 30800},
				"settings":       map[string]any{},
				"streamSettings": map[string]any{"network": "tcp"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stageAll(t, engine, "upload-illegal", document, 64)

	// Commit with the hash the caller would have computed, so the
	// refusal can only come from the config itself.
	body, _ := json.Marshal(map[string]string{
		"uploadId":     "upload-illegal",
		"expectedHash": hashOfStagedConfig(t, document),
	})
	got := post(t, engine, "/panel/api/declarative/commit", body)
	if got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d %s, want 422", got.Code, got.Body.String())
	}
	if !strings.Contains(got.Body.String(), "telepathy") {
		t.Fatalf("the refusal must name what is wrong; got %s", got.Body.String())
	}
}
