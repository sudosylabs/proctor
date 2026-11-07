// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// This file turns typed presentation details into the shared message layout:
// an optional emphasised hero detail and an ordered list of labelled facts.
package mail

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/sudosylabs/proctor/server/model"
)

// messageKind is the quiet line above every heading. Security notices are
// visually distinguished.
type messageKind string

const (
	kindSecurity   messageKind = "security"
	kindAccount    messageKind = "account"
	kindInvitation messageKind = "invitation"
	kindClasses    messageKind = "classes"
	kindAccess     messageKind = "access"
	kindExams      messageKind = "exams"
	kindSystem     messageKind = "system"
)

func kindFor(key model.MailTemplateKey) messageKind {
	switch key {
	case model.MailTemplateIdentityVerifyEmail, model.MailTemplateIdentityEmailChangeVerifyNew,
		model.MailTemplateIdentityPasswordReset, model.MailTemplateIdentityAccountDisabled,
		model.MailTemplateIdentityAccountEnabled, model.MailTemplateIdentityEmailVerifiedByAdmin:
		return kindAccount
	case model.MailTemplateSystemTest:
		return kindSystem
	}
	switch {
	case strings.HasPrefix(string(key), "identity."):
		return kindSecurity
	case strings.HasPrefix(string(key), "access."):
		return kindInvitation
	case strings.HasPrefix(string(key), "academic."):
		return kindClasses
	case strings.HasPrefix(string(key), "authorization."):
		return kindAccess
	default:
		return kindExams
	}
}

// heroProperties is the one detail a message sets large, such as an exam time
// or a receipt code.
type heroProperties struct {
	Label     string
	Primary   string
	Secondary string
	Code      bool
	Struck    bool
}

// factProperties is one labelled row in the message's detail list.
type factProperties struct {
	Label  string
	Value  string
	Code   bool
	Struck bool
}

// clock formats instants for people in one display location. Mail copy is
// English-canonical; date patterns follow it.
type clock struct{ location *time.Location }

func (c clock) in(t time.Time) time.Time { return t.In(c.location) }

// numericOffset writes a UTC offset with minutes when they matter, such as
// UTC+1, UTC+5:45 or UTC-0:30.
func numericOffset(offset int) string {
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	hours, minutes := offset/3600, (offset%3600)/60
	if minutes == 0 {
		return fmt.Sprintf("UTC%s%d", sign, hours)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, hours, minutes)
}

// namedZone reports the zone abbreviation when tzdata has a real one; many
// zones are named only by a numeric offset such as "+0545".
func namedZone(name string) bool { return name != "" && unicode.IsLetter(rune(name[0])) }

// zone labels a sitting window: the abbreviation with its offset, such as
// "WAT (UTC+1)", or the offset alone.
func (c clock) zone(t time.Time) string {
	name, offset := c.in(t).Zone()
	switch {
	case offset == 0 && (!namedZone(name) || name == "UTC"):
		return "UTC"
	case !namedZone(name):
		return numericOffset(offset)
	case offset == 0:
		return name
	default:
		return name + " (" + numericOffset(offset) + ")"
	}
}

// shortZone labels a single instant: the abbreviation, or the offset alone.
func (c clock) shortZone(t time.Time) string {
	name, offset := c.in(t).Zone()
	switch {
	case offset == 0 && (!namedZone(name) || name == "UTC"):
		return "UTC"
	case !namedZone(name):
		return numericOffset(offset)
	default:
		return name
	}
}

func (c clock) date(t time.Time) string { return c.in(t).Format("Monday 2 January 2006") }

func (c clock) dateTime(t time.Time) string {
	return c.in(t).Format("Monday 2 January 2006, 15:04") + " " + c.shortZone(t)
}

func (c clock) dateTimeSeconds(t time.Time) string {
	return c.in(t).Format("Monday 2 January 2006, 15:04:05") + " " + c.shortZone(t)
}

