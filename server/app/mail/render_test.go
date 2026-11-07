// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

func testRenderer() (*templateRenderer, error) {
	return testRendererWithPolicy(RendererPolicy{})
}

func testRendererWithPolicy(policy RendererPolicy) (*templateRenderer, error) {
	localizer, err := localization.New(os.DirFS("../../i18n"), localization.EnglishLocale)
	if err != nil {
		return nil, err
	}
	renderer, err := newRenderer(templateRendererDependencies{Files: os.DirFS("../../templates"), Localizer: localizer, ValidateCompleteCatalog: true})
	if err != nil {
		return nil, err
	}
	return renderer, renderer.applyPolicy(policy)
}

func TestRendererEscapesBoundedPersonalAccessTokenDetailsWithoutScopes(t *testing.T) {
	t.Parallel()

	renderer, err := testRenderer()
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{
		Key: model.MailTemplateIdentityPersonalAccessTokenCreated,
		PersonalAccessToken: &PersonalAccessTokenDetails{
			Description:        `<script>automation & reports</script>`,
			ExpiresAt:          time.Date(2026, 9, 20, 9, 30, 0, 0, time.UTC),
			ActionAt:           time.Date(2026, 8, 20, 8, 15, 0, 0, time.UTC),
			ActionCount:        2,
			AcademicUnitScoped: true,
		},
	})
	require.NoError(t, err)
	for _, want := range []string{
		"&lt;script&gt;automation &amp; reports&lt;/script&gt;",
		"Sunday 20 September 2026",
		"Thursday 20 August 2026, 08:15 UTC",
		"Limited to one academic unit",
	} {
		assert.Contains(t, message.HTML, want)
	}
	require.Contains(t, message.Text, `<script>automation & reports</script>`, "text alternative changed the plain token description")
	for _, forbidden := range []string{"class.view", "role.manage", "raw-secret-value", "token_hash", "action count"} {
		require.NotContains(t, strings.ToLower(message.HTML), forbidden)
		require.NotContains(t, strings.ToLower(message.Text), forbidden)
	}
}

func TestRendererEscapesBoundedExamManagerDetails(t *testing.T) {
	t.Parallel()

	renderer, err := testRenderer()
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{
		Key: model.MailTemplateExamOwnershipTransferredFromYou,
		ExamManager: &ExamManagerDetails{
			Title:        `<script>Algorithms & data</script>`,
			Relationship: "manager",
			ActionAt:     time.Date(2026, 8, 20, 8, 15, 0, 0, time.UTC),
		},
	})
	require.NoError(t, err)
	for _, want := range []string{
		"&lt;script&gt;Algorithms &amp; data&lt;/script&gt;",
		">Manager<",
		"Thursday 20 August 2026, 08:15 UTC",
	} {
		assert.Contains(t, message.HTML, want)
	}
	require.Contains(t, message.Text, `<script>Algorithms & data</script>`, "text alternative changed the plain Exam title")
	for _, forbidden := range []string{"actor-user", "private-reason", "role.manage"} {
		require.NotContains(t, message.HTML, forbidden)
		require.NotContains(t, message.Text, forbidden)
	}
}

func TestRendererWritesSittingTimesInTheDisplayTimezone(t *testing.T) {
	t.Parallel()
	renderer, err := testRendererWithPolicy(RendererPolicy{DisplayTimezone: "Africa/Lagos"})
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{Key: model.MailTemplateExamSittingRescheduled, SittingSchedule: &SittingScheduleDetails{
		ExamTitle: "Algorithms & structures", ClassDisplayName: "CS 2A", PriorClassDisplayName: "CS 2B",
		StartsAt: time.Date(2026, 9, 1, 8, 30, 0, 0, time.FixedZone("node", 7200)),
		EndsAt:   time.Date(2026, 9, 1, 10, 0, 0, 0, time.FixedZone("node", 7200)),
	}})
	require.NoError(t, err)
	for _, want := range []string{"Algorithms &amp; structures", "CS 2A", "CS 2B", "Tuesday 1 September 2026",
		"07:30 – 09:00 WAT (UTC&#43;1)", "1 hour 30 minutes"} {
		assert.Contains(t, message.HTML, want)
	}
	assert.Equal(t, "Exam rescheduled: Algorithms & structures, now Tue 1 Sep at 07:30 WAT", message.Subject)
	for _, forbidden := range []string{"instructions", "resource", "answer", "candidate list", "T06:30:00Z"} {
		require.NotContains(t, strings.ToLower(message.HTML), strings.ToLower(forbidden))
		require.NotContains(t, strings.ToLower(message.Text), strings.ToLower(forbidden))
	}
}

