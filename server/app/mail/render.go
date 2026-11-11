// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"bytes"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"net"
	"net/url"
	"regexp"
	"strings"
	texttemplate "text/template"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

const maxRenderedMessageBytes = 1 << 20

// templateProperties is the complete typed model visible to presentation assets.
// Every template shares one layout: copy, an optional emphasized hero detail,
// labeled facts, an optional aside, and an optional action. It intentionally
// contains no arbitrary map and offers no template helpers.
type templateProperties struct {
	Copy       templateCopy
	ActionURL  string
	Hero       *heroProperties
	Facts      []factProperties
	AsideAlert bool
}

type renderRequest struct {
	Key                 model.MailTemplateKey
	RecipientLocale     string
	RecipientName       string
	RecipientAddress    string
	RecipientTimezone   string
	Institution         string
	At                  time.Time
	Deadline            time.Time
	ActionURL           string
	PersonalAccessToken *PersonalAccessTokenDetails
	ExamManager         *ExamManagerDetails
	SittingSchedule     *SittingScheduleDetails
	ClassTransition     *ClassTransitionDetails
	SubmissionReceipt   *SubmissionReceiptDetails
	ResultRelease       *ResultReleaseDetails
	EmailChange         *EmailChangeDetails
	Retention           *RetentionDetails
}

type renderedMessage struct {
	Key     model.MailTemplateKey
	Locale  string
	Subject string
	Text    string
	HTML    string
}

// templateRendererDependencies are the collaborators of templateRenderer;
// newRenderer validates them once and templateRenderer keeps them in deps.
type templateRendererDependencies struct {
	Files                   fs.FS
	Localizer               *localization.Localizer
	ValidateCompleteCatalog bool
}

type templateRenderer struct {
	deps templateRendererDependencies

	html                         map[model.MailTemplateKey]*htmltemplate.Template
	text                         map[model.MailTemplateKey]*texttemplate.Template
	allowLoopbackHTTPDevelopment bool
	supportAddress               string
	displayLocation              *time.Location
}

// RendererPolicy contains composition-proven transport exceptions and
// installation presentation settings. Production callers leave
// AllowLoopbackHTTPDevelopment false so action links remain HTTPS-only.
type RendererPolicy struct {
	AllowLoopbackHTTPDevelopment bool
	// SupportAddress is shown in every footer when set.
	SupportAddress string
	// DisplayTimezone is the IANA zone used for recipients whose own time zone
	// is unset or the UTC default. Empty means UTC.
	DisplayTimezone string
}

// NewRenderer constructs the application-owned renderer from presentation
// assets and the installation localizer.
func NewRenderer(files fs.FS, localizer *localization.Localizer) (Renderer, error) {
	return NewRendererWithPolicy(files, localizer, RendererPolicy{})
}

// NewRendererWithPolicy constructs a renderer with an explicit
// composition-owned policy.
func NewRendererWithPolicy(files fs.FS, localizer *localization.Localizer, policy RendererPolicy) (Renderer, error) {
	renderer, err := newRenderer(templateRendererDependencies{Files: files, Localizer: localizer, ValidateCompleteCatalog: true})
	if err != nil {
		return nil, err
	}
	if err = renderer.applyPolicy(policy); err != nil {
		return nil, err
	}
	return renderer, nil
}

func (r *templateRenderer) applyPolicy(policy RendererPolicy) error {
	r.allowLoopbackHTTPDevelopment = policy.AllowLoopbackHTTPDevelopment
	if address := strings.ToLower(strings.TrimSpace(policy.SupportAddress)); address != "" {
		if !model.IsValidEmail(address) {
			return errors.New("mail support address is invalid")
		}
		r.supportAddress = address
	}
	location, err := loadDisplayLocation(policy.DisplayTimezone)
	if err != nil {
		return err
	}
	r.displayLocation = location
	return nil
}

func loadDisplayLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == model.DefaultTimezone {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("mail display timezone %q is invalid: %w", name, err)
	}
	return location, nil
}

