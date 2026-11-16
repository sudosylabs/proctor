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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// mailHandlerRequest is what the route kernel hands a mail handler for the
// signed-in operator.
func mailHandlerRequest(t *testing.T, principal model.Principal, request *http.Request, params route.Params) route.Request {
	t.Helper()
	return route.Request{Context: t.Context(), Principal: principal, Metadata: model.RequestMetadata{RequestID: "mail-test"}, HTTP: request, Params: params}
}

// mailFixtureDelivery is a stored test-mail delivery in state, written at.
func mailFixtureDelivery(id model.MailDeliveryID, state model.MailDeliveryState, at time.Time) *model.MailDelivery {
	return &model.MailDelivery{ID: id, OccurrenceID: model.NewMailOccurrenceID(), TargetUserID: model.NewUserID(), TemplateKey: model.MailTemplateSystemTest,
		TemplateDigest: strings.Repeat("a", 64), MaskedRecipient: "o***@example.test", State: state, CreatedAt: at, UpdatedAt: at,
		MessageDate: at, Deadline: at.Add(time.Hour), MessageID: "<mail." + id.String() + "@example.test>", Revision: 1}
}

func TestMailResourceRejectsCallerSuppliedContentAndProjectsNoSecrets(t *testing.T) {
	principal := mailOperatorPrincipal()
	fixture := newMailFixture(t, principal)
	h := mailHandlers{app: fixture.service}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/test", bytes.NewBufferString(`{"recipient":"attacker@example.test","body":"chosen"}`))
	_, err := h.sendTest(mailHandlerRequest(t, principal, request, nil))
	require.True(t, apperror.Is(err, "request.invalid"), "caller content accepted: %v", err)
	require.Empty(t, fixture.deliveries.enqueued, "caller content reached the mail Store")
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/test", nil)
	result, err := h.sendTest(mailHandlerRequest(t, principal, request, nil))
	require.NoError(t, err, "empty test = %#v", result)
	require.Equal(t, http.StatusAccepted, result.Status(), "empty test = %#v", result)
	require.Len(t, fixture.deliveries.enqueued, 1, "the empty test was queued once")
	queued := fixture.deliveries.enqueued[0].Delivery
	require.Equal(t, principal.UserID, queued.TargetUserID, "test mail goes only to the caller")
	require.Equal(t, model.MailTemplateSystemTest, queued.TemplateKey)
	require.Equal(t, []model.UserID{principal.UserID}, fixture.limiter.accounted, "the caller's test mail was rate limited")
	// Field names are checked, not values: delivery and message IDs are random
	// and can spell any short word.
	encoded, err := json.Marshal(result.Body())
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "operator@example.test")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.JSONEq(t, `"`+queued.ID.String()+`"`, string(fields["id"]), "the response is the queued delivery")
	for name := range fields {
		for _, forbidden := range []string{"ciphertext", "subject", "html", "body"} {
			require.NotContains(t, name, forbidden, "mail response field %q", name)
		}
	}
}