func TestRendererStrikesThroughACanceledSitting(t *testing.T) {
	t.Parallel()
	renderer, err := testRenderer()
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{Key: model.MailTemplateExamSittingCanceled, SittingSchedule: &SittingScheduleDetails{
		ExamTitle: "Algorithms", ClassDisplayName: "CS 2A",
		StartsAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
	}})
	require.NoError(t, err)
	assert.Contains(t, message.HTML, "Was scheduled for")
	assert.Contains(t, message.HTML, `<s style="color:#77727F">Tuesday 1 September 2026</s>`)
}

func TestRendererEscapesCompleteClassTransitionFacts(t *testing.T) {
	t.Parallel()
	renderer, err := testRenderer()
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{Key: model.MailTemplateAcademicClassTransferred, ClassTransition: &ClassTransitionDetails{
		PreviousClassDisplayName: `Old <Class> & A`, ClassDisplayName: `New <Class> & B`,
		StartsAt: time.Date(2026, 9, 1, 8, 30, 0, 0, time.FixedZone("node", 7200)),
		EndsAt:   time.Date(2027, 6, 30, 16, 0, 0, 0, time.UTC),
	}})
	require.NoError(t, err)
	for _, want := range []string{"Old &lt;Class&gt; &amp; A", "New &lt;Class&gt; &amp; B", "Tuesday 1 September 2026, 06:30 UTC", "Wednesday 30 June 2027, 16:00 UTC"} {
		assert.Contains(t, message.HTML, want)
	}
	require.Contains(t, message.Text, `Old <Class> & A`, "text alternative changed Class display names")
	require.Contains(t, message.Text, `New <Class> & B`, "text alternative changed Class display names")
	for _, forbidden := range []string{"student list", "exam instructions", "actor-user", "private reason"} {
		require.NotContains(t, strings.ToLower(message.HTML), forbidden)
		require.NotContains(t, strings.ToLower(message.Text), forbidden)
	}
}

func TestRendererIncludesOnlySafeSubmissionReceiptFacts(t *testing.T) {
	t.Parallel()
	renderer, err := testRenderer()
	require.NoError(t, err)
	sittingID, submissionID := model.NewExamSittingID(), model.NewSubmissionID()
	for _, key := range []model.MailTemplateKey{model.MailTemplateExamSubmissionReceived,
		model.MailTemplateExamSubmissionManagerEnded, model.MailTemplateExamSubmissionAutomaticallySealed} {
		message, renderErr := renderer.render(renderRequest{Key: key, SubmissionReceipt: &SubmissionReceiptDetails{
			ExamTitle: "Algorithms <Final> & proofs", SittingID: sittingID,
			SubmissionID: submissionID, ReceiptCode: model.SubmissionReceiptCode("MBY3KBMNEB83TFW1"), SealedAt: time.Date(2026, 8, 21, 9, 30, 15, 0, time.FixedZone("node", 7200)),
		}})
		require.NoError(t, renderErr, "Render(%q)", key)
		for _, want := range []string{"Algorithms &lt;Final&gt; &amp; proofs", "Receipt code", "MBY3-KBMN-EB83-TFW1", "Friday 21 August 2026, 07:30:15 UTC"} {
			assert.Contains(t, message.HTML, want, "Render(%q) HTML does not contain", key)
		}
		require.NotContains(t, message.HTML, sittingID.String(), "Render(%q) exposed the Sitting ID", key)
		require.NotContains(t, message.HTML, submissionID.String(), "Render(%q) exposed the Submission ID", key)
		for _, forbidden := range []string{"manifest", "workspace", "answer", "integrity", "path/inside"} {
			require.NotContains(t, strings.ToLower(message.HTML), forbidden, "Render(%q) leaked forbidden content", key)
			require.NotContains(t, strings.ToLower(message.Text), forbidden, "Render(%q) leaked forbidden content", key)
		}
	}
}

func TestRendererIncludesOnlySafeReleasedResultFacts(t *testing.T) {
	t.Parallel()
	renderer, err := testRenderer()
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{Key: model.MailTemplateExamResultReleased, ResultRelease: &ResultReleaseDetails{
		ExamTitle:  "Algorithms <Final> & proofs",
		ReleasedAt: time.Date(2026, 8, 21, 9, 30, 0, 0, time.FixedZone("node", 7200)),
	}})
	require.NoError(t, err)
	for _, want := range []string{"Algorithms &lt;Final&gt; &amp; proofs", "Friday 21 August 2026, 07:30 UTC"} {
		assert.Contains(t, message.HTML, want)
	}
	for _, forbidden := range []string{"score", "outcome", "remark", "evidence", "submission", "workspace", "rationale"} {
		require.NotContains(t, strings.ToLower(message.HTML), forbidden)
		require.NotContains(t, strings.ToLower(message.Text), forbidden)
	}
}

