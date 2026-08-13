package runtime

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestRequiresInboundReloadForUserMutation(t *testing.T) {
	tests := []struct {
		protocol model.Protocol
		want     bool
	}{
		{protocol: model.Mixed, want: true},
		{protocol: model.HTTP, want: true},
		{protocol: model.VLESS, want: false},
		{protocol: model.VMESS, want: false},
		{protocol: model.Trojan, want: false},
		{protocol: model.Shadowsocks, want: false},
		{protocol: model.Hysteria, want: false},
	}
	for _, tc := range tests {
		t.Run(string(tc.protocol), func(t *testing.T) {
			if got := requiresInboundReloadForUserMutation(tc.protocol); got != tc.want {
				t.Fatalf("requiresInboundReloadForUserMutation(%q)=%v, want %v", tc.protocol, got, tc.want)
			}
		})
	}
}