// timeRange formats a sitting window. Windows that cross midnight repeat the
// end date.
// A window across a daylight-saving change labels each end with its own zone.
func (c clock) timeRange(start, end time.Time) string {
	s, e := c.in(start), c.in(end)
	endTime := e.Format("15:04")
	if s.YearDay() != e.YearDay() || s.Year() != e.Year() {
		endTime = c.date(end) + ", " + endTime
	}
	_, startOffset := s.Zone()
	_, endOffset := e.Zone()
	if startOffset != endOffset {
		return s.Format("15:04") + " " + c.shortZone(start) + " – " + endTime + " " + c.shortZone(end)
	}
	return s.Format("15:04") + " – " + endTime + " " + c.zone(start)
}

func (c clock) day(t time.Time) string { return c.in(t).Format("Mon 2 Jan") }
func (c clock) whenAt(t time.Time) string {
	return c.in(t).Format("Mon 2 Jan") + " at " + c.in(t).Format("15:04") + " " + c.shortZone(t)
}

// layout holds everything the shared template needs besides the copy.
type layout struct {
	Hero  *heroProperties
	Facts []factProperties
}

func (l *layout) fact(label, value string) {
	l.Facts = append(l.Facts, factProperties{Label: label, Value: value})
}

// layoutBuilder produces the hero and facts once the copy is resolved.
type layoutBuilder func(templateCopy, *copyResolver) layout

// buildLayout validates the presentation detail against the template key and
// returns the family's layout builder. It also completes copy data that
// subjects and previews interpolate.
func buildLayout(request renderRequest, c clock, data *copyData) (layoutBuilder, error) {
	if err := validatePresentation(request); err != nil {
		return nil, err
	}
	switch {
	case request.PersonalAccessToken != nil:
		return personalAccessTokenLayout(request, c, data), nil
	case request.ExamManager != nil:
		return examManagerLayout(request.ExamManager, c, data), nil
	case request.SittingSchedule != nil:
		return sittingLayout(request, c, data), nil
	case request.ClassTransition != nil:
		return classTransitionLayout(request.ClassTransition, c, data), nil
	case request.SubmissionReceipt != nil:
		return submissionReceiptLayout(request, c, data), nil
	case request.EmailChange != nil:
		return emailChangeLayout(request, c), nil
	case request.Retention != nil:
		return retentionLayout(request.Retention, c, data), nil
	case request.ResultRelease != nil:
		return resultReleaseLayout(request.ResultRelease, c, data), nil
	default:
		return baseLayout(request, c), nil
	}
}

func personalAccessTokenLayout(request renderRequest, c clock, data *copyData) layoutBuilder {
	pat := request.PersonalAccessToken
	data.Token = strings.TrimSpace(pat.Description)
	return func(copy templateCopy, _ *copyResolver) layout {
		var l layout
		labels := copy.labels
		scope := labels.InstitutionScope
		if pat.AcademicUnitScoped {
			scope = labels.AcademicUnitScope
		}
		l.fact(labels.Token, data.Token)
		l.fact(labels.Access, scope)
		l.fact(labels.Expires, c.date(pat.ExpiresAt))
		l.fact(patActionLabel(request.Key, labels), c.dateTime(pat.ActionAt))
		return l
	}
}

func examManagerLayout(manager *ExamManagerDetails, c clock, data *copyData) layoutBuilder {
	data.ExamTitle = strings.TrimSpace(manager.Title)
	return func(copy templateCopy, _ *copyResolver) layout {
		var l layout
		labels := copy.labels
		role := map[string]string{"manager": labels.Manager, "owner": labels.Owner, "no_longer_manager": labels.NoLongerManager}[manager.Relationship]
		l.fact(labels.Exam, data.ExamTitle)
		l.fact(labels.YourRole, role)
		l.fact(labels.Since, c.dateTime(manager.ActionAt))
		return l
	}
}

