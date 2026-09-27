package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net"
	"strconv"
	"time"

	"github.com/quic-go/quic-go"
)

type IngressProbeRequest struct {
	Address    string `json:"address" binding:"required,ip" example:"203.0.113.10"`
	Port       int    `json:"port" binding:"required,min=1,max=65535" example:"443"`
	Transport  string `json:"transport" binding:"required,oneof=TCP TLS QUIC" example:"TLS"`
	ServerName string `json:"serverName" example:"entry.example.com"`
}

type IngressProbeResult struct {
	Reachable              bool   `json:"reachable" example:"true"`
	ErrorCode              string `json:"errorCode" example:""`
	CertificateFingerprint string `json:"certificateFingerprint" example:""`
	CertificateNotAfter    string `json:"certificateNotAfter" example:"2027-01-01T00:00:00Z"`
}

// ProbeIngress checks the supplied IP without resolving it again; TLS and QUIC require trusted certificates.
// This proves transport reachability, not customer authentication or application traffic.
func ProbeIngress(parent context.Context, input IngressProbeRequest) IngressProbeResult {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	failed := IngressProbeResult{ErrorCode: "INGRESS_TRANSPORT_UNREACHABLE"}
	address := net.JoinHostPort(input.Address, strconv.Itoa(input.Port))
	if input.Transport == "TCP" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err != nil {
			return failed
		}
		_ = conn.Close()
		return IngressProbeResult{Reachable: true}
	}
	if input.ServerName == "" {
		return IngressProbeResult{ErrorCode: "INGRESS_TLS_NAME_REQUIRED"}
	}
	config := &tls.Config{ServerName: input.ServerName, MinVersion: tls.VersionTLS12}
	var state tls.ConnectionState
	if input.Transport == "TLS" {
		conn, err := (&tls.Dialer{Config: config}).DialContext(ctx, "tcp", address)
		if err != nil {
			return failed
		}
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			_ = conn.Close()
			return failed
		}
		state = tlsConn.ConnectionState()
		_ = conn.Close()
	} else if input.Transport == "QUIC" {
		config.MinVersion = tls.VersionTLS13
		config.NextProtos = []string{"h3"}
		conn, err := quic.DialAddr(ctx, address, config, &quic.Config{HandshakeIdleTimeout: 5 * time.Second, MaxIdleTimeout: 5 * time.Second})
		if err != nil {
			return failed
		}
		state = conn.ConnectionState().TLS
		_ = conn.CloseWithError(0, "probe complete")
	} else {
		return IngressProbeResult{ErrorCode: "INGRESS_TRANSPORT_UNSUPPORTED"}
	}
	if len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
		return failed
	}
	cert := state.PeerCertificates[0]
	fingerprint := sha256.Sum256(cert.Raw)
	return IngressProbeResult{Reachable: true, CertificateFingerprint: hex.EncodeToString(fingerprint[:]), CertificateNotAfter: cert.NotAfter.UTC().Format(time.RFC3339)}
}
