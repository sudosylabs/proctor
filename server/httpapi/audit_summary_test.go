// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestAuditSummaryCatalogueCoversEveryActionOperationAndSelfEvent fails when
// an audit action, a self-history operation, or a self-history event is added
// without a summary message, or when a message outlives its catalogue entry.
func TestAuditSummaryCatalogueCoversEveryActionOperationAndSelfEvent(t *testing.T) {
	t.Parallel()

	actions := make([]string, 0, len(auditActionSummaryVariables))
	for action := range auditActionSummaryVariables {
		actions = append(actions, string(action))
	}
	sort.Strings(actions)
	require.Equal(t, model.AuditActions(), actions, "every audit action has exactly one summary")

	wantOperations := []auditOperation{}
	wantEvents := []model.AuditSelfEvent{}
	for _, key := range model.AuditSelfEventKeys(nil, "") {
		if operation := (auditOperation{action: model.Action(key.Action), operation: key.Operation}); key.Operation != "" &&
			!slices.Contains(wantOperations, operation) {
			wantOperations = append(wantOperations, operation)
		}
		event, known := model.AuditSelfEventFor(key.Action, key.Operation, key.Status)
		require.True(t, known)
		if !slices.Contains(wantEvents, event) {
			wantEvents = append(wantEvents, event)
		}
	}
	operations := make([]auditOperation, 0, len(auditOperationSummaryVariables))
	for operation := range auditOperationSummaryVariables {
		operations = append(operations, operation)
	}
	byKey := func(left, right auditOperation) int {
		if left.action != right.action {
			return strings.Compare(string(left.action), string(right.action))
		}
		return strings.Compare(left.operation, right.operation)
	}
	slices.SortFunc(wantOperations, byKey)
	slices.SortFunc(operations, byKey)
	require.Equal(t, wantOperations, operations, "every operation the self-history catalogue keys on has a summary")

	events := make([]model.AuditSelfEvent, 0, len(selfAuditSummaryVariables))
	for event := range selfAuditSummaryVariables {
		events = append(events, event)
	}
	slices.Sort(wantEvents)
	slices.Sort(events)
	require.Equal(t, wantEvents, events, "every self-history event has a summary about the User and one by the User")

	require.NoError(t, route.ValidateLocalization(routetest.NewLocalizer(t), auditSummaryLocalizationDefinitions()),
		"every summary renders in English with its declared variables")
}

func auditSummaryRequest(t *testing.T, acceptLanguage string) *http.Request {
	t.Helper()
	localizer := routetest.NewLocalizer(t)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/audits", nil)
	locale := localization.PreferredLocale(acceptLanguage, localizer.SupportedLocales())
	return request.WithContext(route.WithLocalization(request.Context(), localizer, locale))
}

func TestAuditSummaryNamesWhatTheCallerMaySeeAndOtherwiseUsesGenericNouns(t *testing.T) {
	t.Parallel()
	request := auditSummaryRequest(t, "en")
	actor, subject, classID := model.NewUserID(), model.NewUserID(), model.NewClassID()
	enrolment := &model.AuditEvent{
		ActorID: actor, SubjectUserID: subject, Action: string(model.ActionClassMembersManage),
		Resource: model.Resource{Type: model.ResourceClass, ID: classID.String()}, Status: model.AuditStatusSuccess,
		Parameters: []byte(`{"operation":"enroll","value":{}}`), IPAddress: "192.0.2.10", UserAgent: "Browser",
	}
	visible := store.AuditNames{
		Users:   map[model.UserID]string{actor: "Ada Admin", subject: "Bo Student"},
		Classes: map[model.ClassID]string{classID: "Year 1 Mathematics A"},
	}

	for name, test := range map[string]struct {
		event *model.AuditEvent
		names store.AuditNames
		want  string
	}{
		"visible names":   {enrolment, visible, "Ada Admin enrolled Bo Student in Year 1 Mathematics A"},
		"nothing visible": {enrolment, store.AuditNames{}, "A user enrolled a user in a class"},
		"failed": {
			&model.AuditEvent{ActorID: actor, Action: enrolment.Action, Resource: enrolment.Resource, Status: model.AuditStatusFail},
			visible, "Ada Admin changed the members of Year 1 Mathematics A (failed)",
		},
		"unfinished": {
			&model.AuditEvent{ActorID: actor, Action: string(model.ActionAuditView),
				Resource: model.Resource{Type: model.ResourceInstitution, ID: model.NewID()}, Status: model.AuditStatusAttempt},
			visible, "Ada Admin viewed audit history (not finished)",
		},
		"no actor": {
			&model.AuditEvent{Action: string(model.ActionAuthenticationLogin),
				Resource: model.Resource{Type: model.ResourceUser, ID: subject.String()}, Status: model.AuditStatusSuccess},
			visible, "Sign-in to Bo Student",
		},
		"uncatalogued operation": {
			&model.AuditEvent{ActorID: actor, Action: string(model.ActionClassManage), Status: model.AuditStatusSuccess,
				Resource: enrolment.Resource, Parameters: []byte(`{"operation":"archive","value":{}}`)},
			visible, "Ada Admin managed Year 1 Mathematics A",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, auditSummary(request, test.event, test.names))
		})
	}
}

