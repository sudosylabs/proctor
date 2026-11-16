// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type configureExamDraftNativePolicyRequest struct {
	ExpectedDraftRevision int64                      `json:"expected_revision"`
	Native                model.NativeSecurityPolicy `json:"native"`
}

type configureExamDraftFocusLossRequest struct {
	ExpectedDraftRevision       int64  `json:"expected_revision"`
	Enabled                     bool   `json:"enabled"`
	MinimumDurationMilliseconds int64  `json:"minimum_duration_milliseconds"`
	IncidentCount               int    `json:"incident_count"`
	WindowMilliseconds          int64  `json:"window_milliseconds"`
	Outcome                     string `json:"outcome"`
}

type configureExamDraftExecutionRequest struct {
	ExpectedDraftRevision int64                       `json:"expected_revision"`
	Execution             examExecutionPolicyDocument `json:"execution"`
}

type configureExamDraftBrowserPolicyRequest struct {
	ExpectedDraftRevision int64                 `json:"expected_revision"`
	BrowserPolicy         browserPolicyDocument `json:"browser_policy"`
}

type browserPolicyDocument struct {
	Enabled     bool                        `json:"enabled"`
	StartRuleID string                      `json:"start_rule_id,omitempty"`
	Rules       []browserPolicyRuleDocument `json:"rules,omitempty"`
}

type browserPolicyRuleDocument struct {
	RuleID                   string `json:"rule_id"`
	Origin                   string `json:"origin"`
	PathPrefix               string `json:"path_prefix"`
	HostMatch                string `json:"host_match"`
	AllowRedirects           bool   `json:"allow_redirects"`
	BlockedNavigationOutcome string `json:"blocked_navigation_outcome"`
	InstitutionHTTPException bool   `json:"institution_http_exception"`
}

func (d *browserPolicyDocument) UnmarshalJSON(encoded []byte) error {
	if len(encoded) > model.BrowserPolicyMaximumBytes {
		return errors.New("browser policy exceeds its encoded limit")
	}
	if err := rejectDuplicateJSONObjectMembers(encoded, "browser policy"); err != nil {
		return err
	}
	type wire browserPolicyDocument
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &members); err != nil {
		return err
	}
	if members["enabled"] == nil || bytes.Equal(bytes.TrimSpace(members["enabled"]), []byte("null")) {
		return errors.New("browser policy enabled is required")
	}

	if decoded.Enabled {
		if len(members) != 3 || members["start_rule_id"] == nil || members["rules"] == nil || decoded.Rules == nil {
			return errors.New("enabled browser policy requires exactly start_rule_id and rules")
		}
	} else if len(members) != 1 {
		return errors.New("disabled browser policy contains enabled fields")
	}
	*d = browserPolicyDocument(decoded)
	return nil
}

func (d *browserPolicyRuleDocument) UnmarshalJSON(encoded []byte) error {
	if err := rejectDuplicateJSONObjectMembers(encoded, "browser policy rule"); err != nil {
		return err
	}

	type wire browserPolicyRuleDocument
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &members); err != nil {
		return err
	}

	for _, name := range []string{
		"rule_id",
		"origin",
		"path_prefix",
		"host_match",
		"allow_redirects",
		"blocked_navigation_outcome",
		"institution_http_exception",
	} {
		if members[name] == nil || bytes.Equal(bytes.TrimSpace(members[name]), []byte("null")) {
			return errors.New("browser policy rule fields are required")
		}
	}

	if len(members) != 7 {
		return errors.New("browser policy rule contains an unknown field")
	}

	*d = browserPolicyRuleDocument(decoded)
	return nil
}

func (d browserPolicyDocument) model() (model.BrowserPolicy, error) {
	rules := make([]model.BrowserPolicyRule, len(d.Rules))
	for index, rule := range d.Rules {
		rules[index] = model.BrowserPolicyRule{
			RuleID:                   rule.RuleID,
			Origin:                   rule.Origin,
			PathPrefix:               rule.PathPrefix,
			HostMatch:                model.BrowserPolicyHostMatch(rule.HostMatch),
			AllowRedirects:           rule.AllowRedirects,
			BlockedNavigationOutcome: model.BrowserPolicyBlockedNavigationOutcome(rule.BlockedNavigationOutcome),
			InstitutionHTTPException: rule.InstitutionHTTPException,
		}
	}

	if !d.Enabled {
		return model.DisabledBrowserPolicy(), nil
	}

	return model.NewBrowserPolicy(true, d.StartRuleID, rules)
}

func browserPolicyDocumentFromModel(policy model.BrowserPolicy) browserPolicyDocument {
	document := browserPolicyDocument{Enabled: policy.Enabled}
	if !policy.Enabled {
		return document
	}

	document.StartRuleID = policy.StartRuleID
	document.Rules = make([]browserPolicyRuleDocument, len(policy.Rules))
	for index, rule := range policy.Rules {
		document.Rules[index] = browserPolicyRuleDocument{
			RuleID:                   rule.RuleID,
			Origin:                   rule.Origin,
			PathPrefix:               rule.PathPrefix,
			HostMatch:                string(rule.HostMatch),
			AllowRedirects:           rule.AllowRedirects,
			BlockedNavigationOutcome: string(rule.BlockedNavigationOutcome),
			InstitutionHTTPException: rule.InstitutionHTTPException,
		}
	}

	return document
}

