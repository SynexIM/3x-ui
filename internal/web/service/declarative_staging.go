package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// A full apply carries a whole node in one body; web.go caps that at 10 MiB.
// Staging is transport only — the bytes go through the same Apply either way.
var (
	ErrStagedUploadNotFound = errors.New("no staged upload with that id; it was never started, already committed, or expired")
	ErrStagedUploadTooLarge = errors.New("staged upload exceeds the maximum size")
)

// How long an upload survives without a new chunk: a control plane that dies
// mid-upload would otherwise hold its bytes until the panel restarts.
var stagedUploadTTL = 10 * time.Minute

// Well above a real full-node projection (50k lines weigh ~61 MiB) and still a
// bound on what one caller can make the panel hold.
const maxStagedUploadBytes = 256 << 20

type stagedUpload struct {
	buffer  []byte
	nextSeq int
	expiry  *time.Timer
}

type declarativeStagingArea struct {
	mu      sync.Mutex
	uploads map[string]*stagedUpload
}

var declarativeStaging = &declarativeStagingArea{uploads: map[string]*stagedUpload{}}

// Stage appends one chunk. Chunks arrive in order from 0; the returned next
// sequence is how a caller whose response was lost learns whether its chunk landed.
func (a *declarativeStagingArea) Stage(uploadID string, seq int, chunk []byte) (staged int, nextSeq int, err error) {
	if uploadID == "" {
		return 0, 0, errors.New("uploadId is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	upload := a.uploads[uploadID]
	if upload == nil {
		// Chunk 3 with nothing before it: the panel restarted or the upload
		// expired. There is nothing to append to and no way to tell which.
		if seq != 0 {
			return 0, 0, fmt.Errorf("%w (chunk %d)", ErrStagedUploadNotFound, seq)
		}
		upload = &stagedUpload{}
		a.uploads[uploadID] = upload
	}
	if seq != upload.nextSeq {
		return len(upload.buffer), upload.nextSeq,
			fmt.Errorf("chunk %d is out of order; this upload is waiting for chunk %d", seq, upload.nextSeq)
	}
	if len(upload.buffer)+len(chunk) > maxStagedUploadBytes {
		a.discardLocked(uploadID)
		return 0, 0, ErrStagedUploadTooLarge
	}
	upload.buffer = append(upload.buffer, chunk...)
	upload.nextSeq++

	if upload.expiry != nil {
		upload.expiry.Stop()
	}
	upload.expiry = time.AfterFunc(stagedUploadTTL, func() { a.Discard(uploadID) })

	return len(upload.buffer), upload.nextSeq, nil
}

// Take removes the upload and returns everything staged under it. A commit
// consumes its upload either way; holding bytes already known wrong helps nobody.
func (a *declarativeStagingArea) Take(uploadID string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	upload := a.uploads[uploadID]
	if upload == nil {
		return nil, ErrStagedUploadNotFound
	}
	a.discardLocked(uploadID)
	return upload.buffer, nil
}

// Discard drops an upload, abandoned by its caller or aged out.
func (a *declarativeStagingArea) Discard(uploadID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.discardLocked(uploadID)
}

func (a *declarativeStagingArea) discardLocked(uploadID string) {
	if upload := a.uploads[uploadID]; upload != nil && upload.expiry != nil {
		upload.expiry.Stop()
	}
	delete(a.uploads, uploadID)
}

// stagedBytes is what this area holds across all uploads.
func (a *declarativeStagingArea) stagedBytes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	total := 0
	for _, upload := range a.uploads {
		total += len(upload.buffer)
	}
	return total
}

// DeclarativeStageReceipt says what the panel holds and which chunk it wants next.
type DeclarativeStageReceipt struct {
	UploadID    string `json:"uploadId"`
	StagedBytes int    `json:"stagedBytes"`
	NextSeq     int    `json:"nextSeq"`
}

// StageChunk buffers one piece of a full apply request body.
func (s *DeclarativeProvisioningService) StageChunk(uploadID string, seq int, chunk []byte) (*DeclarativeStageReceipt, error) {
	staged, next, err := declarativeStaging.Stage(uploadID, seq, chunk)
	if err != nil {
		return nil, err
	}
	return &DeclarativeStageReceipt{UploadID: uploadID, StagedBytes: staged, NextSeq: next}, nil
}

// AbortStagedUpload frees an abandoned upload without waiting for it to expire.
func (s *DeclarativeProvisioningService) AbortStagedUpload(uploadID string) {
	declarativeStaging.Discard(uploadID)
}

// CommitStaged applies the staged bytes as one whole-node configuration.
// expectedHash is checked first, so a truncated upload is refused, not applied.
func (s *DeclarativeProvisioningService) CommitStaged(uploadID, expectedHash string) (*DeclarativeApplyReceipt, error) {
	raw, err := declarativeStaging.Take(uploadID)
	if err != nil {
		return nil, err
	}
	request, err := assembleStagedApply(raw, expectedHash)
	if err != nil {
		return nil, err
	}
	return s.Apply(request)
}

// assembleStagedApply turns staged bytes into the apply request they describe.
func assembleStagedApply(raw []byte, expectedHash string) (*DeclarativeApplyRequest, error) {
	if expectedHash == "" {
		return nil, errors.New("expectedHash is required")
	}
	request := &DeclarativeApplyRequest{}
	if err := json.Unmarshal(raw, request); err != nil {
		return nil, fmt.Errorf("the staged upload is not a complete apply request (%d bytes): %w", len(raw), err)
	}
	hash, err := hashDeclarativeConfig(request.Config)
	if err != nil {
		return nil, err
	}
	if hash != expectedHash {
		return nil, fmt.Errorf("the staged configuration hashes to %s but %s was expected; the upload is not the configuration the control plane meant to send", hash, expectedHash)
	}
	return request, nil
}