func newRenderer(deps templateRendererDependencies) (*templateRenderer, error) {
	if deps.Files == nil || deps.Localizer == nil {
		return nil, errors.New("mail renderer dependencies are invalid")
	}

	assets, err := NewInlineAssets(deps.Files)
	if err != nil {
		return nil, fmt.Errorf("load mail inline assets: %w", err)
	}

	renderer := &templateRenderer{
		deps:            deps,
		displayLocation: time.UTC,
		html:            make(map[model.MailTemplateKey]*htmltemplate.Template),
		text:            make(map[model.MailTemplateKey]*texttemplate.Template),
	}
	for _, key := range model.AllMailTemplateKeys() {
		if deps.ValidateCompleteCatalog {
			if _, _, _, err := resolveCopy(deps.Localizer, key, localization.EnglishLocale, representativeCopyData); err != nil {
				return nil, fmt.Errorf("validate localized mail copy %q: %w", key, err)
			}
		}

		name := string(key)
		htmlSource, err := fs.ReadFile(deps.Files, name+".html")
		if err != nil {
			return nil, fmt.Errorf("read HTML mail template %q: %w", key, err)
		}

		images, err := assets.ForHTML(string(htmlSource))
		if err != nil || len(images) != 1 {
			return nil, fmt.Errorf("mail template %q must reference one released inline logo", key)
		}

		htmlValue, err := parseMailHTMLTemplate(name, string(htmlSource))
		if err != nil {
			return nil, fmt.Errorf("parse HTML mail template %q: %w", key, err)
		}

		textSource, err := fs.ReadFile(deps.Files, name+".txt")
		if err != nil {
			return nil, fmt.Errorf("read text mail template %q: %w", key, err)
		}

		textValue, err := texttemplate.New(name).Option("missingkey=error").Parse(string(textSource))
		if err != nil {
			return nil, fmt.Errorf("parse text mail template %q: %w", key, err)
		}

		renderer.html[key] = htmlValue
		renderer.text[key] = textValue
	}

	return renderer, nil
}

// clockFor picks the recipient's own time zone when they have set one, and the
// installation display time zone otherwise. UTC is the unset default, so it
// falls back too.
func (r *templateRenderer) clockFor(timezone string) clock {
	timezone = strings.TrimSpace(timezone)
	if timezone != "" && timezone != model.DefaultTimezone && timezone != "Local" {
		if location, err := time.LoadLocation(timezone); err == nil {
			return clock{location: location}
		}
	}
	if r.displayLocation == nil {
		return clock{location: time.UTC}
	}
	return clock{location: r.displayLocation}
}

func (r *templateRenderer) render(request renderRequest) (renderedMessage, error) {
	if r == nil || r.deps.Localizer == nil {
		return renderedMessage{}, errors.New("mail template renderer is nil")
	}

	c := r.clockFor(request.RecipientTimezone)
	data := copyData{
		Name:        firstName(request.RecipientName),
		Institution: strings.TrimSpace(request.Institution),
		Address:     r.supportAddress,
	}
	if !request.Deadline.IsZero() {
		data.Deadline = c.dateTime(request.Deadline)
	}

	build, err := buildLayout(request, c, &data)
	if err != nil {
		return renderedMessage{}, err
	}

	copy, resolver, locale, err := resolveCopy(r.deps.Localizer, request.Key, request.RecipientLocale, data)
	if err != nil {
		return renderedMessage{}, err
	}

	actionURL := strings.TrimSpace(request.ActionURL)
	if copy.ActionLabel == "" {
		actionURL = ""
	} else if err = validateActionURL(actionURL, r.allowLoopbackHTTPDevelopment); err != nil {
		return renderedMessage{}, err
	}

	built := build(copy, resolver)
	if resolver.failure != nil {
		return renderedMessage{}, fmt.Errorf("resolve mail copy %q: %w", request.Key, resolver.failure)
	}

	properties := templateProperties{
		Copy:       copy,
		ActionURL:  actionURL,
		Hero:       built.Hero,
		Facts:      built.Facts,
		AsideAlert: kindFor(request.Key) == kindSecurity,
	}
	text, html, err := r.executeTemplates(request.Key, properties)
	if err != nil {
		return renderedMessage{}, err
	}

	return renderedMessage{Key: request.Key, Locale: locale, Subject: copy.Subject, Text: text, HTML: html}, nil
}

