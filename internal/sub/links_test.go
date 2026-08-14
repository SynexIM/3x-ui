package sub

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestSplitLinkLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single_line", "vless://abc", []string{"vless://abc"}},
		{"two_lines", "vless://abc\nvmess://xyz", []string{"vless://abc", "vmess://xyz"}},
		{"trims_each_line", "  vless://abc  \n\tvmess://xyz\t", []string{"vless://abc", "vmess://xyz"}},
		{"skips_blank_lines", "vless://abc\n\n\nvmess://xyz\n", []string{"vless://abc", "vmess://xyz"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitLinkLines(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("splitLinkLines(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestGetLinkAtEndpointUsesRequestedCustomerIngressWithoutMutatingInbound(t *testing.T) {
	inbound := &model.Inbound{
		Id:                17,
		Protocol:          model.VLESS,
		Port:              443,
		ShareAddrStrategy: "node",
		Settings: `{"decryption":"none","clients":[{
			"id":"11111111-2222-4333-8444-555555555555",
			"email":"line@example.test"
		}]}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	service := &SubService{address: "panel.internal"}

	link := service.GetLinkAtEndpoint(
		inbound,
		"line@example.test",
		"vless.customer.example",
		18443,
	)

	if !strings.Contains(link, "@vless.customer.example:18443") {
		t.Fatalf("link did not use requested customer ingress: %q", link)
	}
	if inbound.Port != 443 || inbound.ShareAddrStrategy != "node" || inbound.ShareAddr != "" {
		t.Fatalf("endpoint generation mutated stored inbound: %#v", inbound)
	}
}

func TestSplitLinkLines_EmptyInputIsNil(t *testing.T) {
	if got := splitLinkLines(""); got != nil {
		t.Fatalf("splitLinkLines(\"\") = %#v, want nil", got)
	}
}

func TestSplitLinkLines_WhitespaceOnlyHasNoEntries(t *testing.T) {
	got := splitLinkLines("   \n\t  \n")
	if len(got) != 0 {
		t.Fatalf("splitLinkLines(whitespace) = %#v, want empty slice", got)
	}
}