func TestRendererGreetsTheRecipientByFirstName(t *testing.T) {
	t.Parallel()
	renderer, err := testRenderer()
	require.NoError(t, err)
	named, err := renderer.render(renderRequest{Key: model.MailTemplateIdentityPasswordChanged,
		RecipientName: "Adaeze <b>Okonkwo</b>", RecipientAddress: "adaeze@example.test", At: time.Date(2026, 11, 7, 9, 42, 0, 0, time.UTC)})
	require.NoError(t, err)
	assert.Contains(t, named.HTML, "Hello Adaeze,")
	assert.NotContains(t, named.HTML, "Okonkwo")
	assert.Contains(t, named.HTML, "adaeze@example.test")
	assert.Contains(t, named.HTML, "Saturday 7 November 2026, 09:42 UTC")

	anonymous, err := renderer.render(renderRequest{Key: model.MailTemplateIdentityPasswordChanged, At: time.Date(2026, 11, 7, 9, 42, 0, 0, time.UTC)})
	require.NoError(t, err)
	assert.Contains(t, anonymous.HTML, "Hello,")
}

func TestRendererPrefersTheRecipientTimezoneOverTheDisplayTimezone(t *testing.T) {
	t.Parallel()
	renderer, err := testRendererWithPolicy(RendererPolicy{DisplayTimezone: "Africa/Lagos"})
	require.NoError(t, err)
	at := time.Date(2026, 11, 7, 9, 42, 0, 0, time.UTC)
	for timezone, want := range map[string]string{
		"":                    "Saturday 7 November 2026, 10:42 WAT",
		model.DefaultTimezone: "Saturday 7 November 2026, 10:42 WAT",
		"Europe/London":       "Saturday 7 November 2026, 09:42 GMT",
		"Asia/Kolkata":        "Saturday 7 November 2026, 15:12 IST",
		"Not/AZone":           "Saturday 7 November 2026, 10:42 WAT",
	} {
		message, err := renderer.render(renderRequest{Key: model.MailTemplateIdentityPasswordChanged, RecipientTimezone: timezone, At: at})
		require.NoError(t, err)
		assert.Contains(t, message.HTML, want, "timezone %q", timezone)
	}
}

func TestRendererNamesTheInstitutionAndSupportAddress(t *testing.T) {
	t.Parallel()
	withSupport, err := testRendererWithPolicy(RendererPolicy{SupportAddress: "Help@Example.test"})
	require.NoError(t, err)
	message, err := withSupport.render(renderRequest{Key: model.MailTemplateIdentityAccountEnabled, Institution: "Lagoon City <University>"})
	require.NoError(t, err)
	for _, want := range []string{"Lagoon City &lt;University&gt;", "Questions? Contact support at help@example.test.",
		"Sent by Proctor for Lagoon City &lt;University&gt;."} {
		assert.Contains(t, message.HTML, want)
	}

	without, err := testRenderer()
	require.NoError(t, err)
	message, err = without.render(renderRequest{Key: model.MailTemplateIdentityAccountEnabled})
	require.NoError(t, err)
	assert.Contains(t, message.HTML, "Questions? Contact your institution.")
}

func TestRendererPolicyRejectsInvalidPresentationSettings(t *testing.T) {
	t.Parallel()
	_, err := testRendererWithPolicy(RendererPolicy{DisplayTimezone: "Mars/Olympus"})
	require.Error(t, err)
	_, err = testRendererWithPolicy(RendererPolicy{SupportAddress: "not an address"})
	require.Error(t, err)
}

