// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"fmt"
	"strings"

	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

// templateCopy is the localized human copy of one message. Every field is
// plain text; the templates escape it for their context.
type templateCopy struct {
	Subject        string
	Preheader      string
	Institution    string
	Kind           string
	Heading        string
	Greeting       string
	Paragraphs     []string
	ActionLabel    string
	ActionNote     string
	ActionFallback string
	// ActionFallbackLink is the short link text after ActionFallback; the HTML
	// links it to the action URL and the text alternative prints the URL.
	ActionFallbackLink string
	AsideTitle         string
	AsideText          string
	Why                string
	Support            string
	SentBy             string

	addressLabel string
	atLabel      string
	expiresLabel string
	labels       commonLabels
}

// commonLabels are the shared fact labels and closed values used by the
// presentation families.
type commonLabels struct {
	Exam, Class, PreviousClass, FromClass, ToClass, When, WasScheduled        string
	EnrolledFrom, EnrolledUntil, NoEnd, YourRole, Since, Token, Access        string
	Expires, Released, Submitted, Ended, Sealed, ReceiptCode                  string
	PATCreated, PATDisabled, PATEnabled, PATRevoked, NewAddress, DeletionDate string
	InstitutionScope, AcademicUnitScope, Manager, Owner, NoLongerManager      string
}

// copyData is the closed set of values localized copy may interpolate.
type copyData struct {
	Name          string
	Institution   string
	Address       string
	ExamTitle     string
	Class         string
	PreviousClass string
	When          string
	Day           string
	Token         string
	Deadline      string
	Hours         string
	Minutes       string
}

// representativeCopyData fills every placeholder so startup validation proves
// each catalog entry renders.
var representativeCopyData = copyData{
	Name: "Representative", Institution: "Representative institution", Address: "support@example.test",
	ExamTitle: "Representative exam", Class: "Representative class", PreviousClass: "Previous class",
	When: "Wed 1 Sep at 08:30", Day: "Wed 1 Sep", Token: "Representative token",
	Deadline: "Wednesday 1 September 2026, 08:30 UTC", Hours: "2 hours", Minutes: "30 minutes",
}

type copyResolver struct {
	localizer      *localization.Localizer
	key            model.MailTemplateKey
	locale         string
	data           copyData
	resolvedLocale string
	failure        error
}

func resolveCopy(localizer *localization.Localizer, key model.MailTemplateKey, locale string, data copyData) (templateCopy, *copyResolver, string, error) {
	r := &copyResolver{localizer: localizer, key: key, locale: locale, data: data}
	institution := strings.TrimSpace(data.Institution)
	if strings.TrimSpace(r.data.Institution) == "" {
		r.data.Institution = r.common("institution_fallback", nil)
	}
	copy := templateCopy{
		Subject: r.required("subject"), Preheader: r.required("preheader"), Institution: institution,
		Kind: r.common("kind."+string(kindFor(key)), nil), Heading: r.required("heading"),
		Paragraphs:  splitParagraphs(r.required("body")),
		ActionLabel: r.optional("action_label"), ActionNote: r.optional("action_note"),
		ActionFallback: r.common("action_fallback", nil), ActionFallbackLink: r.common("action_fallback_link", nil),
		AsideTitle: r.optional("aside_title"), AsideText: r.optional("aside_text"),
		Why:          r.required("why"),
		SentBy:       r.common("sent_by", r.data),
		addressLabel: r.optional("address_label"), atLabel: r.optional("at_label"), expiresLabel: r.optional("expires_label"),
	}
	if (copy.AsideTitle == "") != (copy.AsideText == "") {
		return templateCopy{}, nil, "", fmt.Errorf("resolve mail copy %q: aside title and text must both be present", key)
	}
	if strings.TrimSpace(data.Name) == "" {
		copy.Greeting = r.common("greeting_anonymous", nil)
	} else {
		copy.Greeting = r.common("greeting", r.data)
	}
	if strings.TrimSpace(data.Address) == "" {
		copy.Support = r.common("support_fallback", r.data)
	} else {
		copy.Support = r.common("support", r.data)
	}
	copy.labels = commonLabels{
		Exam: r.label("exam"), Class: r.label("class"), PreviousClass: r.label("previous_class"),
		FromClass: r.label("from_class"), ToClass: r.label("to_class"), When: r.label("when"),
		WasScheduled: r.label("was_scheduled"), EnrolledFrom: r.label("enrolled_from"),
		EnrolledUntil: r.label("enrolled_until"), NoEnd: r.label("no_end"), YourRole: r.label("your_role"),
		Since: r.label("since"), Token: r.label("token"), Access: r.label("access"), Expires: r.label("expires"),
		Released: r.label("released"), Submitted: r.label("submitted"), Ended: r.label("ended"),
		Sealed: r.label("sealed"), ReceiptCode: r.label("receipt_code"),
		PATCreated: r.label("pat_created"), PATDisabled: r.label("pat_disabled"),
		PATEnabled: r.label("pat_enabled"), PATRevoked: r.label("pat_revoked"),
		NewAddress: r.label("new_address"), DeletionDate: r.label("deletion_date"),
		InstitutionScope: r.value("institution_scope"), AcademicUnitScope: r.value("academic_unit_scope"),
		Manager: r.value("manager"), Owner: r.value("owner"), NoLongerManager: r.value("no_longer_manager"),
	}
	if r.failure != nil {
		return templateCopy{}, nil, "", fmt.Errorf("resolve mail copy %q: %w", key, r.failure)
	}
	return copy, r, r.resolvedLocale, nil
}

// duration localizes a whole-minute duration as hours, minutes, or both.
func (r *copyResolver) duration(hours, minutes int) string {
	hoursText, minutesText := r.plural("value.duration_hours", hours), r.plural("value.duration_minutes", minutes)
	switch {
	case minutes == 0:
		return hoursText
	case hours == 0:
		return minutesText
	default:
		return r.common("value.duration_join", copyData{Hours: hoursText, Minutes: minutesText})
	}
}

func (r *copyResolver) plural(field string, count int) string {
	value, err := r.localizer.ResolveRequest(r.locale, localization.Request{ID: "mail.common." + field, PluralCount: count})
	if err != nil {
		r.fail(err)
		return ""
	}
	return value.Text
}

func splitParagraphs(body string) []string {
	var paragraphs []string
	for _, paragraph := range strings.Split(body, "\n\n") {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			paragraphs = append(paragraphs, paragraph)
		}
	}
	return paragraphs
}

func (r *copyResolver) required(field string) string {
	value, err := r.localizer.Resolve(r.locale, localizationID(r.key, field), r.data)
	if err != nil {
		r.fail(err)
		return ""
	}
	if r.resolvedLocale == "" {
		r.resolvedLocale = value.Locale
	}
	return value.Text
}

func (r *copyResolver) optional(field string) string {
	if !hasCopyField(r.key, field) {
		return ""
	}
	value, err := r.localizer.Resolve(r.locale, localizationID(r.key, field), r.data)
	if err != nil {
		r.fail(err)
		return ""
	}
	return value.Text
}

func (r *copyResolver) common(field string, data any) string {
	value, err := r.localizer.Resolve(r.locale, "mail.common."+field, data)
	if err != nil {
		r.fail(err)
		return ""
	}
	return value.Text
}

func (r *copyResolver) label(field string) string { return r.common("label."+field, nil) }
func (r *copyResolver) value(field string) string { return r.common("value."+field, nil) }

func (r *copyResolver) fail(err error) {
	if r.failure == nil {
		r.failure = err
	}
}
