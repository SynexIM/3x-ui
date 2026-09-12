package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestRuntimePatchPreservesValidityAndIdentity(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	NewClientController(engine.Group("/panel/api/clients"))
	record := model.ClientRecord{
		Email: "runtime-test", UUID: "keep-uuid", Password: "test-password", Auth: "test-auth", Flow: "xtls-rprx-vision", Enable: false,
		ExpiryTime: 2_000_000_000_000, TotalGB: 12345678, LimitIP: 3, Reset: 7, Group: "keep-group", Comment: "keep-comment", Security: "auto", SubID: "keep-sub",
		DownloadBandwidthBps: 24_000_000, UploadBandwidthBps: 8_000_000, UploadBurstBytes: 3_000_000, DownloadBurstBytes: 5_000_000, ConnLimit: 4,
	}
	if err := database.GetDB().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&record).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(&record, record.Id).Error; err != nil {
		t.Fatal(err)
	}
	before := record
	send := func(body string) bool {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/panel/api/clients/runtime/runtime-test", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(response, request)
		var result struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Success
	}
	if !send(`{"upload_bandwidth_bps":16000000,"conn_limit":0}`) {
		t.Fatal("valid runtime patch rejected")
	}
	if err := database.GetDB().First(&record, record.Id).Error; err != nil {
		t.Fatal(err)
	}
	want := before
	want.UploadBandwidthBps, want.ConnLimit, want.UpdatedAt = 16_000_000, 0, record.UpdatedAt
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("runtime patch changed unrelated fields: got %+v; want %+v", record, want)
	}
	for _, body := range []string{`{"expiryTime":0}`, `{"upload_bandwidth_bps":-1}`, `{}`} {
		if send(body) {
			t.Errorf("invalid patch accepted: %s", body)
		}
	}
	if err := database.GetDB().First(&record, record.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record, want) {
		t.Fatal("invalid patch changed stored state")
	}
}