// firstName returns the greeting name: the first word of a display name.
func firstName(displayName string) string {
	fields := strings.Fields(displayName)
	if len(fields) == 0 {
		return ""
	}
	name := fields[0]
	for _, character := range name {
		if unicode.IsControl(character) {
			return ""
		}
	}
	if utf8.RuneCountInString(name) > 64 {
		return ""
	}
	return name
}

// executeTemplates executes the HTML and text templates for the key and enforces the rendered size bound.
func (r *templateRenderer) executeTemplates(key model.MailTemplateKey, properties templateProperties) (text, html string, err error) {
	htmlValue, ok := r.html[key]
	if !ok {
		return "", "", fmt.Errorf("HTML mail template %q is unavailable", key)
	}
	textValue, ok := r.text[key]
	if !ok {
		return "", "", fmt.Errorf("text mail template %q is unavailable", key)
	}
	var htmlOutput bytes.Buffer
	if err := htmlValue.Execute(&htmlOutput, properties); err != nil {
		return "", "", fmt.Errorf("render HTML mail template %q: %w", key, err)
	}
	var textOutput bytes.Buffer
	if err := textValue.Execute(&textOutput, properties); err != nil {
		return "", "", fmt.Errorf("render text mail template %q: %w", key, err)
	}
	if htmlOutput.Len()+textOutput.Len() > maxRenderedMessageBytes {
		return "", "", fmt.Errorf("rendered mail template %q exceeds %d bytes", key, maxRenderedMessageBytes)
	}
	return textOutput.String(), htmlOutput.String(), nil
}

func (r *templateRenderer) Render(request RenderRequest) (FrozenContent, error) {
	internal := renderRequest{
		Key:               request.Key,
		RecipientLocale:   request.Locale,
		ActionURL:         request.ActionURL,
		RecipientName:     request.RecipientName,
		RecipientAddress:  request.RecipientAddress,
		RecipientTimezone: request.RecipientTimezone,
		Institution:       request.Institution,
		At:                request.At,
		Deadline:          request.Deadline,
	}
	switch presentation := request.Presentation.(type) {
	case nil:
	case PersonalAccessTokenDetails:
		internal.PersonalAccessToken = &presentation
	case ExamManagerDetails:
		internal.ExamManager = &presentation
	case SittingScheduleDetails:
		internal.SittingSchedule = &presentation
	case ClassTransitionDetails:
		internal.ClassTransition = &presentation
	case SubmissionReceiptDetails:
		internal.SubmissionReceipt = &presentation
	case ResultReleaseDetails:
		internal.ResultRelease = &presentation
	case EmailChangeDetails:
		internal.EmailChange = &presentation
	case RetentionDetails:
		internal.Retention = &presentation
	default:
		return FrozenContent{}, errors.New("mail presentation is invalid")
	}

	return r.renderContent(internal)
}

func (r *templateRenderer) SittingLocales() (string, []string) {
	return r.deps.Localizer.DefaultLocale(), r.deps.Localizer.SupportedLocales()
}

func (r *templateRenderer) renderContent(request renderRequest) (FrozenContent, error) {
	message, err := r.render(request)
	if err != nil {
		return FrozenContent{}, err
	}
	return FrozenContent{Subject: message.Subject, Text: message.Text, HTML: message.HTML}, nil
}

func isPersonalAccessTokenTemplate(key model.MailTemplateKey) bool {
	definition, ok := definitionFor(key)
	return ok && definition.presentation == presentationPersonalAccessToken
}

func validPersonalAccessTokenDetails(details *PersonalAccessTokenDetails) bool {
	if details == nil ||
		strings.TrimSpace(details.Description) == "" ||
		!utf8.ValidString(details.Description) ||
		utf8.RuneCountInString(details.Description) > 255 ||
		details.ExpiresAt.IsZero() ||
		details.ActionAt.IsZero() ||
		details.ActionCount < 1 ||
		details.ActionCount > 128 {
		return false
	}

	for _, character := range details.Description {
		if unicode.IsControl(character) {
			return false
		}
	}

	return true
}

func isExamManagerTemplate(key model.MailTemplateKey) bool {
	definition, ok := definitionFor(key)
	return ok && definition.presentation == presentationExamManager
}

