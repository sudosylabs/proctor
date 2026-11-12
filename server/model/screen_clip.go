// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on screen clips. A request is refused rather than trimmed.
const (
	// ScreenClipManagerRequestLimit and ScreenClipIncidentRequestLimit bound
	// each kind of request per Attempt, across every Participation.
	ScreenClipManagerRequestLimit  = 10
	ScreenClipIncidentRequestLimit = 10
	// ScreenClipMaxBytes bounds one clip, and ScreenClipAttemptMaxBytes all of
	// an Attempt's clips together.
	ScreenClipMaxBytes        int64 = 3 << 30
	ScreenClipAttemptMaxBytes int64 = 6 << 30
	// ScreenClipMaxRange bounds an explicit range. A range from the start of
	// the session is bounded by the session itself.
	ScreenClipMaxRange = 6 * time.Hour
	// ScreenClipIncidentMargin is how much an incident clip records before and
	// after the moment it is about.
	ScreenClipIncidentMargin = time.Minute
	// ScreenClipPrivateReasonMaximumRunes bounds a manager's reason.
	ScreenClipPrivateReasonMaximumRunes = 1000
	// ScreenClipChunkBytes is the size of every chunk but the last, which
	// holds the rest. Chunks are stored as they arrive and never joined, so a
	// byte range maps straight onto them.
	ScreenClipChunkBytes int64 = 8 << 20
)

// ErrScreenClipChunkInvalid means a chunk's bytes were not its declared size
// or did not match its declared hash. Nothing of it is kept.
var ErrScreenClipChunkInvalid = errors.New("screen clip chunk is invalid")

// screenClipMediaTypes are the containers Desktop may upload a clip in.
var screenClipMediaTypes = []string{"video/mp4", "video/webm"}

// ScreenClipUpload is what Desktop declares before uploading a clip: its
// media type, its exact size, and the SHA-256 of every chunk in order. Each
// chunk is checked against its hash as it arrives, so a clip is whole once
// every chunk is in.
type ScreenClipUpload struct {
	MediaType   string
	SizeBytes   int64
	ChunkSHA256 []string
}

// Validate checks the declaration is bounded and has one hash per chunk.
func (u *ScreenClipUpload) Validate() error {
	if u == nil || !slices.Contains(screenClipMediaTypes, u.MediaType) ||
		u.SizeBytes < 1 || u.SizeBytes > ScreenClipMaxBytes || len(u.ChunkSHA256) != u.ChunkCount() {
		return invalidModelError("ScreenClipUpload.Validate", "screen_clip", "upload", "is invalid", "")
	}
	for _, chunk := range u.ChunkSHA256 {
		if !isLowerHexSHA256(chunk) {
			return invalidModelError("ScreenClipUpload.Validate", "screen_clip", "chunk_sha256", "is invalid", "")
		}
	}
	return nil
}

// ChunkCount is how many chunks the clip has.
func (u *ScreenClipUpload) ChunkCount() int {
	return int((u.SizeBytes + ScreenClipChunkBytes - 1) / ScreenClipChunkBytes)
}

// ChunkSize is the size of chunk index, or 0 for an index outside the clip.
func (u *ScreenClipUpload) ChunkSize(index int) int64 {
	count := u.ChunkCount()
	switch {
	case index < 0 || index >= count:
		return 0
	case index < count-1:
		return ScreenClipChunkBytes
	default:
		return u.SizeBytes - int64(count-1)*ScreenClipChunkBytes
	}
}

// Digest is the clip's content digest: the SHA-256 of its manifest, the media
// type, size, chunk size and chunk hashes in order, one per line. It binds
// every byte of the clip without reading it again.
func (u *ScreenClipUpload) Digest() string {
	var manifest strings.Builder
	fmt.Fprintf(&manifest, "proctor-screen-clip-v1\n%s\n%d\n%d\n", u.MediaType, u.SizeBytes, ScreenClipChunkBytes)
	for _, chunk := range u.ChunkSHA256 {
		manifest.WriteString(chunk)
		manifest.WriteString("\n")
	}
	sum := sha256.Sum256([]byte(manifest.String()))
	return hex.EncodeToString(sum[:])
}

// Clone isolates the chunk list.
func (u *ScreenClipUpload) Clone() *ScreenClipUpload {
	if u == nil {
		return nil
	}
	clone := *u
	clone.ChunkSHA256 = slices.Clone(u.ChunkSHA256)
	return &clone
}

func isLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ScreenClipSource is why a clip was requested.
type ScreenClipSource string