func TestMailDeliveryListCursorFiltersAndMutationBodiesAreBounded(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	id := model.NewMailDeliveryID()
	principal := mailOperatorPrincipal()
	fixture := newMailFixture(t, principal)
	fixture.deliveries.listed = []*model.MailDelivery{mailFixtureDelivery(id, model.MailDeliveryFailed, at)}
	h := mailHandlers{app: fixture.service}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?state=failed&template_key=system.mail_test&created_after=1970-01-01T00:00:00.001Z&created_before=2286-11-20T17:46:39.999Z&limit=1", nil)
	result, err := h.listDeliveries(mailHandlerRequest(t, principal, request, nil))
	require.NoError(t, err, "list result=%#v err=%v", result, err)
	require.Equal(t, http.StatusOK, result.Status(), "list result=%#v err=%v", result, err)
	require.Len(t, fixture.deliveries.lists, 1, "the listing reached the mail Store once")
	listed := fixture.deliveries.lastList(t)
	require.Equal(t, 2, listed.Limit, "the page size reached the Store with its probe row")
	require.Equal(t, []model.MailDeliveryState{model.MailDeliveryFailed}, listed.States)
	require.Equal(t, []model.MailTemplateKey{model.MailTemplateSystemTest}, listed.TemplateKeys)
	cursor, err := encodeMailDeliveryCursor(mailDeliveryCursor{Sort: "updated", Dir: listDescending, At: at.Format(time.RFC3339Nano), ID: id.String()})
	require.NoError(t, err)
	decoded, err := decodeMailDeliveryCursor(cursor)
	require.NoError(t, err, "cursor=%#v err", decoded)
	require.Equal(t, id.String(), decoded.ID, "cursor=%#v err", decoded)
	require.Equal(t, at.Format(time.RFC3339Nano), decoded.At, "cursor=%#v err", decoded)
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?state=not-a-state", nil)
	_, err = h.listDeliveries(mailHandlerRequest(t, principal, request, nil))
	require.True(t, apperror.Is(err, "request.invalid"), "invalid filter err=%v", err)
	require.Len(t, fixture.deliveries.lists, 1, "an invalid filter reaches no listing")
	recipient := model.NewUserID()
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?recipient_user_id="+recipient.String(), nil)
	_, err = h.listDeliveries(mailHandlerRequest(t, principal, request, nil))
	require.NoError(t, err)
	require.Equal(t, recipient, fixture.deliveries.lastList(t).RecipientUserID, "the recipient User reaches the mail Store")
	for _, raw := range []string{"recipient_user_id=someone@example.test", "recipient_user_id=", "recipient_user_id=" + recipient.String() + "&recipient_user_id=" + recipient.String()} {
		request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?"+raw, nil)
		_, err = h.listDeliveries(mailHandlerRequest(t, principal, request, nil))
		require.True(t, apperror.Is(err, "request.invalid"), "%s err=%v", raw, err)
	}
	require.Len(t, fixture.deliveries.lists, 2, "a refused recipient reaches no listing")
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/deliveries/"+id.String()+"/retry", bytes.NewBufferString(`{"body":"forbidden"}`))
	_, err = h.retryDelivery(mailHandlerRequest(t, principal, request, route.Params{"mail_delivery_id": id.String()}))
	require.True(t, apperror.Is(err, "request.invalid"), "retry body err=%v", err)
	require.Empty(t, fixture.deliveries.gets, "a retry body reaches no delivery")
	require.Empty(t, fixture.deliveries.retries, "a retry body reaches no delivery")
}

func TestMailDeliveryEndpointForwardsExactCursorAndMapsMalformedCursor(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 10, 11, 12, 345000, time.UTC)
	id := model.NewMailDeliveryID()
	principal := operatorTestPrincipal()
	fixture := newMailFixture(t, principal)
	fixture.deliveries.listed = []*model.MailDelivery{mailFixtureDelivery(model.NewMailDeliveryID(), model.MailDeliveryQueued, at.Add(-time.Minute))}
	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, mailResource(fixture.service))
	cursor, err := encodeMailDeliveryCursor(mailDeliveryCursor{Sort: "updated", Dir: listDescending, At: at.Format(time.RFC3339Nano), ID: id.String()})
	require.NoError(t, err)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?cursor="+url.QueryEscape(cursor), nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "cursor forwarding = %d body=%s", response.Code, response.Body.String())
	continued := fixture.deliveries.lastList(t)
	require.True(t, continued.BeforeAt.Equal(at), "cursor forwarding = %#v", continued)
	require.Equal(t, id, continued.BeforeID, "cursor forwarding = %#v", continued)

	malformed := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/mail/deliveries?cursor=not-a-cursor", nil)
	malformed.Header.Set("Authorization", "Bearer credential")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformed)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
	require.Len(t, fixture.deliveries.lists, 1, "a malformed cursor reaches no listing")
}

func TestMailMetricsProjectsOnlyBoundedOperationalDimensions(t *testing.T) {
	t.Parallel()
	principal := mailOperatorPrincipal()
	fixture := newMailFixture(t, principal)
	result, err := (mailHandlers{app: fixture.service}).getMetrics(mailHandlerRequest(t, principal, &http.Request{}, nil))
	require.NoError(t, err, "getMetrics() = %#v, %v", result, err)
	require.Equal(t, http.StatusOK, result.Status(), "getMetrics() = %#v, %v", result, err)
	require.Equal(t, 1, fixture.metrics.reads, "getMetrics() = %#v, %v", result, err)
}

