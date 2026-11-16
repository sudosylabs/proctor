// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// FrozenContent is a rendered message as it will be sent. Its digest is stored
// on the delivery, so a later template change cannot alter what was frozen.
type FrozenContent struct{ Subject, Text, HTML string }

// PersonalAccessTokenDetails are the facts a personal access token notice may
// show: the token's description and expiry, never its secret.
type PersonalAccessTokenDetails struct {
	Description        string
	ExpiresAt          time.Time
	ActionAt           time.Time
	ActionCount        int
	AcademicUnitScoped bool
}

// ExamManagerDetails tell an exam manager which exam and relationship
// changed.
type ExamManagerDetails struct {
	Title        string
	Relationship string
	ActionAt     time.Time
}

// ClassTransitionDetails name the classes and dates of an enrollment change;
// PreviousClassDisplayName is set only for a transfer.
type ClassTransitionDetails struct {
	PreviousClassDisplayName string
	ClassDisplayName         string
	StartsAt                 time.Time
	EndsAt                   time.Time
}

// SubmissionReceiptDetails carry the receipt a candidate can quote to prove
// what was sealed and when.
type SubmissionReceiptDetails struct {
	ExamTitle    string
	SittingID    model.ExamSittingID
	SubmissionID model.SubmissionID
	ReceiptCode  model.SubmissionReceiptCode
	SealedAt     time.Time
}

// ResultReleaseDetails announce a released result without containing it.
type ResultReleaseDetails struct {
	ExamTitle  string
	ReleasedAt time.Time
}

// EmailChangeDetails names the address an account moved to. Mail shows only
// its masked form to the previous address.
type EmailChangeDetails struct {
	NewAddress string
}

// RetentionDetails carries the planned retirement date of a retention
// notice.
type RetentionDetails struct {
	RetireAfter time.Time
}

// Presentation is the closed set of typed facts mail rendering accepts. Only
// this package can add a presentation family.
type Presentation interface{ mailPresentation() }

func (PersonalAccessTokenDetails) mailPresentation() {}
func (ExamManagerDetails) mailPresentation()         {}
func (ClassTransitionDetails) mailPresentation()     {}
func (SubmissionReceiptDetails) mailPresentation()   {}
func (ResultReleaseDetails) mailPresentation()       {}
func (EmailChangeDetails) mailPresentation()         {}
func (RetentionDetails) mailPresentation()           {}

// RenderRequest holds everything a template may show. Composers build it from
// frozen recipient facts, so rendering reads no mutable account state.
type RenderRequest struct {
	Key          model.MailTemplateKey
	Locale       string
	ActionURL    string
	Presentation Presentation
	// RecipientName is the frozen display name; the greeting uses its first
	// word. Empty renders an anonymous greeting.
	RecipientName string
	// RecipientAddress is shown only by templates that label it.
	RecipientAddress string
	// RecipientTimezone selects how times are written; empty or UTC uses the
	// installation display time zone.
	RecipientTimezone string
	// Institution is the institution display name at preparation time.
	Institution string
	// At and Deadline are the occurrence time and delivery or link deadline.
	At       time.Time
	Deadline time.Time
}

// Digest is the SHA-256 of subject, text and HTML separated by NUL bytes,
// stored as a delivery's template digest.
func Digest(subject, text, html string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(subject))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(text))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(html))
	return hex.EncodeToString(hash.Sum(nil))
}