const (
	// ScreenClipSourceManager is an Exam Manager's request, with a private
	// reason only managers see.
	ScreenClipSourceManager ScreenClipSource = "manager"
	// ScreenClipSourceIncident is the server's own request around the moment
	// an Integrity Flag opened, linked to that Flag.
	ScreenClipSourceIncident ScreenClipSource = "incident"
)

// ScreenClipState is where a clip is in its life.
type ScreenClipState string

const (
	// ScreenClipRequested waits for Desktop to take the request. While the
	// Attempt has no current Participation it stays here, queued.
	ScreenClipRequested ScreenClipState = "requested"
	// ScreenClipRecording means Desktop has the request and is waiting for
	// the part of the range still in the future.
	ScreenClipRecording ScreenClipState = "recording"
	// ScreenClipUploading means Desktop is uploading the cut clip.
	ScreenClipUploading ScreenClipState = "uploading"
	// ScreenClipReady means the clip is stored and can be played.
	ScreenClipReady ScreenClipState = "ready"
	// ScreenClipFailed is final, with a reason.
	ScreenClipFailed ScreenClipState = "failed"
)

// ScreenClipFailure says why a clip failed.
type ScreenClipFailure string

const (
	// ScreenClipFailureAttemptEnded: the Attempt ended before Desktop took or
	// finished the request, and its local recording is deleted.
	ScreenClipFailureAttemptEnded ScreenClipFailure = "attempt_ended"
	// ScreenClipFailureNotRecorded: Desktop holds no recording for the range.
	ScreenClipFailureNotRecorded ScreenClipFailure = "not_recorded"
	// ScreenClipFailureUploadDeadline: the upload did not finish in time.
	ScreenClipFailureUploadDeadline ScreenClipFailure = "upload_deadline"
	// ScreenClipFailureRejected: the uploaded clip failed verification.
	ScreenClipFailureRejected ScreenClipFailure = "rejected"
)

// ScreenClipRange is the recorded time a clip covers. FromSessionStart means
// from the moment the candidate window opened; Start is then zero. End may
// still be in the future when requested.
type ScreenClipRange struct {
	FromSessionStart bool
	Start            time.Time
	End              time.Time
}

// Validate checks the range is whole and bounded.
func (r ScreenClipRange) Validate() error {
	switch {
	case r.End.IsZero():
	case r.FromSessionStart && r.Start.IsZero():
		return nil
	case !r.FromSessionStart && !r.Start.IsZero() && r.Start.Before(r.End) && r.End.Sub(r.Start) <= ScreenClipMaxRange:
		return nil
	}
	return invalidModelError("ScreenClipRange.Validate", "screen_clip", "range", "is invalid", "")
}

// ScreenClip is one request for a range of a candidate's recorded screen and
// what became of it.
type ScreenClip struct {
	ID        ScreenClipID
	AttemptID ExamAttemptID
	Source    ScreenClipSource
	// RequestedByUserID and PrivateReason are set for a manager's request. The
	// reason is manager-only: it never enters audit data, logs or realtime.
	RequestedByUserID UserID
	PrivateReason     string
	// FlagID is set for an incident request.
	FlagID      IntegrityFlagID
	Range       ScreenClipRange
	State       ScreenClipState
	Failure     ScreenClipFailure // set only when State is failed
	RequestedAt time.Time
	// DeliveredParticipationID is the Participation whose Desktop took the
	// request.
	DeliveredParticipationID AttemptParticipationID
	// Upload is Desktop's declaration, set once it starts uploading.
	Upload    *ScreenClipUpload
	UpdatedAt time.Time
	Revision  int64
}

// NewManagerScreenClip records an Exam Manager's request.
func NewManagerScreenClip(
	id ScreenClipID,
	attemptID ExamAttemptID,
	managerID UserID,
	privateReason string,
	clipRange ScreenClipRange,
	at time.Time,
) (*ScreenClip, error) {
	return newScreenClip(ScreenClip{
		ID: id, AttemptID: attemptID, Source: ScreenClipSourceManager,
		RequestedByUserID: managerID, PrivateReason: privateReason, Range: clipRange,
	}, at)
}

func newScreenClip(clip ScreenClip, at time.Time) (*ScreenClip, error) {
	at = TimeUTC(at)
	clip.Range.Start, clip.Range.End = timeUTCOrZero(clip.Range.Start), timeUTCOrZero(clip.Range.End)
	clip.State, clip.RequestedAt, clip.UpdatedAt, clip.Revision = ScreenClipRequested, at, at, 1
	if err := clip.Validate(); err != nil {
		return nil, err
	}
	return &clip, nil
}