func sittingLayout(request renderRequest, c clock, data *copyData) layoutBuilder {
	sitting := request.SittingSchedule
	data.ExamTitle, data.Class = strings.TrimSpace(sitting.ExamTitle), strings.TrimSpace(sitting.ClassDisplayName)
	data.When, data.Day = c.whenAt(sitting.StartsAt), c.day(sitting.StartsAt)
	struck := request.Key == model.MailTemplateExamSittingCanceled || request.Key == model.MailTemplateExamSittingAssignmentRemoved
	return func(copy templateCopy, r *copyResolver) layout {
		labels := copy.labels
		minutes := int(sitting.EndsAt.Sub(sitting.StartsAt).Round(time.Minute) / time.Minute)
		hero := &heroProperties{Label: labels.When, Primary: c.date(sitting.StartsAt),
			Secondary: c.timeRange(sitting.StartsAt, sitting.EndsAt) + " · " + r.duration(minutes/60, minutes%60), Struck: struck}
		if struck {
			hero.Label = labels.WasScheduled
		}
		l := layout{Hero: hero}
		l.fact(labels.Exam, data.ExamTitle)
		l.fact(labels.Class, data.Class)
		if prior := strings.TrimSpace(sitting.PriorClassDisplayName); prior != "" && request.Key == model.MailTemplateExamSittingRescheduled {
			l.fact(labels.PreviousClass, prior)
		}
		return l
	}
}

func classTransitionLayout(transition *ClassTransitionDetails, c clock, data *copyData) layoutBuilder {
	data.Class = strings.TrimSpace(transition.ClassDisplayName)
	data.PreviousClass = strings.TrimSpace(transition.PreviousClassDisplayName)
	return func(copy templateCopy, _ *copyResolver) layout {
		var l layout
		labels := copy.labels
		if data.PreviousClass != "" {
			l.fact(labels.FromClass, data.PreviousClass)
			l.fact(labels.ToClass, data.Class)
		} else {
			l.fact(labels.Class, data.Class)
		}
		l.fact(labels.EnrolledFrom, c.dateTime(transition.StartsAt))
		until := labels.NoEnd
		if !transition.EndsAt.IsZero() {
			until = c.dateTime(transition.EndsAt)
		}
		l.fact(labels.EnrolledUntil, until)
		return l
	}
}

func submissionReceiptLayout(request renderRequest, c clock, data *copyData) layoutBuilder {
	receipt := request.SubmissionReceipt
	data.ExamTitle = strings.TrimSpace(receipt.ExamTitle)
	return func(copy templateCopy, _ *copyResolver) layout {
		labels := copy.labels
		l := layout{Hero: &heroProperties{Label: labels.ReceiptCode, Primary: receiptCodeDisplay(receipt), Code: true}}
		l.fact(labels.Exam, data.ExamTitle)
		sealedLabel := labels.Submitted
		switch request.Key {
		case model.MailTemplateExamSubmissionManagerEnded:
			sealedLabel = labels.Ended
		case model.MailTemplateExamSubmissionAutomaticallySealed:
			sealedLabel = labels.Sealed
		}
		l.fact(sealedLabel, c.dateTimeSeconds(receipt.SealedAt))
		return l
	}
}

func emailChangeLayout(request renderRequest, c clock) layoutBuilder {
	masked := MaskAddress(strings.TrimSpace(request.EmailChange.NewAddress))
	return func(copy templateCopy, _ *copyResolver) layout {
		var l layout
		l.fact(copy.addressLabel, strings.TrimSpace(request.RecipientAddress))
		l.fact(copy.labels.NewAddress, masked)
		if copy.atLabel != "" && !request.At.IsZero() {
			l.fact(copy.atLabel, c.dateTime(request.At))
		}
		return l
	}
}

func retentionLayout(retention *RetentionDetails, c clock, data *copyData) layoutBuilder {
	data.Day = c.date(retention.RetireAfter)
	return func(copy templateCopy, _ *copyResolver) layout {
		var l layout
		l.fact(copy.labels.DeletionDate, data.Day)
		return l
	}
}

