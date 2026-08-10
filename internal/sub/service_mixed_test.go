package sub

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestGenMixedLinkFields(t *testing.T) {
	inbound := &model.Inbound{
		Listen:   "2001:db8::1",
		Port:     1080,
		Protocol: model.Mixed,
		Settings: `{"auth":"password","clients":[{"email":"alice@example.test","password":"p@ss word","enable":true}]}`,
	}
	s := &SubService{}
	lines := strings.Split(s.genMixedLink(inbound, "alice@example.test"), "\n")
	if len(lines) != 3 {
		t.Fatalf("Mixed links = %v, want SOCKS5, HTTP, and Telegram", lines)
	}
	for i, scheme := range []string{"socks5", "http", "https"} {
		parsed, err := url.Parse(lines[i])
		if err != nil {
			t.Fatalf("link %d does not parse: %v", i, err)
		}
		if parsed.Scheme != scheme {
			t.Fatalf("link %d scheme = %q, want %q", i, parsed.Scheme, scheme)
		}
	}
	if !strings.Contains(lines[0], "alice%40example.test:p%40ss%20word@[2001:db8::1]:1080") {
		t.Fatalf("SOCKS5 credentials or IPv6 authority are not encoded: %q", lines[0])
	}
}

func TestGetInboundsBySubIdIncludesMixed(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()
	inbound := &model.Inbound{
		Port:     1080,
		Protocol: model.Mixed,
		Enable:   true,
		Tag:      "mixed-sub",
		Settings: `{"auth":"password","clients":[{"email":"u@mixed","password":"secret","enable":true,"subId":"submixed"}]}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("create Mixed inbound: %v", err)
	}
	record := &model.ClientRecord{
		Email: "u@mixed", SubID: "submixed", Password: "secret", Enable: true,
	}
	if err := db.Create(record).Error; err != nil {
		t.Fatalf("create Mixed client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: record.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatalf("create Mixed client link: %v", err)
	}

	s := &SubService{}
	links, emails, _, _, err := s.GetSubs("submixed", "sub.example.test")
	if err != nil {
		t.Fatalf("GetSubs: %v", err)
	}
	if len(links) != 1 || len(splitLinkLines(links[0])) != 3 ||
		len(emails) != 1 || emails[0] != "u@mixed" {
		t.Fatalf("Mixed subscription missing links: links=%v emails=%v", links, emails)
	}
}