// Validate checks the clip's invariants.
func (c *ScreenClip) Validate() error {
	if c == nil || !c.ID.IsValid() || !c.AttemptID.IsValid() || c.Range.Validate() != nil ||
		c.RequestedAt.IsZero() || c.UpdatedAt.Before(c.RequestedAt) || c.Revision < 1 ||
		!c.validSource() || !c.validState() {
		return invalidModelError("ScreenClip.Validate", "screen_clip", "", "invalid screen clip", "")
	}
	return nil
}

func (c *ScreenClip) validSource() bool {
	switch c.Source {
	case ScreenClipSourceManager:
		return c.RequestedByUserID.IsValid() && validScreenClipReason(c.PrivateReason) && c.FlagID == ""
	case ScreenClipSourceIncident:
		return c.FlagID.IsValid() && c.RequestedByUserID == "" && c.PrivateReason == "" && !c.Range.FromSessionStart
	default:
		return false
	}
}

func (c *ScreenClip) validState() bool {
	delivered := c.DeliveredParticipationID.IsValid()
	switch c.State {
	case ScreenClipRequested:
		return !delivered && c.Failure == "" && c.Upload == nil
	case ScreenClipRecording:
		return delivered && c.Failure == "" && c.Upload == nil
	case ScreenClipUploading, ScreenClipReady:
		return delivered && c.Failure == "" && c.Upload.Validate() == nil
	case ScreenClipFailed:
		return c.Failure.IsValid() && (delivered || c.Failure == ScreenClipFailureAttemptEnded)
	default:
		return false
	}
}

// IsValid reports whether the failure is a known one.
func (f ScreenClipFailure) IsValid() bool {
	switch f {
	case ScreenClipFailureAttemptEnded, ScreenClipFailureNotRecorded, ScreenClipFailureUploadDeadline, ScreenClipFailureRejected:
		return true
	default:
		return false
	}
}

// IsFinal reports whether the clip can no longer change.
func (s ScreenClipState) IsFinal() bool { return s == ScreenClipReady || s == ScreenClipFailed }

// Deliver records that the Desktop of participationID took the request. It
// is the only way out of requested apart from failure.
func (c *ScreenClip) Deliver(participationID AttemptParticipationID, at time.Time) error {
	if c.State != ScreenClipRequested || !participationID.IsValid() {
		return invalidModelError("ScreenClip.Deliver", "screen_clip", "state", "is not requested", "")
	}
	return c.advance(func(next *ScreenClip) {
		next.State, next.DeliveredParticipationID = ScreenClipRecording, participationID
	}, at)
}

// BeginUpload records Desktop's declaration and starts the upload.
func (c *ScreenClip) BeginUpload(upload *ScreenClipUpload, at time.Time) error {
	if c.State != ScreenClipRecording || upload.Validate() != nil {
		return invalidModelError("ScreenClip.BeginUpload", "screen_clip", "state", "is not recording", "")
	}
	return c.advance(func(next *ScreenClip) { next.State, next.Upload = ScreenClipUploading, upload.Clone() }, at)
}

// Complete makes an uploaded clip ready to play. The caller has checked
// every chunk arrived.
func (c *ScreenClip) Complete(at time.Time) error {
	if c.State != ScreenClipUploading {
		return invalidModelError("ScreenClip.Complete", "screen_clip", "state", "is not uploading", "")
	}
	return c.advance(func(next *ScreenClip) { next.State = ScreenClipReady }, at)
}

// Fail ends the clip with a reason. A final clip cannot fail.
func (c *ScreenClip) Fail(failure ScreenClipFailure, at time.Time) error {
	if c.State.IsFinal() || !failure.IsValid() {
		return invalidModelError("ScreenClip.Fail", "screen_clip", "state", "is final", "")
	}
	return c.advance(func(next *ScreenClip) { next.State, next.Failure = ScreenClipFailed, failure }, at)
}

func (c *ScreenClip) advance(change func(*ScreenClip), at time.Time) error {
	next := *c
	change(&next)
	next.Revision++
	next.UpdatedAt = TimeUTC(at)
	if next.UpdatedAt.Before(c.UpdatedAt) {
		next.UpdatedAt = c.UpdatedAt
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*c = next
	return nil
}

// validScreenClipReason holds a manager's reason to the rule a manager's
// suspension reason follows: trimmed, 1 to 1000 characters.
func validScreenClipReason(reason string) bool {
	return utf8.ValidString(reason) &&
		reason == strings.TrimSpace(reason) &&
		utf8.RuneCountInString(reason) >= 1 &&
		utf8.RuneCountInString(reason) <= ScreenClipPrivateReasonMaximumRunes &&
		len(reason) <= 4*ScreenClipPrivateReasonMaximumRunes
}

func timeUTCOrZero(at time.Time) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	return TimeUTC(at)
}
