// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

type recordingMailAPI struct {
	sends     int
	ids       []model.MailDeliveryID
	view      mail.DeliveryView
	lists     int
	listQuery mail.ListDeliveriesQuery
	metrics   int
	rekeys    []string
	rekeyIDs  []model.JobID
}

func (m *recordingMailAPI) GetMailKeyState(context.Context, invocation.Invocation) (mail.KeyStateView, error) {
	return mail.KeyStateView{PrimaryKeyID: "22222222222222222222222222222222",
		RequiredPrimaryKeyID: "22222222222222222222222222222222", Active: []mail.PayloadKeyUsageView{{
			KeyID: "11111111111111111111111111111111", ActiveReferences: 7,
		}}}, nil
}

func (m *recordingMailAPI) StartMailRekey(_ context.Context, _ invocation.Invocation, retiringKeyID string) (mail.RekeyView, error) {
	m.rekeys = append(m.rekeys, retiringKeyID)
	return mail.RekeyView{JobID: model.NewJobID(), PrimaryKeyID: "22222222222222222222222222222222",
		RetiringKeyID: retiringKeyID, CreatedAt: time.Unix(100, 0).UTC()}, nil
}

func (m *recordingMailAPI) GetMailRekeyStatus(_ context.Context, _ invocation.Invocation, jobID model.JobID) (mail.RekeyStatusView, error) {
	m.rekeyIDs = append(m.rekeyIDs, jobID)
	return mail.RekeyStatusView{JobID: jobID, Status: model.JobStatusSucceeded,
		PrimaryKeyID: "22222222222222222222222222222222", RetiringKeyID: "11111111111111111111111111111111",
		CreatedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(103, 0).UTC(), CompletedAt: model.OptionalTimeFrom(time.Unix(103, 0).UTC()),
		AttemptCount: 2, MaximumAttempts: 11, Processed: 9, Reencrypted: 8,
		Progress: &mail.RekeyProgressView{Current: 4, Total: 9, Stage: "reencrypting"},
		Proof:    &mail.RekeyProofView{RetirementSafe: true}}, nil
}

func (m *recordingMailAPI) GetMailMetrics(context.Context, invocation.Invocation) (mail.MetricsSnapshot, error) {
	m.metrics++
	return mail.MetricsSnapshot{HealthCode: mail.HealthHealthy}, nil
}

func (m *recordingMailAPI) SendTestMail(context.Context, invocation.Invocation) (mail.DeliveryView, error) {
	m.sends++
	return m.view, nil
}
func (m *recordingMailAPI) GetMailDelivery(_ context.Context, _ invocation.Invocation, id model.MailDeliveryID) (mail.DeliveryView, error) {
	m.ids = append(m.ids, id)
	return m.view, nil
}
func (m *recordingMailAPI) ListMailDeliveries(_ context.Context, _ invocation.Invocation, query mail.ListDeliveriesQuery) (mail.DeliveryPage, error) {
	m.lists++
	m.listQuery = query
	page := mail.DeliveryPage{Items: []mail.DeliveryView{m.view}}
	if query.BeforeID.IsZero() {
		page.Total = paging.Total{Counted: true, Total: 1}
	}
	return page, nil
}
func (m *recordingMailAPI) CancelMailDelivery(_ context.Context, _ invocation.Invocation, id model.MailDeliveryID) (mail.DeliveryView, error) {
	m.ids = append(m.ids, id)
	return m.view, nil
}
func (m *recordingMailAPI) RetryMailDelivery(_ context.Context, _ invocation.Invocation, id model.MailDeliveryID) (mail.DeliveryView, error) {
	m.ids = append(m.ids, id)
	return m.view, nil
}

