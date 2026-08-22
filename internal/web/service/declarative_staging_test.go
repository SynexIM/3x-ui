package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// chunk splits a document the way a control plane would: fixed-size pieces, each
// comfortably under the 10 MiB request body cap.
func chunk(raw []byte, size int) [][]byte {
	pieces := make([][]byte, 0, len(raw)/size+1)
	for start := 0; start < len(raw); start += size {
		end := min(start+size, len(raw))
		pieces = append(pieces, raw[start:end])
	}
	return pieces
}

func TestStagedUploadReassemblesTheDocumentByteForByte(t *testing.T) {
	document := []byte(strings.Repeat("declarative-", 5_000))
	area := &declarativeStagingArea{uploads: map[string]*stagedUpload{}}

	for seq, piece := range chunk(document, 1024) {
		if _, next, err := area.Stage("upload-1", seq, piece); err != nil {
			t.Fatalf("chunk %d: %v", seq, err)
		} else if next != seq+1 {
			t.Fatalf("chunk %d: next = %d, want %d", seq, next, seq+1)
		}
	}

	assembled, err := area.Take("upload-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(assembled) != string(document) {
		t.Fatalf("reassembled %d bytes, want %d", len(assembled), len(document))
	}
	if area.stagedBytes() != 0 {
		t.Fatalf("a committed upload must be released; still holding %d bytes", area.stagedBytes())
	}
	if _, err := area.Take("upload-1"); !errors.Is(err, ErrStagedUploadNotFound) {
		t.Fatalf("an upload can only be taken once; got %v", err)
	}
}