func TestRendererPlainTextStartsWithTheHeading(t *testing.T) {
	t.Parallel()
	renderer, err := testRenderer()
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{Key: model.MailTemplateIdentityPasswordReset, RecipientName: "Adaeze",
		ActionURL: "https://proctor.example.test/reset#token=t", At: time.Date(2026, 11, 7, 9, 42, 0, 0, time.UTC),
		Deadline: time.Date(2026, 11, 7, 10, 42, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(message.Text, "Reset your password\n\nHello Adaeze,\n"), "text alternative starts with %q", message.Text[:min(60, len(message.Text))])
	assert.Contains(t, message.Text, "Choose a new password: https://proctor.example.test/reset#token=t")
	assert.Contains(t, message.Text, "This link works once, until Saturday 7 November 2026, 10:42 UTC.")
	assert.NotContains(t, message.Text, "\n\n\n")
	assert.Contains(t, message.HTML, `If the button doesn&#39;t work, open the link <a href="https://proctor.example.test/reset#token=t"`)
	assert.Contains(t, message.HTML, `>here</a>.`)
	assert.NotContains(t, message.HTML, ">https://proctor.example.test/reset#token=t<", "the HTML fallback printed the full URL")
}

func TestRendererContextuallyEscapesLocalizedCopyAndActionURL(t *testing.T) {
	t.Parallel()

	key := model.MailTemplateIdentityVerifyEmail
	source, err := os.ReadFile("../../i18n/en.json")
	require.NoError(t, err)
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(source, &entries))
	overrides := map[string]string{
		"mail.identity.verify_email.action_label": "Verify >",
		"mail.identity.verify_email.body":         "A & B",
		"mail.identity.verify_email.heading":      "<script>alert(1)</script>",
		"mail.identity.verify_email.subject":      "Verify <account>",
	}
	for _, entry := range entries {
		if value, ok := overrides[entry["id"].(string)]; ok {
			entry["translation"] = value
		}
	}
	catalog, err := json.Marshal(entries)
	require.NoError(t, err)
	localizer, err := localization.New(fstest.MapFS{"en.json": {Data: catalog}}, localization.EnglishLocale)
	require.NoError(t, err)
	renderer, err := newRenderer(templateRendererDependencies{Files: os.DirFS("../../templates"), Localizer: localizer, ValidateCompleteCatalog: false})
	require.NoError(t, err)

	message, err := renderer.render(renderRequest{
		Key:       key,
		ActionURL: "https://proctor.example.test/account/verify-email?one=1&two=2",
	})
	require.NoError(t, err)
	require.NotContains(t, message.HTML, "<script>alert(1)</script>", "HTML contains unescaped localized markup")
	for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "one=1&amp;two=2", "A &amp; B"} {
		assert.Contains(t, message.HTML, want)
	}
	require.Contains(t, message.Text, "<script>alert(1)</script>", "text alternative changed plain localized copy")
}

func TestRendererRejectsUnsafeActionURL(t *testing.T) {
	t.Parallel()

	renderer, err := testRenderer()
	require.NoError(t, err)
	_, err = renderer.render(renderRequest{
		Key:       model.MailTemplateIdentityVerifyEmail,
		ActionURL: "javascript:alert(1)",
	})
	require.Error(t, err, "Render accepted a non-HTTPS action URL")
}

func TestRendererLoopbackHTTPRequiresExplicitDevelopmentPolicy(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://localhost:8065/join#token=representative",
		"http://127.0.0.1:8065/join#token=representative",
		"http://[::1]:8065/join#token=representative",
	} {
		assert.Error(t, validateActionURL(raw, false), "validateActionURL(%q) accepted loopback HTTP without development policy", raw)
		assert.NoError(t, validateActionURL(raw, true), "validateActionURL(%q) rejected explicit loopback development URL", raw)
	}
	for _, raw := range []string{
		"http://192.0.2.1:8065/join#token=representative",
		"http://user@localhost:8065/join#token=representative",
	} {
		assert.Error(t, validateActionURL(raw, true), "validateActionURL(%q) accepted unsafe HTTP development URL", raw)
	}
}