func TestMailResourceRejectsCallerSuppliedContentAndProjectsNoSecrets(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	id := model.NewMailDeliveryID()
	fake := &recordingMailAPI{view: mail.DeliveryView{ID: id, OccurrenceID: model.NewMailOccurrenceID(), TargetUserID: model.NewUserID(), TemplateKey: model.MailTemplateSystemTest, TemplateDigest: strings.Repeat("a", 64), MaskedRecipient: "o***@example.test", State: model.MailDeliveryQueued, CreatedAt: at, UpdatedAt: at, MessageDate: at, Deadline: at.Add(time.Hour), MessageID: "<mail." + id.String() + "@example.test>"}}
	h := mailHandlers{app: fake}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/test", bytes.NewBufferString(`{"recipient":"attacker@example.test","body":"chosen"}`))
	_, err := h.sendTest(route.Request{HTTP: request})
	require.True(t, apperror.Is(err, "request.invalid"), "caller content accepted: %v sends", err)
	require.Equal(t, 0, fake.sends, "caller content accepted: %v sends", err)
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/test", nil)
	result, err := h.sendTest(route.Request{HTTP: request})
	require.NoError(t, err, "empty test = %#v", result)
	require.Equal(t, http.StatusAccepted, result.Status(), "empty test = %#v", result)
	require.Equal(t, 1, fake.sends, "empty test = %#v", result)
	// Field names are checked, not values: delivery and message IDs are random
	// and can spell any short word.
	encoded, err := json.Marshal(mailResponse(fake.view))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "operator@example.test")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	for name := range fields {
		for _, forbidden := range []string{"ciphertext", "subject", "html", "body"} {
			require.NotContains(t, name, forbidden, "mail response field %q", name)
		}
	}
}

func TestMailDeliveryListCursorFiltersAndMutationBodiesAreBounded(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	id := model.NewMailDeliveryID()
	fake := &recordingMailAPI{view: mail.DeliveryView{ID: id, OccurrenceID: model.NewMailOccurrenceID(), TargetUserID: model.NewUserID(), TemplateKey: model.MailTemplateSystemTest, TemplateDigest: strings.Repeat("a", 64), MaskedRecipient: "o***@example.test", State: model.MailDeliveryFailed, CreatedAt: at, UpdatedAt: at, MessageDate: at, Deadline: at.Add(time.Hour), MessageID: "<mail." + id.String() + "@example.test>"}}
	h := mailHandlers{app: fake}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?state=failed&template_key=system.mail_test&created_after=1970-01-01T00:00:00.001Z&created_before=2286-11-20T17:46:39.999Z&limit=1", nil)
	result, err := h.listDeliveries(route.Request{HTTP: request})
	require.NoError(t, err, "list result=%#v err=%v lists", result, err)
	require.Equal(t, http.StatusOK, result.Status(), "list result=%#v err=%v lists", result, err)
	require.Equal(t, 1, fake.lists, "list result=%#v err=%v lists", result, err)
	cursor, err := encodeMailDeliveryCursor(mailDeliveryCursor{Sort: "updated", Dir: listDescending, At: at.Format(time.RFC3339Nano), ID: id.String()})
	require.NoError(t, err)
	decoded, err := decodeMailDeliveryCursor(cursor)
	require.NoError(t, err, "cursor=%#v err", decoded)
	require.Equal(t, id.String(), decoded.ID, "cursor=%#v err", decoded)
	require.Equal(t, at.Format(time.RFC3339Nano), decoded.At, "cursor=%#v err", decoded)
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?state=not-a-state", nil)
	_, err = h.listDeliveries(route.Request{HTTP: request})
	require.True(t, apperror.Is(err, "request.invalid"), "invalid filter err=%v lists", err)
	require.Equal(t, 1, fake.lists, "invalid filter err=%v lists", err)
	recipient := model.NewUserID()
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?recipient_user_id="+recipient.String(), nil)
	_, err = h.listDeliveries(route.Request{HTTP: request})
	require.NoError(t, err)
	require.Equal(t, recipient, fake.listQuery.RecipientUserID, "the recipient User reaches the application")
	for _, raw := range []string{"recipient_user_id=someone@example.test", "recipient_user_id=", "recipient_user_id=" + recipient.String() + "&recipient_user_id=" + recipient.String()} {
		request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?"+raw, nil)
		_, err = h.listDeliveries(route.Request{HTTP: request})
		require.True(t, apperror.Is(err, "request.invalid"), "%s err=%v", raw, err)
	}
	require.Equal(t, 2, fake.lists, "a refused recipient reaches no listing")
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/deliveries/"+id.String()+"/retry", bytes.NewBufferString(`{"body":"forbidden"}`))
	_, err = h.retryDelivery(route.Request{HTTP: request, Params: route.Params{"mail_delivery_id": id.String()}})
	require.True(t, apperror.Is(err, "request.invalid"), "retry body err=%v ids", err)
	require.Empty(t, fake.ids, "retry body err=%v ids", err)
}