func TestMailRekeyAcceptsOnlyAKeyIdentityAndProjectsSafeOperation(t *testing.T) {
	t.Parallel()
	principal := mailOperatorPrincipal()
	fixture := newMailFixture(t, principal)
	primaryKeyID := fixture.sealer.PrimaryKeyID()
	fixture.deliveries.keyState = &store.MailKeyState{RequiredPrimaryKeyID: primaryKeyID,
		Active: []store.MailPayloadKeyUsage{{KeyID: fixture.retiringID, ActiveReferences: 7}}}
	h := mailHandlers{app: fixture.service}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/rekey",
		bytes.NewBufferString(`{"retiring_key_id":"`+fixture.retiringID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	result, err := h.startRekey(mailHandlerRequest(t, principal, request, nil))
	require.NoError(t, err, "startRekey() = %#v, %v", result, err)
	require.Equal(t, http.StatusAccepted, result.Status(), "startRekey() = %#v, %v", result, err)
	starts := fixture.deliveries.rekeyStarts
	require.Len(t, starts, 1, "startRekey() = %#v, %v", result, err)
	require.Equal(t, fixture.retiringID, starts[0].RetiringKeyID, "startRekey() = %#v, %v", result, err)
	require.Equal(t, primaryKeyID, starts[0].PrimaryKeyID, "the rekey targets the configured primary key")
	require.Equal(t, 1, fixture.wakes, "the rekey Job was woken")
	response, ok := result.Body().(mailRekeyResponse)
	require.True(t, ok)
	require.Equal(t, starts[0].Job.ID.String(), response.JobID)
	require.Equal(t, primaryKeyID, response.PrimaryKeyID)
	require.Equal(t, starts[0].RetiringKeyID, response.RetiringKeyID)
	require.Equal(t, "2026-09-04T12:00:00Z", response.CreatedAt)
	invalid, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mail/rekey",
		bytes.NewBufferString(`{"retiring_key_id":"`+fixture.retiringID+`","encryption_key":"secret"}`))
	invalid.Header.Set("Content-Type", "application/json")
	_, err = h.startRekey(mailHandlerRequest(t, principal, invalid, nil))
	require.True(t, apperror.Is(err, "request.invalid"), "secret-bearing rekey body error = %v", err)
	require.Len(t, fixture.deliveries.rekeyStarts, 1, "secret-bearing rekey body error = %v", err)
	stateResult, err := h.getKeyState(mailHandlerRequest(t, principal, &http.Request{}, nil))
	require.NoError(t, err, "key state response = %#v", stateResult.Body())
	state, ok := stateResult.Body().(mailKeyStateResponse)
	require.True(t, ok, "key state response = %#v", stateResult.Body())
	require.Equal(t, primaryKeyID, state.PrimaryKeyID, "key state response = %#v", stateResult.Body())
	require.Equal(t, state.PrimaryKeyID, state.RequiredPrimaryKeyID, "key state response = %#v", stateResult.Body())
	require.Len(t, state.Active, 1, "key state response = %#v", stateResult.Body())
	require.Equal(t, fixture.retiringID, state.Active[0].KeyID, "key state response = %#v", stateResult.Body())
	require.Equal(t, int64(7), state.Active[0].ActiveReferences, "key state response = %#v", stateResult.Body())
}

func TestMailRekeyStatusProjectsSafeProgressAndFinalProof(t *testing.T) {
	t.Parallel()
	principal := mailOperatorPrincipal()
	fixture := newMailFixture(t, principal)
	rekey := succeededMailRekeyJob(t, fixture.sealer.PrimaryKeyID(), fixture.retiringID, mailFixtureNow.Add(-time.Hour))
	fixture.rekeyJobs.jobs = []*model.Job{rekey}
	jobID := rekey.ID
	result, err := (mailHandlers{app: fixture.service}).getRekeyStatus(mailHandlerRequest(t, principal, &http.Request{}, route.Params{"job_id": jobID.String()}))
	require.NoError(t, err, "getRekeyStatus() = %#v, %v", result, err)
	require.Equal(t, http.StatusOK, result.Status(), "getRekeyStatus() = %#v, %v", result, err)
	require.Equal(t, []model.JobID{jobID}, fixture.rekeyJobs.gets, "getRekeyStatus() = %#v, %v", result, err)
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

// TestMailDeliveryResponseNamesTheRecipientOnlyWhenTheApplicationDid carries
// the recipient's name beside the masked address when the application named
// the User, and omits it otherwise; no address beyond the masked one appears.
func TestMailDeliveryResponseNamesTheRecipientOnlyWhenTheApplicationDid(t *testing.T) {
	t.Parallel()
	recipient := model.NewUserID()
	view := mail.DeliveryView{ID: model.NewMailDeliveryID(), TargetUserID: recipient, MaskedRecipient: "b***@example.test", RecipientDisplayName: "Bo Student"}
	named := mailResponse(view)
	require.Equal(t, recipient.String(), named.TargetUserID)
	require.Equal(t, "Bo Student", named.RecipientDisplayName)
	require.Equal(t, "b***@example.test", named.MaskedRecipient)

	view.RecipientDisplayName = ""
	encoded, err := json.Marshal(mailResponse(view))
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.NotContains(t, fields, "recipient_display_name", "an unnamed recipient omits the name")
}
