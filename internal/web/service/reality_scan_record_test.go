package service

import (
	"crypto/x509"
	"testing"
)

// cert builds a certificate whose DER blob is exactly derLen bytes. Only the
// length matters here: the estimator counts bytes, it never parses them.
func cert(derLen int) *x509.Certificate {
	return &x509.Certificate{Raw: make([]byte, derLen)}
}

func TestEstimateCertRecordBytesCountsChainAndStaple(t *testing.T) {
	// 5 header + 4 handshake + 1 context + 3 list
	// + (3 + 1000 + 2) + (3 + 900 + 2)
	// + 1 inner type + 16 tag
	got := estimateCertRecordBytes([]*x509.Certificate{cert(1000), cert(900)}, nil)
	want := 5 + 4 + 1 + 3 + (3 + 1000 + 2) + (3 + 900 + 2) + 1 + 16
	if got != want {
		t.Fatalf("chain without staple: got %d, want %d", got, want)
	}
}

func TestEstimateCertRecordBytesIncludesOCSPStaple(t *testing.T) {
	// OCSP stapling is what pushes real-world targets past the old 8192 ceiling
	// (www.microsoft.com lands at 8273), so it has to be counted.
	bare := estimateCertRecordBytes([]*x509.Certificate{cert(1000)}, nil)
	stapled := estimateCertRecordBytes([]*x509.Certificate{cert(1000)}, make([]byte, 1500))
	if stapled-bare != 4+4+1500 {
		t.Fatalf("staple overhead: got %d, want %d", stapled-bare, 4+4+1500)
	}
}

func TestEstimateCertRecordBytesEmptyChain(t *testing.T) {
	if got := estimateCertRecordBytes(nil, nil); got != 0 {
		t.Fatalf("no certificates should estimate 0, got %d", got)
	}
}

func TestClassifyCertRecordThresholds(t *testing.T) {
	cases := []struct {
		name string
		size int
		want string
	}{
		{"unknown when nothing was measured", 0, "unknown"},
		{"a small chain is fine", 3000, "ok"},
		// The real www.microsoft.com figure from the upstream bug report: it clears
		// the RFC bound but breaks any node still running an unpatched REALITY.
		{"microsoft-sized record breaks unpatched nodes", 8273, "over-legacy-limit"},
		{"just under the legacy ceiling is still ok", legacyRealityCeiling, "ok"},
		{"at the RFC bound is still usable", maxTLSRecordBytes, "over-legacy-limit"},
		{"past the RFC bound cannot work at all", maxTLSRecordBytes + 1, "over-protocol-limit"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classifyCertRecord(testCase.size); got != testCase.want {
				t.Fatalf("classifyCertRecord(%d) = %q, want %q", testCase.size, got, testCase.want)
			}
		})
	}
}

func TestClassifyCertRecordHasNoUnreachableBand(t *testing.T) {
	// Every byte count must land in exactly one of four labels, and each label must
	// be reachable. An unreachable branch reads like a promise the code never keeps.
	seen := map[string]bool{}
	for _, size := range []int{0, 3000, legacyRealityCeiling + 1, maxTLSRecordBytes + 1} {
		seen[classifyCertRecord(size)] = true
	}
	for _, label := range []string{"unknown", "ok", "over-legacy-limit", "over-protocol-limit"} {
		if !seen[label] {
			t.Fatalf("label %q is never produced", label)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("expected exactly 4 labels, got %v", seen)
	}
}