func TestMailDeliveryEndpointForwardsExactCursorAndMapsMalformedCursor(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 10, 11, 12, 345000, time.UTC)
	id := model.NewMailDeliveryID()
	fake := &recordingMailAPI{view: mail.DeliveryView{ID: model.NewMailDeliveryID(), CreatedAt: at.Add(-time.Minute)}}
	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: operatorTestPrincipal()}, mailResource(fake))
	cursor, err := encodeMailDeliveryCursor(mailDeliveryCursor{Sort: "updated", Dir: listDescending, At: at.Format(time.RFC3339Nano), ID: id.String()})
	require.NoError(t, err)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?cursor="+url.QueryEscape(cursor), nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "cursor forwarding = %d query=%#v body=%s", response.Code, fake.listQuery, response.Body.String())
	require.True(t, fake.listQuery.BeforeAt.Equal(at), "cursor forwarding = %d query=%#v body=%s", response.Code, fake.listQuery, response.Body.String())
	require.Equal(t, id, fake.listQuery.BeforeID, "cursor forwarding = %d query=%#v body=%s", response.Code, fake.listQuery, response.Body.String())

	malformed := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?cursor=not-a-cursor", nil)
	malformed.Header.Set("Authorization", "Bearer credential")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformed)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
}

func TestMailMetricsProjectsOnlyBoundedOperationalDimensions(t *testing.T) {
	t.Parallel()
	fake := &recordingMailAPI{}
	result, err := (mailHandlers{app: fake}).getMetrics(route.Request{HTTP: &http.Request{}})
	require.NoError(t, err, "getMetrics() = %#v, %v calls", result, err)
	require.Equal(t, http.StatusOK, result.Status(), "getMetrics() = %#v, %v calls", result, err)
	require.Equal(t, 1, fake.metrics, "getMetrics() = %#v, %v calls", result, err)
}

func TestMailRekeyAcceptsOnlyAKeyIdentityAndProjectsSafeOperation(t *testing.T) {
	t.Parallel()
	fake := &recordingMailAPI{}
	h := mailHandlers{app: fake}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/rekey",
		bytes.NewBufferString(`{"retiring_key_id":"11111111111111111111111111111111"}`))
	request.Header.Set("Content-Type", "application/json")
	result, err := h.startRekey(route.Request{HTTP: request})
	require.NoError(t, err, "startRekey() = %#v, %v calls", result, err)
	require.Equal(t, http.StatusAccepted, result.Status(), "startRekey() = %#v, %v calls", result, err)
	require.Len(t, fake.rekeys, 1, "startRekey() = %#v, %v calls", result, err)
	require.Equal(t, "11111111111111111111111111111111", fake.rekeys[0], "startRekey() = %#v, %v calls", result, err)
	response, ok := result.Body().(mailRekeyResponse)
	require.True(t, ok)
	require.NotEmpty(t, response.JobID)
	require.Equal(t, "22222222222222222222222222222222", response.PrimaryKeyID)
	require.Equal(t, fake.rekeys[0], response.RetiringKeyID)
	require.Equal(t, "1970-01-01T00:01:40Z", response.CreatedAt)
	invalid, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/rekey",
		bytes.NewBufferString(`{"retiring_key_id":"11111111111111111111111111111111","encryption_key":"secret"}`))
	invalid.Header.Set("Content-Type", "application/json")
	_, err = h.startRekey(route.Request{HTTP: invalid})
	require.True(t, apperror.Is(err, "request.invalid"), "secret-bearing rekey body error = %v calls", err)
	require.Len(t, fake.rekeys, 1, "secret-bearing rekey body error = %v calls", err)
	stateResult, err := h.getKeyState(route.Request{HTTP: &http.Request{}})
	state, ok := stateResult.Body().(mailKeyStateResponse)
	require.NoError(t, err, "key state response = %#v", stateResult.Body())
	require.True(t, ok, "key state response = %#v", stateResult.Body())
	require.Equal(t, "22222222222222222222222222222222", state.PrimaryKeyID, "key state response = %#v", stateResult.Body())
	require.Equal(t, state.PrimaryKeyID, state.RequiredPrimaryKeyID, "key state response = %#v", stateResult.Body())
	require.Len(t, state.Active, 1, "key state response = %#v", stateResult.Body())
	require.Equal(t, "11111111111111111111111111111111", state.Active[0].KeyID, "key state response = %#v", stateResult.Body())
	require.Equal(t, int64(7), state.Active[0].ActiveReferences, "key state response = %#v", stateResult.Body())
}