// TestAcademicAuditSummaryLeaksNothingTheRedactionRemoved renders a
// redacted academic row: no operation, subject, network identity, or
// parameter value reaches the sentence.
func TestAcademicAuditSummaryLeaksNothingTheRedactionRemoved(t *testing.T) {
	t.Parallel()
	request := auditSummaryRequest(t, "en")
	actor, subject, classID := model.NewUserID(), model.NewUserID(), model.NewClassID()
	recorded := &model.AuditEvent{
		ActorID: actor, SubjectUserID: subject, Action: string(model.ActionClassMembersManage),
		Resource: model.Resource{Type: model.ResourceClass, ID: classID.String()}, Status: model.AuditStatusSuccess,
		Parameters: []byte(`{"operation":"transfer_out","value":{"reason":"Private reason"}}`),
		PriorState: []byte(`{"display_name":"Prior Name"}`), Result: []byte(`{"secret":"Result value"}`),
		IPAddress: "192.0.2.10", UserAgent: "Secret Browser", RequestID: "request-1",
	}
	redacted := recorded.Clone()
	redacted.IPAddress, redacted.UserAgent, redacted.RequestID = "", "", ""
	redacted.Parameters, redacted.PriorState, redacted.Result = nil, nil, nil
	names := store.AuditNames{
		Users:   map[model.UserID]string{actor: "Ada Admin"},
		Classes: map[model.ClassID]string{classID: "Year 1 Mathematics A"},
	}

	summary := auditSummary(request, redacted, names)
	require.Equal(t, "Ada Admin changed the members of Year 1 Mathematics A", summary, "the action's sentence, never the operation's")
	for _, leaked := range []string{"transferred", "Private reason", "Prior Name", "Result value", "192.0.2.10", "Secret Browser", "request-1"} {
		require.NotContains(t, summary, leaked)
	}
}