func validExamManagerDetails(details *ExamManagerDetails) bool {
	if details == nil {
		return false
	}
	title := strings.TrimSpace(details.Title)
	if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 255 || details.ActionAt.IsZero() {
		return false
	}
	switch details.Relationship {
	case "manager", "owner", "no_longer_manager":
	default:
		return false
	}
	for _, character := range title {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func isSittingScheduleTemplate(key model.MailTemplateKey) bool {
	definition, ok := definitionFor(key)
	return ok && definition.presentation == presentationSittingSchedule
}

func validSittingScheduleDetails(details *SittingScheduleDetails) bool {
	if details == nil || details.StartsAt.IsZero() || !details.StartsAt.Before(details.EndsAt) {
		return false
	}
	for _, value := range []string{details.ExamTitle, details.ClassDisplayName} {
		value = strings.TrimSpace(value)
		if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 {
			return false
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return false
			}
		}
	}
	return true
}

func isClassTransitionTemplate(key model.MailTemplateKey) bool {
	definition, ok := definitionFor(key)
	return ok && definition.presentation == presentationClassTransition
}

func isSubmissionReceiptTemplate(key model.MailTemplateKey) bool {
	definition, ok := definitionFor(key)
	return ok && definition.presentation == presentationSubmissionReceipt
}

func validSubmissionReceiptDetails(details *SubmissionReceiptDetails) bool {
	if details == nil || details.SealedAt.IsZero() || !validBoundedMailLabel(details.ExamTitle) {
		return false
	}
	return details.SittingID.IsValid() && details.SubmissionID.IsValid() && details.ReceiptCode.IsValid()
}

func validClassTransitionDetails(key model.MailTemplateKey, details *ClassTransitionDetails) bool {
	if details == nil || details.StartsAt.IsZero() || (!details.EndsAt.IsZero() && !details.StartsAt.Before(details.EndsAt)) {
		return false
	}

	previous := strings.TrimSpace(details.PreviousClassDisplayName)
	if (key == model.MailTemplateAcademicClassTransferred) != (previous != "") ||
		!validBoundedMailLabel(details.ClassDisplayName) ||
		(previous != "" && !validBoundedMailLabel(previous)) {
		return false
	}

	if key == model.MailTemplateAcademicClassEnrollmentEnded && details.EndsAt.IsZero() {
		return false
	}

	return true
}

func validBoundedMailLabel(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateActionURL(raw string, allowLoopbackHTTPDevelopment bool) error {
	if raw == "" || len(raw) > 4096 {
		return errors.New("mail action URL is missing or too long")
	}
	for _, character := range raw {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return errors.New("mail action URL contains whitespace or control characters")
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("mail action URL must be an absolute HTTPS URL without user information")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || !allowLoopbackHTTPDevelopment {
		return errors.New("mail action URL must be an absolute HTTPS URL without user information")
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("mail action URL HTTP development origin must be loopback")
	}
	return nil
}

// outlookConditionalComment matches a complete Outlook conditional comment:
// a hidden block (<!--[if mso]>...<![endif]-->) or one marker of a revealed
// block (<!--[if !mso]><!--> and <!--<![endif]-->).
var outlookConditionalComment = regexp.MustCompile(`(?s)<!--(?:\[if [^\]]+\]|<!\[endif\]).*?-->`)

// parseMailHTMLTemplate keeps the generated Outlook conditional comments that
// html/template would otherwise strip as ordinary comments. Each comment is
// static generated markup; it is replaced by a template call returning the
// exact trusted text, so the escaper still owns every dynamic value.
func parseMailHTMLTemplate(name, source string) (*htmltemplate.Template, error) {
	var conditionals []htmltemplate.HTML
	var invalid error
	rewritten := outlookConditionalComment.ReplaceAllStringFunc(source, func(comment string) string {
		if strings.Contains(comment, "{{") || strings.Contains(comment, "}}") {
			invalid = fmt.Errorf("mail template %q has a template action inside an Outlook conditional comment", name)
		}
		conditionals = append(conditionals, htmltemplate.HTML(comment)) // #nosec G203 -- static generated template markup.
		return fmt.Sprintf("{{outlookConditional %d}}", len(conditionals)-1)
	})
	if invalid != nil {
		return nil, invalid
	}
	return htmltemplate.New(name).Option("missingkey=error").Funcs(htmltemplate.FuncMap{
		"outlookConditional": func(index int) htmltemplate.HTML { return conditionals[index] },
	}).Parse(rewritten)
}