func TestMailRekeyStatusProjectsSafeProgressAndFinalProof(t *testing.T) {
	t.Parallel()
	jobID := model.NewJobID()
	fake := &recordingMailAPI{}
	result, err := (mailHandlers{app: fake}).getRekeyStatus(route.Request{
		HTTP: &http.Request{}, Params: route.Params{"job_id": jobID.String()},
	})
	require.NoError(t, err, "getRekeyStatus() = %#v, %v ids", result, err)
	require.Equal(t, http.StatusOK, result.Status(), "getRekeyStatus() = %#v, %v ids", result, err)
	require.Len(t, fake.rekeyIDs, 1, "getRekeyStatus() = %#v, %v ids", result, err)
	require.Equal(t, jobID, fake.rekeyIDs[0], "getRekeyStatus() = %#v, %v ids", result, err)
	response, ok := result.Body().(mailRekeyStatusResponse)
	require.True(t, ok)
	require.Equal(t, jobID.String(), response.JobID)
	require.Equal(t, model.JobStatusSucceeded, response.Status)
	require.NotNil(t, response.Progress)
	require.Equal(t, int64(4), response.Progress.Current)
	require.Equal(t, int64(9), response.Progress.Total)
	require.Equal(t, int64(9), response.Processed)
	require.Equal(t, int64(8), response.Reencrypted)
	require.NotNil(t, response.Proof)
	require.True(t, response.Proof.RetirementSafe)
	require.Equal(t, int64(0), response.Proof.NonPrimaryReferences)
	require.Equal(t, int64(0), response.Proof.RetiringReferences)
	encoded, _ := json.Marshal(response)
	for _, forbidden := range []string{"ciphertext", "recipient", "encryption_key", "command", "checkpoint", "result"} {
		require.False(t, bytes.Contains(encoded, []byte(forbidden)), "rekey status leaked %q: %s", forbidden, encoded)
	}
}

func TestMailDeliveryResponseNamesExactlyOneTarget(t *testing.T) {
	t.Parallel()
	invitation := model.NewInvitationID()
	encoded, err := json.Marshal(mailResponse(mail.DeliveryView{ID: model.NewMailDeliveryID(), OccurrenceID: model.NewMailOccurrenceID(),
		TargetInvitationID: invitation, TemplateKey: model.MailTemplateAccessStudentClassInvitation}))
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &wire))
	require.NotContains(t, wire, "target_user_id", "an invitee has no account")
	require.JSONEq(t, `"`+invitation.String()+`"`, string(wire["target_invitation_id"]))

	user := model.NewUserID()
	encoded, err = json.Marshal(mailResponse(mail.DeliveryView{ID: model.NewMailDeliveryID(), OccurrenceID: model.NewMailOccurrenceID(),
		TargetUserID: user, TemplateKey: model.MailTemplateSystemTest}))
	require.NoError(t, err)
	wire = nil
	require.NoError(t, json.Unmarshal(encoded, &wire))
	require.NotContains(t, wire, "target_invitation_id")
	require.JSONEq(t, `"`+user.String()+`"`, string(wire["target_user_id"]))
}
