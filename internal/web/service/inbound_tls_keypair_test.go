package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// LibreSSL (macOS openssl) "req -newkey ec" writes explicit curve parameters,
// which Go rejects; Xray then serves no certificate and every HY2/TLS handshake
// fails with "unrecognized name". The save must refuse it instead.
const explicitParamsECCert = `-----BEGIN CERTIFICATE-----
MIICSTCCAe+gAwIBAgIJAIGCEF615+mOMAoGCCqGSM49BAMCMB4xHDAaBgNVBAMM
E2FjY2VwdC5pcGxpbmUubG9jYWwwHhcNMjYwOTI3MTIzMTMyWhcNMjcwOTI3MTIz
MTMyWjAeMRwwGgYDVQQDDBNhY2NlcHQuaXBsaW5lLmxvY2FsMIIBSzCCAQMGByqG
SM49AgEwgfcCAQEwLAYHKoZIzj0BAQIhAP////8AAAABAAAAAAAAAAAAAAAA////
////////////MFsEIP////8AAAABAAAAAAAAAAAAAAAA///////////////8BCBa
xjXYqjqT57PrvVV2mIa8ZR0GsMxTsPY7zjw+J9JgSwMVAMSdNgiG5wSTamZ44ROd
JreBn36QBEEEaxfR8uEsQkf4vOblY6RA8ncDfYEt6zOg9KE5RdiYwpZP40Li/hp/
m47n60p8D54WK84zV2sxXs7LtkBoN79R9QIhAP////8AAAAA//////////+85vqt
pxeehPO5ysL8YyVRAgEBA0IABHOGXVV+Dq2dW4uMJSS5ydZ8A9EjBMXNSjyIV1Xw
Z31lZjZcR01xQG2Ftqstb1BDFLbbmN14j7IetrguEe8e6JujIjAgMB4GA1UdEQQX
MBWCE2FjY2VwdC5pcGxpbmUubG9jYWwwCgYIKoZIzj0EAwIDSAAwRQIhAOebQ+li
M9GxJJxaVG4S2oI0eKUmrstS76eXordVGndPAiAxO3cubKHxGdkYVMMXyZ4p2MMl
glp6qpr548wjBnAFLw==
-----END CERTIFICATE-----`

const explicitParamsECKey = `-----BEGIN PRIVATE KEY-----
MIIBeQIBADCCAQMGByqGSM49AgEwgfcCAQEwLAYHKoZIzj0BAQIhAP////8AAAAB
AAAAAAAAAAAAAAAA////////////////MFsEIP////8AAAABAAAAAAAAAAAAAAAA
///////////////8BCBaxjXYqjqT57PrvVV2mIa8ZR0GsMxTsPY7zjw+J9JgSwMV
AMSdNgiG5wSTamZ44ROdJreBn36QBEEEaxfR8uEsQkf4vOblY6RA8ncDfYEt6zOg
9KE5RdiYwpZP40Li/hp/m47n60p8D54WK84zV2sxXs7LtkBoN79R9QIhAP////8A
AAAA//////////+85vqtpxeehPO5ysL8YyVRAgEBBG0wawIBAQQgLrr9QKk0cNON
UpA7veuHKqjiQrjvKKttVxG3oyFFuB6hRANCAARzhl1Vfg6tnVuLjCUkucnWfAPR
IwTFzUo8iFdV8Gd9ZWY2XEdNcUBthbarLW9QQxS225jdeI+yHra4LhHvHuib
-----END PRIVATE KEY-----`

func inlineTLSStream(t *testing.T, cert, key string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"security": "tls",
		"tlsSettings": map[string]any{"certificates": []any{map[string]any{
			"certificate": strings.Split(cert, "\n"), "key": strings.Split(key, "\n"),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestValidateInboundTLSCertificatesRejectsUnloadableKeyPair(t *testing.T) {
	if err := validateInboundTLSCertificates(inlineTLSStream(t, explicitParamsECCert, explicitParamsECKey)); err == nil {
		t.Fatal("explicit-parameter EC certificate was accepted; Xray would drop it and fail every handshake")
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "a.test"}, DNSNames: []string{"a.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cert := strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	key := strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})))
	if err := validateInboundTLSCertificates(inlineTLSStream(t, cert, key)); err != nil {
		t.Fatalf("named-curve P-256 certificate rejected: %v", err)
	}
}
