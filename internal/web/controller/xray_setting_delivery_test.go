package controller

import "testing"

func TestDeliveryProtocolForResourceLink(t *testing.T) {
	tests := map[string]string{
		"vless://id@example.com:443":   "vless",
		"vmess://encoded":              "vmess",
		"ss://encoded@example.com":     "shadowsocks",
		"socks5://user:pass@host:1080": "mixed",
		"http://user:pass@host:1080":   "mixed",
		"tg://proxy?server=host":       "mixed",
		"wireguard://unsupported":      "",
		"not-a-link":                   "",
	}
	for link, want := range tests {
		if got := deliveryProtocolForLink(link); got != want {
			t.Fatalf("deliveryProtocolForLink(%q) = %q, want %q", link, got, want)
		}
	}
}