func resultReleaseLayout(result *ResultReleaseDetails, c clock, data *copyData) layoutBuilder {
	data.ExamTitle = strings.TrimSpace(result.ExamTitle)
	return func(copy templateCopy, _ *copyResolver) layout {
		var l layout
		l.fact(copy.labels.Exam, data.ExamTitle)
		l.fact(copy.labels.Released, c.dateTime(result.ReleasedAt))
		return l
	}
}

// baseLayout shows the recipient address, occurrence time and expiry for the
// templates whose copy labels them.
func baseLayout(request renderRequest, c clock) layoutBuilder {
	return func(copy templateCopy, _ *copyResolver) layout {
		var l layout
		if copy.addressLabel != "" && strings.TrimSpace(request.RecipientAddress) != "" {
			l.fact(copy.addressLabel, strings.TrimSpace(request.RecipientAddress))
		}
		if copy.atLabel != "" && !request.At.IsZero() {
			l.fact(copy.atLabel, c.dateTime(request.At))
		}
		if copy.expiresLabel != "" && !request.Deadline.IsZero() {
			l.fact(copy.expiresLabel, c.dateTime(request.Deadline))
		}
		return l
	}
}

func patActionLabel(key model.MailTemplateKey, labels commonLabels) string {
	switch key {
	case model.MailTemplateIdentityPersonalAccessTokenDisabled:
		return labels.PATDisabled
	case model.MailTemplateIdentityPersonalAccessTokenEnabled:
		return labels.PATEnabled
	case model.MailTemplateIdentityPersonalAccessTokenRevoked:
		return labels.PATRevoked
	default:
		return labels.PATCreated
	}
}

// validatePresentation requires exactly the detail family the template key
// declares, and that the detail is complete and bounded.
func validatePresentation(request renderRequest) error {
	checks := []struct {
		declared bool
		present  bool
		valid    func() bool
		name     string
	}{
		{isPersonalAccessTokenTemplate(request.Key), request.PersonalAccessToken != nil,
			func() bool { return validPersonalAccessTokenDetails(request.PersonalAccessToken) }, "PAT"},
		{isExamManagerTemplate(request.Key), request.ExamManager != nil,
			func() bool { return validExamManagerDetails(request.ExamManager) }, "Exam Manager"},
		{isSittingScheduleTemplate(request.Key), request.SittingSchedule != nil,
			func() bool { return validSittingScheduleDetails(request.SittingSchedule) }, "Sitting schedule"},
		{isClassTransitionTemplate(request.Key), request.ClassTransition != nil,
			func() bool { return validClassTransitionDetails(request.Key, request.ClassTransition) }, "Class transition"},
		{isSubmissionReceiptTemplate(request.Key), request.SubmissionReceipt != nil,
			func() bool { return validSubmissionReceiptDetails(request.SubmissionReceipt) }, "Submission receipt"},
		{request.Key == model.MailTemplateExamResultReleased, request.ResultRelease != nil,
			func() bool {
				return !request.ResultRelease.ReleasedAt.IsZero() && validBoundedMailLabel(request.ResultRelease.ExamTitle)
			}, "result release"},
		{request.Key == model.MailTemplateIdentityEmailChangeWarningOld, request.EmailChange != nil,
			func() bool { return model.IsValidEmail(strings.TrimSpace(request.EmailChange.NewAddress)) }, "email change"},
		{request.Key == model.MailTemplateExamRetentionScheduled, request.Retention != nil,
			func() bool { return !request.Retention.RetireAfter.IsZero() }, "retention"},
	}
	for _, check := range checks {
		if check.declared != check.present || check.present && !check.valid() {
			return fmt.Errorf("mail template %q has invalid %s details", request.Key, check.name)
		}
	}
	return nil
}

// receiptCodeDisplay is replaced by the Submission receipt code once it is
// stored. RECEIPT_CODE_PENDING
func receiptCodeDisplay(receipt *SubmissionReceiptDetails) string {
	return receipt.SubmissionID.String()
}