func TestRendererParsesAndRendersEveryProductionTemplate(t *testing.T) {
	t.Parallel()

	renderer, err := testRenderer()
	require.NoError(t, err)
	for _, key := range model.AllMailTemplateKeys() {
		request := renderRequest{
			Key:             key,
			RecipientLocale: "zz-ZZ",
			RecipientName:   "Representative Recipient",
			ActionURL:       "https://proctor.example.test/action#token=representative",
			At:              time.Date(2026, 8, 20, 8, 15, 0, 0, time.UTC),
			Deadline:        time.Date(2026, 8, 27, 8, 15, 0, 0, time.UTC),
		}
		if isPersonalAccessTokenTemplate(key) {
			request.PersonalAccessToken = &PersonalAccessTokenDetails{
				Description: "Representative automation", ExpiresAt: time.Date(2026, 9, 20, 9, 30, 0, 0, time.UTC),
				ActionAt: time.Date(2026, 8, 20, 8, 15, 0, 0, time.UTC), ActionCount: 2,
			}
		}
		if isExamManagerTemplate(key) {
			request.ExamManager = &ExamManagerDetails{
				Title: "Representative exam", Relationship: "manager",
				ActionAt: time.Date(2026, 8, 20, 8, 15, 0, 0, time.UTC),
			}
		}
		if isSittingScheduleTemplate(key) {
			request.SittingSchedule = &SittingScheduleDetails{ExamTitle: "Representative exam", ClassDisplayName: "Class A",
				StartsAt: time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC), EndsAt: time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)}
		}
		if isClassTransitionTemplate(key) {
			request.ClassTransition = &ClassTransitionDetails{PreviousClassDisplayName: "Class A", ClassDisplayName: "Class B",
				StartsAt: time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC), EndsAt: time.Date(2027, 6, 30, 16, 0, 0, 0, time.UTC)}
			if key != model.MailTemplateAcademicClassTransferred {
				request.ClassTransition.PreviousClassDisplayName = ""
			}
		}
		if isSubmissionReceiptTemplate(key) {
			request.SubmissionReceipt = &SubmissionReceiptDetails{ExamTitle: "Representative exam",
				SittingID: model.NewExamSittingID(), SubmissionID: model.NewSubmissionID(),
				ReceiptCode: model.NewSubmissionReceiptCode(), SealedAt: time.Date(2026, 8, 21, 9, 30, 0, 0, time.UTC)}
		}
		if key == model.MailTemplateIdentityEmailChangeWarningOld {
			request.EmailChange = &EmailChangeDetails{NewAddress: "adaeze.okonkwo@gmail.com"}
		}
		if key == model.MailTemplateExamRetentionScheduled {
			request.Retention = &RetentionDetails{RetireAfter: time.Date(2027, 2, 7, 0, 0, 0, 0, time.UTC)}
		}
		if key == model.MailTemplateExamResultReleased {
			request.ResultRelease = &ResultReleaseDetails{ExamTitle: "Representative exam",
				ReleasedAt: time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)}
		}
		message, err := renderer.render(request)
		if !assert.NoError(t, err, "Render(%q)", key) {
			continue
		}
		assert.NotEmpty(t, message.Subject, "Render(%q) subject", key)
		assert.NotEmpty(t, message.Text, "Render(%q) text", key)
		assert.NotEmpty(t, message.HTML, "Render(%q) HTML", key)
		assert.Contains(t, message.HTML, "Hello Representative,", "Render(%q) greeting", key)
		assert.NotContains(t, message.HTML, "<no value>", "Render(%q) left a template value unset", key)
		source, readErr := os.ReadFile("../../templates/" + string(key) + ".html")
		require.NoError(t, readErr)
		assert.Equal(t, strings.Count(string(source), "<!--[if"), strings.Count(message.HTML, "<!--[if"), "Render(%q) dropped Outlook conditional comments", key)
		assert.Equal(t, strings.Count(string(source), "<![endif]-->"), strings.Count(message.HTML, "<![endif]-->"), "Render(%q) dropped Outlook conditional comments", key)
		assert.Contains(t, message.HTML, "<o:OfficeDocumentSettings>", "Render(%q) dropped the Outlook settings block", key)
		for _, expected := range []string{`src="cid:` + testLockupCID + `"`, `alt="Proctor"`, `width="112"`, `height="22"`} {
			assert.Contains(t, message.HTML, expected, "Render(%q) is missing its accessible inline logo", key)
		}
	}
}

func TestRendererMasksTheNewAddressInTheEmailChangeWarning(t *testing.T) {
	t.Parallel()
	renderer, err := testRenderer()
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{Key: model.MailTemplateIdentityEmailChangeWarningOld,
		RecipientAddress: "old@example.test", EmailChange: &EmailChangeDetails{NewAddress: "attacker.chosen@example.org"},
		ActionURL: "https://proctor.example.test/undo#token=t", At: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC),
		Deadline: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	assert.Contains(t, message.HTML, "a***@example.org")
	assert.NotContains(t, message.HTML, "attacker.chosen")
	assert.NotContains(t, message.Text, "attacker.chosen")
	assert.Contains(t, message.HTML, "Tuesday 10 November 2026, 09:00 UTC")
}

func TestRendererStatesTheRetentionDeletionDate(t *testing.T) {
	t.Parallel()
	renderer, err := testRenderer()
	require.NoError(t, err)
	message, err := renderer.render(renderRequest{Key: model.MailTemplateExamRetentionScheduled,
		Retention: &RetentionDetails{RetireAfter: time.Date(2027, 2, 7, 12, 0, 0, 0, time.UTC)}})
	require.NoError(t, err)
	assert.Equal(t, "Some exam records will be deleted on Sunday 7 February 2027", message.Subject)
	assert.Contains(t, message.HTML, "Deletion date")
}