func TestStagedUploadRefusesWhatItCannotAssemble(t *testing.T) {
	area := &declarativeStagingArea{uploads: map[string]*stagedUpload{}}

	t.Run("a resumed upload the panel never saw", func(t *testing.T) {
		// Chunk 3 with nothing before it means the panel restarted, or the
		// upload expired. There is nothing to append to and no way to tell.
		if _, _, err := area.Stage("ghost", 3, []byte("x")); !errors.Is(err, ErrStagedUploadNotFound) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("out of order", func(t *testing.T) {
		if _, _, err := area.Stage("upload-2", 0, []byte("aaaa")); err != nil {
			t.Fatal(err)
		}
		staged, next, err := area.Stage("upload-2", 5, []byte("bbbb"))
		if err == nil {
			t.Fatal("a chunk that would leave a hole must be refused")
		}
		if next != 1 {
			t.Fatalf("the refusal must say which chunk is wanted; got %d", next)
		}
		if staged != 4 {
			t.Fatalf("the refused chunk must not have been appended; staged = %d", staged)
		}
	})

	t.Run("no upload id", func(t *testing.T) {
		if _, _, err := area.Stage("", 0, []byte("x")); err == nil {
			t.Fatal("an upload with no id cannot be committed later")
		}
	})
}

// A control plane that dies mid-upload leaves its bytes behind. Without the
// expiry they would be held until the panel restarts.
func TestStagedUploadExpiresWhenTheUploaderDisappears(t *testing.T) {
	previous := stagedUploadTTL
	stagedUploadTTL = 50 * time.Millisecond
	t.Cleanup(func() { stagedUploadTTL = previous })

	area := &declarativeStagingArea{uploads: map[string]*stagedUpload{}}
	if _, _, err := area.Stage("half-uploaded", 0, []byte(strings.Repeat("x", 4096))); err != nil {
		t.Fatal(err)
	}
	if area.stagedBytes() != 4096 {
		t.Fatalf("staged = %d, want 4096", area.stagedBytes())
	}

	deadline := time.Now().Add(2 * time.Second)
	for area.stagedBytes() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if area.stagedBytes() != 0 {
		t.Fatalf("an abandoned upload must be released; still holding %d bytes", area.stagedBytes())
	}
	if _, err := area.Take("half-uploaded"); !errors.Is(err, ErrStagedUploadNotFound) {
		t.Fatalf("got %v", err)
	}

	// And a fresh chunk keeps the upload alive rather than letting the first
	// timer kill it mid-upload.
	if _, _, err := area.Stage("slow", 0, []byte("a")); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		time.Sleep(20 * time.Millisecond)
		if _, _, err := area.Stage("slow", area.uploads["slow"].nextSeq, []byte("a")); err != nil {
			t.Fatalf("a live upload must not be expired out from under its uploader: %v", err)
		}
	}
}

// wholeNodeProjection is what a node holding `lines` dedicated lines looks like
// on the wire: one account per line, a client for it in every inbound, its own
// egress and its own routing rule.
func wholeNodeProjection(lines int) DeclarativeApplyRequest {
	protocols := []string{"vless", "vmess", "trojan", "shadowsocks", "mixed"}
	config := DeclarativeNodeConfig{NodeBandwidthBps: 480_000_000}
	for index, protocol := range protocols {
		config.Inbounds = append(config.Inbounds, DeclarativeInbound{
			Tag:            fmt.Sprintf("line-%s", protocol),
			Protocol:       protocol,
			ListenPort:     30800 + index,
			ShareAddr:      DeclarativeShareAddress{Strategy: "custom", Host: "entry.line.invalid", Port: 30800 + index},
			Settings:       map[string]any{},
			StreamSettings: map[string]any{"network": "tcp"},
			Clients:        make([]DeclarativeClient, 0, lines),
		})
	}
	for line := range lines {
		email := fmt.Sprintf("line-%06d@line.invalid", line)
		password := fmt.Sprintf("secret-%06d-%036d", line, line)
		client := DeclarativeClient{
			Email:    email,
			UUID:     fmt.Sprintf("%08d-1111-2222-3333-%012d", line, line),
			Password: &password,
			PirBps:   100_000_000,
			CirBps:   20_000_000,
			CbsBytes: 50_000_000,
		}
		for i := range config.Inbounds {
			config.Inbounds[i].Clients = append(config.Inbounds[i].Clients, client)
		}
		tag := fmt.Sprintf("egress-%06d", line)
		config.Outbounds = append(config.Outbounds, DeclarativeOutbound{
			Tag: tag, Protocol: "socks",
			Server: DeclarativeSocksServer{Host: fmt.Sprintf("10.%d.%d.%d", line>>16&0xff, line>>8&0xff, line&0xff), Port: 1080},
		})
		config.Routing.Rules = append(config.Routing.Rules, DeclarativeRule{AccountEmail: email, OutboundTag: tag})
	}
	return DeclarativeApplyRequest{Revision: 1, Config: config}
}

// The size this feature exists for: 50k lines across five inbounds is 250k
// clients, which is tens of megabytes — many times the 10 MiB request body cap
// in web.go, and the full apply is the path taken when a delta has already been
// refused.
func TestAFullNodeProjectionTooBigForOneRequestSurvivesTheChunkedPath(t *testing.T) {
	const lines = 50_000
	const chunkSize = 8 << 20 // comfortably under web.go's 10 MiB body cap

	request := wholeNodeProjection(lines)
	document, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	expectedHash, err := hashDeclarativeConfig(request.Config)
	if err != nil {
		t.Fatal(err)
	}
	pieces := chunk(document, chunkSize)
	t.Logf("%d lines x 5 inbounds = %d clients; %.1f MiB in %d chunks of %d MiB",
		lines, lines*5, float64(len(document))/(1<<20), len(pieces), chunkSize>>20)
	if len(document) <= 10<<20 {
		t.Fatalf("this projection is only %d bytes; it would have fit in one request and proves nothing", len(document))
	}

	svc := &DeclarativeProvisioningService{}
	started := time.Now()
	for seq, piece := range pieces {
		if _, err := svc.StageChunk("full-node", seq, piece); err != nil {
			t.Fatalf("chunk %d: %v", seq, err)
		}
	}
	staged := time.Since(started)

	assembleStarted := time.Now()
	raw, err := declarativeStaging.Take("full-node")
	if err != nil {
		t.Fatal(err)
	}
	assembled, err := assembleStagedApply(raw, expectedHash)
	if err != nil {
		t.Fatalf("the staged upload must assemble back into the apply it came from: %v", err)
	}
	t.Logf("staged in %s, assembled and hashed in %s", staged.Round(time.Millisecond), time.Since(assembleStarted).Round(time.Millisecond))

	counts := countsFor(assembled.Config)
	if counts.Inbounds != 5 || counts.Clients != lines || counts.Outbounds != lines || counts.Rules != lines {
		t.Fatalf("the reassembled config is not the one that was sent: %#v", counts)
	}
	if err := validateDeclarativeRequest(assembled); err != nil {
		t.Fatalf("the reassembled config must pass the ordinary validation: %v", err)
	}
}

func TestAssembleStagedApplyRefusesWhatTheControlPlaneDidNotMean(t *testing.T) {
	request := wholeNodeProjection(3)
	document, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hashDeclarativeConfig(request.Config)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a hash that does not match", func(t *testing.T) {
		_, err := assembleStagedApply(document, "1111111111111111111111111111111111111111111111111111111111111111")
		if err == nil || !strings.Contains(err.Error(), hash) {
			t.Fatalf("the refusal must name what was actually staged; got %v", err)
		}
	})

	t.Run("a truncated upload", func(t *testing.T) {
		if _, err := assembleStagedApply(document[:len(document)/2], hash); err == nil {
			t.Fatal("half a document must not be applied")
		}
	})

	t.Run("no expected hash", func(t *testing.T) {
		if _, err := assembleStagedApply(document, ""); err == nil {
			t.Fatal("committing without an expected hash applies whatever happened to arrive")
		}
	})
}

// A commit that is refused must leave the node exactly where it was, and must
// not leave the upload behind either.
func TestCommitStagedLeavesTheAppliedStateAloneWhenTheHashIsWrong(t *testing.T) {
	setupConflictDB(t)
	appliedHash := seedDeclarativeState(t, deltaBaseConfig(), 7)

	request := wholeNodeProjection(2)
	document, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	svc := &DeclarativeProvisioningService{}
	for seq, piece := range chunk(document, 4096) {
		if _, err := svc.StageChunk("wrong-hash", seq, piece); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := svc.CommitStaged("wrong-hash", "2222222222222222222222222222222222222222222222222222222222222222"); err == nil {
		t.Fatal("a commit whose content does not match expectedHash must be refused")
	}

	state, err := svc.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Hash != appliedHash || state.Request.Revision != 7 {
		t.Fatalf("a refused commit must not move the applied state; now %s / %d", state.Hash, state.Request.Revision)
	}
	if _, err := svc.CommitStaged("wrong-hash", "whatever"); !errors.Is(err, ErrStagedUploadNotFound) {
		t.Fatalf("a commit consumes its upload whatever the outcome; got %v", err)
	}
}