func (r *configureExamDraftFocusLossRequest) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateExamPolicyRequestMembers(data); err != nil {
		return err
	}
	type wire configureExamDraftFocusLossRequest
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	enabled, ok := members["enabled"]
	if !ok || bytes.Equal(bytes.TrimSpace(enabled), []byte("null")) {
		return errors.New("enabled must be provided and non-null")
	}
	*r = configureExamDraftFocusLossRequest(decoded)
	return nil
}

func rejectDuplicateExamPolicyRequestMembers(data []byte) error {
	return rejectDuplicateJSONObjectMembers(data, "focus loss policy")
}

func rejectDuplicateJSONObjectMembers(data []byte, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New(label + " must be a JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New(label + " member is invalid")
		}
		if _, exists := seen[key]; exists {
			return errors.New(label + " contains a duplicate member")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return decodeErr
		}
	}
	_, err = decoder.Token()
	return err
}

// examExecutionPolicyDocument is the execution policy, as requested and as
// returned.
type examExecutionPolicyDocument struct {
	Enabled     bool   `json:"enabled"`
	ProfileID   string `json:"profile_id,omitempty"`
	RecordInput bool   `json:"record_input"`
	Terminals   int    `json:"terminals"`
}

func (h examHandlers) configureDraftFocusLoss(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}

	var body configureExamDraftFocusLossRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedDraftRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}
	if body.MinimumDurationMilliseconds < 500 || body.MinimumDurationMilliseconds > 300_000 {
		return route.Result{}, route.InvalidRequest(errors.New("must be between 500 and 300000"))
	}
	if body.IncidentCount < 1 || body.IncidentCount > 100 {
		return route.Result{}, route.InvalidRequest(errors.New("must be between 1 and 100"))
	}
	if body.WindowMilliseconds < 10_000 || body.WindowMilliseconds > 14_400_000 || body.WindowMilliseconds < body.MinimumDurationMilliseconds {
		return route.Result{}, route.InvalidRequest(errors.New("must be between 10000 and 14400000 and at least minimum_duration_milliseconds"))
	}

	outcome := model.IntegrityOutcome(body.Outcome)
	switch outcome {
	case model.IntegrityOutcomeFlag, model.IntegrityOutcomeFlagAndWarn, model.IntegrityOutcomeFlagAndSuspend:
	default:
		return route.Result{}, route.InvalidRequest(errors.New("is not supported"))
	}

	view, err := h.app.ConfigureExamDraftFocusLoss(request.Context, request.Invocation(), exam.ConfigureDraftFocusLossCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Enabled:               body.Enabled,
		MinimumDuration:       time.Duration(body.MinimumDurationMilliseconds) * time.Millisecond,
		IncidentCount:         body.IncidentCount,
		Window:                time.Duration(body.WindowMilliseconds) * time.Millisecond,
		Outcome:               outcome,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examResponseFromView(view)), nil
}

func (h examHandlers) configureDraftExecution(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}
	var body configureExamDraftExecutionRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedDraftRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}
	var profile model.EnvironmentProfileID
	if body.Execution.ProfileID != "" {
		if profile, err = model.ParseEnvironmentProfileID(body.Execution.ProfileID); err != nil {
			return route.Result{}, route.InvalidRequest(err)
		}
	}
	view, err := h.app.ConfigureExamDraftExecution(request.Context, request.Invocation(), exam.ConfigureDraftExecutionCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Execution: model.ExecutionPolicy{Enabled: body.Execution.Enabled, Profile: profile,
			RecordInput: body.Execution.RecordInput, Terminals: body.Execution.Terminals},
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, examResponseFromView(view)), nil
}

func (h examHandlers) configureDraftBrowserPolicy(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}

	var body configureExamDraftBrowserPolicyRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedDraftRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	policy, err := body.BrowserPolicy.model()
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	view, err := h.app.ConfigureExamDraftBrowserPolicy(request.Context, request.Invocation(), exam.ConfigureDraftBrowserPolicyCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Policy:                policy,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examResponseFromView(view)), nil
}

func (h examHandlers) configureDraftNativePolicy(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}

	var body configureExamDraftNativePolicyRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedDraftRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}
	if validateErr := body.Native.Validate(); validateErr != nil {
		return route.Result{}, route.InvalidRequest(validateErr)
	}

	view, err := h.app.ConfigureExamDraftNativePolicy(request.Context, request.Invocation(), exam.ConfigureDraftNativePolicyCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		NativePolicy:          body.Native,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examResponseFromView(view)), nil
}

func (r *configureExamDraftNativePolicyRequest) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateJSONObjectMembers(data, "native policy request"); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	if len(members) != 2 {
		return errors.New("native policy request requires exactly expected_revision and native")
	}
	for _, name := range []string{"expected_revision", "native"} {
		value, exists := members[name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("native policy request member is missing or null")
		}
	}
	type wire configureExamDraftNativePolicyRequest
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*r = configureExamDraftNativePolicyRequest(decoded)
	return nil
}