func TestSelfAuditSummaryKeepsTheActorMask(t *testing.T) {
	t.Parallel()
	request := auditSummaryRequest(t, "en")
	unit := model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewAcademicUnitID().String()}
	teacher := store.AuditRoleName{Name: "teacher", DisplayName: "Teacher"}

	for name, test := range map[string]struct {
		event administration.SelfAuditEvent
		want  string
	}{
		"administrator grant with names": {administration.SelfAuditEvent{
			Event: model.AuditSelfEventRoleGranted, Status: model.AuditStatusSuccess, Relation: store.AuditSelfRelationSubject,
			ActorKind: administration.SelfAuditActorAdministrator, Resource: unit, ResourceName: "Mathematics", Role: teacher,
		}, "An administrator gave you the Teacher role in Mathematics"},
		"unrelated scope and unnamed role": {administration.SelfAuditEvent{
			Event: model.AuditSelfEventRoleGranted, Status: model.AuditStatusSuccess, Relation: store.AuditSelfRelationSubject,
			ActorKind: administration.SelfAuditActorAdministrator, Resource: model.Resource{Type: model.ResourceAcademicUnit},
		}, "An administrator gave you a role in an academic unit"},
		"built-in role is localized": {administration.SelfAuditEvent{
			Event: model.AuditSelfEventRoleEnded, Status: model.AuditStatusSuccess, Relation: store.AuditSelfRelationSubject,
			ActorKind: administration.SelfAuditActorSystem, Resource: model.Resource{Type: model.ResourceInstitution},
			Role: store.AuditRoleName{Name: model.SystemAdministratorRoleName, DisplayName: "Renamed administrators"},
		}, "The system ended the System Administrator role for you in the institution"},
		"own action": {administration.SelfAuditEvent{
			Event: model.AuditSelfEventPasswordChanged, Status: model.AuditStatusSuccess, Relation: store.AuditSelfRelationSubject,
			ActorKind: administration.SelfAuditActorSelf, Resource: model.Resource{Type: model.ResourceUser},
		}, "You changed your password"},
		"by the caller on another person": {administration.SelfAuditEvent{
			Event: model.AuditSelfEventClassEnrolled, Status: model.AuditStatusSuccess, Relation: store.AuditSelfRelationActor,
			ActorKind: administration.SelfAuditActorSelf, Resource: model.Resource{Type: model.ResourceClass},
		}, "You enrolled a user in a class"},
		"failed": {administration.SelfAuditEvent{
			Event: model.AuditSelfEventAccountDisabled, Status: model.AuditStatusFail, Relation: store.AuditSelfRelationSubject,
			ActorKind: administration.SelfAuditActorAdministrator, Resource: model.Resource{Type: model.ResourceUser},
		}, "An administrator disabled your account (failed)"},
		"failed sign-in says so once": {administration.SelfAuditEvent{
			Event: model.AuditSelfEventSignInFailed, Status: model.AuditStatusFail, Relation: store.AuditSelfRelationSubject,
			ActorKind: administration.SelfAuditActorSystem, Resource: model.Resource{Type: model.ResourceUser},
		}, "A sign-in to your account failed"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, selfAuditSummary(request, test.event))
		})
	}
}

// TestAuditSummaryFallsBackToEnglishForAnUnknownLocale reads a language the
// installation has no catalog for; the summary uses English rather than an
// empty or code-only string.
func TestAuditSummaryFallsBackToEnglishForAnUnknownLocale(t *testing.T) {
	t.Parallel()
	event := administration.SelfAuditEvent{
		Event: model.AuditSelfEventProfileChanged, Status: model.AuditStatusSuccess, Relation: store.AuditSelfRelationSubject,
		ActorKind: administration.SelfAuditActorAdministrator, Resource: model.Resource{Type: model.ResourceUser},
	}
	for _, acceptLanguage := range []string{"zz-ZZ", "de;q=0.9, fr;q=0.8", ""} {
		require.Equal(t, "An administrator changed your profile", selfAuditSummary(auditSummaryRequest(t, acceptLanguage), event), acceptLanguage)
	}

	locale := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	locale = locale.WithContext(route.WithLocalization(locale.Context(), routetest.NewLocalizer(t), "de"))
	require.Equal(t, "An administrator changed your profile", selfAuditSummary(locale, event), "an unsupported negotiated locale")
}

func TestAuditResourceWritesSummariesFromThePageNames(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	actor, subject, classID := model.NewUserID(), model.NewUserID(), model.NewClassID()
	audits := &focusedAuditApplication{
		events: []*model.AuditEvent{{
			ID: model.NewAuditEventID(), CreatedAt: model.TimeFromMillis(100), ActorID: actor, SubjectUserID: subject,
			Action: string(model.ActionClassMembersManage), Resource: model.Resource{Type: model.ResourceClass, ID: classID.String()},
			Status: model.AuditStatusSuccess, Parameters: []byte(`{"operation":"enroll","value":{}}`),
		}},
		names: store.AuditNames{
			Users:   map[model.UserID]string{actor: "Ada Admin"},
			Classes: map[model.ClassID]string{classID: "Year 1 Mathematics A"},
		},
	}
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: principal}, auditResource(audits))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/audits", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page route.List[auditEventResponse]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, "Ada Admin enrolled a user in Year 1 Mathematics A", page.Items[0].Summary,
		"a subject the caller cannot see stays generic")
	require.Equal(t, string(model.ActionClassMembersManage), page.Items[0].Action, "the codes stay beside the summary")
}
