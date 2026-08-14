package service

import (
	"net"
	"regexp"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

type SubLinkProvider interface {
	SubLinksForSubId(host, subId string) ([]string, error)
	LinksForClient(host string, inbound *model.Inbound, email string) []string
	LinksForClientAtEndpoint(host string, inbound *model.Inbound, email, endpointHost string, endpointPort int) []string
	LinksForInbounds(host string, inbounds []*model.Inbound) []string
}

var endpointHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var ErrInvalidEndpointOverride = common.NewError("protocol, host and port must be provided together")

var registeredSubLinkProvider SubLinkProvider

func RegisterSubLinkProvider(p SubLinkProvider) {
	registeredSubLinkProvider = p
}

func (s *InboundService) GetSubLinks(host, subId string) ([]string, error) {
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	return registeredSubLinkProvider.SubLinksForSubId(host, subId)
}

func (s *InboundService) GetAllInboundLinks(host string, userId int) ([]string, error) {
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	inbounds, err := s.GetInbounds(userId)
	if err != nil {
		return nil, err
	}
	return registeredSubLinkProvider.LinksForInbounds(host, inbounds), nil
}

func (s *InboundService) GetAllClientLinks(host string, email string) ([]string, error) {
	if email == "" {
		return nil, common.NewError("client email is required")
	}
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	rec, err := s.clientService.GetRecordByEmail(nil, email)
	if err != nil {
		return nil, err
	}
	inboundIds, err := s.clientService.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		return nil, err
	}
	var links []string
	for _, ibId := range inboundIds {
		inbound, getErr := s.GetInbound(ibId)
		if getErr != nil {
			return nil, getErr
		}
		links = append(links, registeredSubLinkProvider.LinksForClient(host, inbound, email)...)
	}
	return links, nil
}

func (s *InboundService) GetClientLinksAtEndpoint(host, email, protocol, endpointHost string, endpointPort int) ([]string, error) {
	if email == "" {
		return nil, common.NewError("client email is required")
	}
	if registeredSubLinkProvider == nil {
		return nil, common.NewError("sub link provider not registered")
	}
	protocol = strings.TrimSpace(protocol)
	if protocol == "hysteria2" {
		protocol = "hysteria"
	}
	switch protocol {
	case "vless", "vmess", "mixed", "shadowsocks", "hysteria":
	default:
		return nil, common.NewError("unsupported delivery protocol")
	}
	endpointHost = strings.TrimSpace(endpointHost)
	ipHost := strings.Trim(endpointHost, "[]")
	if endpointHost == "" ||
		(net.ParseIP(ipHost) == nil && !endpointHostnamePattern.MatchString(endpointHost)) {
		return nil, common.NewError("invalid delivery endpoint host")
	}
	if endpointPort < 1 || endpointPort > 65535 {
		return nil, common.NewError("invalid delivery endpoint port")
	}

	rec, err := s.clientService.GetRecordByEmail(nil, email)
	if err != nil {
		return nil, err
	}
	inboundIDs, err := s.clientService.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		return nil, err
	}
	var links []string
	for _, inboundID := range inboundIDs {
		inbound, getErr := s.GetInbound(inboundID)
		if getErr != nil {
			return nil, getErr
		}
		if string(inbound.Protocol) != protocol {
			continue
		}
		links = append(
			links,
			registeredSubLinkProvider.LinksForClientAtEndpoint(
				host,
				inbound,
				email,
				endpointHost,
				endpointPort,
			)...,
		)
	}
	if len(links) == 0 {
		return nil, common.NewError("client is not attached to the requested protocol")
	}
	return links, nil
}
