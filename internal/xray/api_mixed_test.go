package xray

import (
	"testing"

	"github.com/xtls/xray-core/proxy/socks"
	"google.golang.org/protobuf/proto"
)

func TestBuildUserAccountMixedUsesSocksCredentials(t *testing.T) {
	typed, err := buildUserAccount("mixed", map[string]any{
		"email": "line@example.test",
		"user":  "line@example.test",
		"pass":  "proxy-secret",
	})
	if err != nil {
		t.Fatalf("build mixed account: %v", err)
	}
	account := new(socks.Account)
	if err := proto.Unmarshal(typed.Value, account); err != nil {
		t.Fatalf("decode mixed account: %v", err)
	}
	if account.Username != "line@example.test" || account.Password != "proxy-secret" {
		t.Fatalf("mixed account = %#v", account)
	}
}

func TestBuildUserAccountMixedRequiresUsernameAndPassword(t *testing.T) {
	for _, user := range []map[string]any{
		{"pass": "proxy-secret"},
		{"user": "line@example.test"},
	} {
		if _, err := buildUserAccount("mixed", user); err == nil {
			t.Fatalf("incomplete mixed account was accepted: %#v", user)
		}
	}
}
