// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// newInboundRuntime is the connection of world's principal on a real socket,
// not yet running, admitted by world and running Attempt commands on attempts.
func newInboundRuntime(t testing.TB, world *admissionWorld, attempts *attempt.Service) (*connectionRuntime, *testPeer) {
	t.Helper()
	socket, peer := newSocketPair(t)
	return &connectionRuntime{
		admission:     world.admission,
		attempts:      attempts,
		socket:        socket,
		principal:     world.principal,
		metadata:      model.RequestMetadata{RequestID: "upgrade-request"},
		id:            model.NewID(),
		subscriptions: make(map[string]Subscription),
		replayable:    true,
		send:          make(chan outboundMessage, maximumSubscriptions+8),
	}, peer
}

func validInboundSubscription() Subscription {
	return Subscription{
		Action: model.ActionClassView,
		Resource: Resource{
			Type: model.ResourceClass,
			ID:   model.NewID(),
		},
	}
}

func requestWithData(t *testing.T, sequence int64, action string, value any) *Request {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return &Request{Sequence: sequence, Action: action, Data: data}
}

func nextInboundResponse(t *testing.T, runtime *connectionRuntime) *Response {
	t.Helper()
	select {
	case message := <-runtime.send:
		require.NotNil(t, message.response, "runtime enqueued an event, want response")
		return message.response
	case <-time.After(time.Second):
		require.FailNow(t, "runtime did not enqueue a response")
		return nil
	}
}

// pingRequestOfSize is a valid ping exactly size bytes long.
func pingRequestOfSize(sequence int64, size int) []byte {
	request := fmt.Sprintf(`{"sequence":%d,"action":"ping"}`, sequence)
	return []byte(request + strings.Repeat(" ", size-len(request)))
}

func TestConnectionRuntimeReadPumpRejectsInvalidRequestAndContinues(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		runtime, peer := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), nil)
		done := make(chan struct{})
		go func() {
			runtime.readPump(context.Background())
			close(done)
		}()
		require.NoError(t, peer.send(Request{Action: "ping"}))
		require.NoError(t, peer.send(Request{Sequence: 2, Action: "ping"}))
		peer.hangUp()
		synctest.Wait()
		select {
		case <-done:
		default:
			require.FailNow(t, "read pump outlived its connection")
		}

		invalid := nextInboundResponse(t, runtime)
		require.Equal(t, "error", invalid.Status)
		require.Equal(t, int64(0), invalid.Sequence)
		require.NotNil(t, invalid.Error)
		require.Equal(t, "websocket.request.invalid", invalid.Error.Code)
		require.Equal(t, "Invalid WebSocket request.", invalid.Error.Message)
		pong := nextInboundResponse(t, runtime)
		require.Equal(t, "ok", pong.Status)
		require.Equal(t, int64(2), pong.Sequence)
		require.Equal(t, `{"pong":true}`, string(pong.Data))
	})
}

func TestConnectionRuntimeParentCancellationStopsBlockedRead(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			runtime.run(ctx)
			close(done)
		}()
		synctest.Wait()
		select {
		case <-done:
			require.FailNow(t, "runtime stopped before its parent was canceled")
		default:
		}

		cancel()
		// No time passes in the bubble, so the blocked read ends at once.
		synctest.Wait()
		select {
		case <-done:
		default:
			require.FailNow(t, "runtime did not stop a blocked read after parent cancellation")
		}
	})
}

// TestConnectionRuntimePongExtendsReadDeadline lets a peer answer one ping
// 17 seconds in: the read deadline moves to pongWait after the pong instead
// of pongWait after the start. A frame over MaxMessageBytes closes the
// connection as too big, while one of exactly that size is read.
func TestConnectionRuntimePongExtendsReadDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		runtime, peer := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), nil)
		peer.listen()
		done := make(chan struct{})
		go func() {
			runtime.readPump(context.Background())
			close(done)
		}()
		const pongAfter = 17 * time.Second
		time.Sleep(pongAfter)
		require.NoError(t, peer.write(gorilla.PongMessage, []byte("client payload")))
		time.Sleep(pongWait - pongAfter)
		synctest.Wait()
		select {
		case <-done:
			require.FailNow(t, "the pong did not extend the initial read deadline")
		default:
		}
		time.Sleep(pongAfter - time.Nanosecond)
		synctest.Wait()
		select {
		case <-done:
			require.FailNow(t, "read deadline ended before pongWait after the pong")
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		select {
		case <-done:
		default:
			require.FailNow(t, "read deadline did not end pongWait after the pong")
		}
	})

	synctest.Test(t, func(t *testing.T) {
		runtime, peer := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), nil)
		peer.listen()
		done := make(chan struct{})
		go func() {
			runtime.readPump(context.Background())
			close(done)
		}()
		require.NoError(t, peer.write(gorilla.TextMessage, pingRequestOfSize(1, MaxMessageBytes)))
		pong := nextInboundResponse(t, runtime)
		require.Equal(t, "ok", pong.Status, "a frame of MaxMessageBytes is read")
		go func() { _ = peer.write(gorilla.TextMessage, pingRequestOfSize(2, MaxMessageBytes+1)) }()
		synctest.Wait()
		select {
		case <-done:
		default:
			require.FailNow(t, "read responsibility did not stop after an oversized frame")
		}
		require.Equal(t, []peerClose{{code: gorilla.CloseMessageTooBig}}, peer.closeFrames(), "oversized frame close")
	})
}

func TestConnectionRuntimeOwnsAuthorizedSubscriptionSet(t *testing.T) {
	t.Parallel()

	world := newAdmissionWorld(t, admissionOptions{})
	runtime, _ := newInboundRuntime(t, world, nil)
	runtime.metadata = model.RequestMetadata{
		RequestID: "upgrade-request",
		IPAddress: "192.0.2.10",
		UserAgent: "proctor-test",
	}
	subscription := validInboundSubscription()

	runtime.handleRequest(context.Background(), requestWithData(t, 7, "subscribe", subscription))
	response := nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status)
	require.Equal(t, int64(7), response.Sequence)
	require.Nil(t, response.Error)
	require.True(t, runtime.hasSubscription(subscription), "authorized subscription is not visible to publication")

	runtime.handleRequest(context.Background(), requestWithData(t, 8, "subscribe", subscription))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status)
	require.Equal(t, int64(8), response.Sequence)
	runtime.mu.Lock()
	if got := len(runtime.subscriptions); got != 1 {
		runtime.mu.Unlock()
		require.FailNow(t, fmt.Sprintf("subscription count after duplicate = %d, want 1", got))
	}
	for len(runtime.subscriptions) < maximumSubscriptions {
		candidate := validInboundSubscription()
		runtime.subscriptions[candidate.Key()] = candidate
	}
	runtime.mu.Unlock()

	overLimit := validInboundSubscription()
	runtime.handleRequest(context.Background(), requestWithData(t, 9, "subscribe", overLimit))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "error", response.Status)
	require.Equal(t, int64(9), response.Sequence)
	require.NotNil(t, response.Error)
	require.Equal(t, "websocket.subscription.limit", response.Error.Code)
	require.Equal(t, "WebSocket subscription limit reached.", response.Error.Message)
	require.False(t, runtime.hasSubscription(overLimit), "over-limit subscription was retained")

	runtime.handleRequest(context.Background(), requestWithData(t, 10, "subscribe", subscription))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status)
	require.Equal(t, int64(10), response.Sequence)

	runtime.handleRequest(context.Background(), requestWithData(t, 11, "unsubscribe", subscription))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status, "unsubscribe response = %#v, still subscribed", response)
	require.Equal(t, int64(11), response.Sequence, "unsubscribe response = %#v, still subscribed", response)
	require.False(t, runtime.hasSubscription(subscription), "unsubscribe response = %#v, still subscribed", response)

	// Every subscribe, the over-limit one included, took a recorded decision
	// for the connection's principal and the upgrade request's client, under
	// a request ID naming the connection and the command's sequence.
	decisions := world.access.Audits.Decisions()
	require.Len(t, decisions, 4, "authorization decisions")
	first := decisions[0]
	require.Equal(t, runtime.principal.UserID, first.ActorID)
	require.Equal(t, runtime.id+":7", first.RequestID)
	require.Equal(t, runtime.metadata.IPAddress, first.IPAddress)
	require.Equal(t, runtime.metadata.UserAgent, first.UserAgent)
	require.Equal(t, string(subscription.Action), first.Action)
	require.Equal(t, subscription.Resource.model(), first.Resource)
	require.Equal(t, model.AuditStatusSuccess, first.Status)
}

func TestConnectionRuntimeConnectsExamAttemptAndOwnsCandidateSubscription(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), f.service)
	sittingID := f.sitting.ID
	credential := model.NewCredentialToken()
	connectRequest := validExamAttemptConnectRequest(t, sittingID, "admit-once", credential)
	runtime.handleRequest(context.Background(), requestWithData(t, 30, "exam_attempt.connect", connectRequest))
	response := nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status, "connect response = %#v error", response)
	require.Nil(t, response.Error, "connect response = %#v error", response)
	admitted := f.store.log()
	require.Len(t, admitted.connects, 1)
	admission := admitted.connects[0]
	var body examAttemptConnectResponse
	err := json.Unmarshal(response.Data, &body)
	require.NoError(t, err)
	require.Equal(t, admission.AttemptID.String(), body.AttemptID)
	require.Equal(t, admission.WorkspaceID.String(), body.WorkspaceID)
	require.Equal(t, admission.ParticipationID.String(), body.ParticipationID)
	require.Equal(t, admission.ConnectionID.String(), body.AttemptConnectionID)
	require.Equal(t, int64(1), body.Generation)
	require.Equal(t, int64(5), body.RenewalIntervalSeconds)
	require.True(t, body.FirstAdmission)
	for _, secret := range []string{credential, model.HashToken(credential), "continuity_credential", "credential_hash"} {
		require.NotContains(t, string(response.Data), secret, "connect response exposed")
	}
	candidate := Subscription{Action: model.ActionExamSittingParticipate,
		Resource: Resource{Type: model.ResourceExamSitting, ID: sittingID.String()}}
	require.True(t, runtime.hasSubscription(candidate), "candidate relationship subscription was not installed")
	require.Equal(t, sittingID, admission.SittingID)
	require.Equal(t, runtime.principal.UserID, admission.CandidateUserID)
	require.Equal(t, runtime.principal.SessionID, admission.SessionID)
	require.Equal(t, sha256.Sum256([]byte("admit-once")), admitted.connectCommands[0].KeyDigest, "the request's idempotency key")
	require.Equal(t, model.HashToken(credential), admission.ContinuityCredentialHash, "the request's continuity credential")
	runtime.handleRequest(context.Background(), requestWithData(t, 31, "exam_attempt.connect", connectRequest))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status)
	require.Nil(t, response.Error)

	runtime.handleRequest(context.Background(), requestWithData(t, 32, "unsubscribe", candidate))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status, "protected candidate unsubscribe = %#v, subscribed", response)
	require.True(t, runtime.hasSubscription(candidate), "protected candidate unsubscribe = %#v, subscribed", response)

	runtime.handleRequest(context.Background(), requestWithData(t, 33, "exam_attempt.connect",
		validExamAttemptConnectRequest(t, model.NewExamSittingID(), "another-admission", model.NewCredentialToken())))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "error", response.Status)
	require.NotNil(t, response.Error)
	require.Equal(t, "exam.attempt.already_connected", response.Error.Code)
	if got := len(f.store.log().connects); got != 2 {
		require.FailNow(t, fmt.Sprintf("durable connect calls after exact replay and rejected binding = %d, want 2", got))
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	runtime.finalizeExamAttempt(canceled)
	runtime.finalizeExamAttempt(context.Background())
	require.False(t, runtime.hasSubscription(candidate), "candidate subscription survived transport finalization")
	closed := f.store.log()
	require.Len(t, closed.closes, 1)
	require.Equal(t, admission.ConnectionID, closed.closes[0].ConnectionID)
	require.Equal(t, model.AttemptConnectionCloseTransport, closed.closes[0].Reason)
	require.Equal(t, runtime.principal.UserID, closed.closes[0].CandidateUserID)
	require.Equal(t, runtime.principal.SessionID, closed.closes[0].SessionID)
	require.NoError(t, closed.closeContextErrs[0], "transport close ran under the canceled context")
	audit, value := f.audited(t, "exam_attempt_connection_close")
	require.Equal(t, admission.AttemptID.String(), value["exam_attempt_id"])
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()}, audit.Resource)
	require.Equal(t, f.sitting.ClassID.String(), audit.ScopeID)
	require.Equal(t, runtime.principal.UserID, audit.ActorID)
	require.Equal(t, runtime.principal.SessionID, audit.SessionID)
	require.Equal(t, runtime.id+":attempt-close", audit.RequestID)
}

func TestConnectionRuntimeRejectsNonStrictExamAttemptConnectPayload(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), f.service)
	runtime.handleRequest(context.Background(), &Request{Sequence: 31, Action: "exam_attempt.connect",
		Data: json.RawMessage(`{"exam_sitting_id":"bad","idempotency_key":"one","idempotency_key":"two","continuity_credential":"secret"}`)})
	response := nextInboundResponse(t, runtime)
	require.Equal(t, "error", response.Status)
	require.NotNil(t, response.Error)
	require.Equal(t, "websocket.request.invalid", response.Error.Code)
	require.Empty(t, f.store.log().connects, "a non-strict payload reached admission")
}

func TestConnectionRuntimeRenewsBoundParticipationWithoutTreatingPingAsRenewal(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	f.store.generation = 4
	databaseNow := f.at.Add(5 * time.Second)
	f.store.databaseTime = databaseNow
	runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), f.service)
	credential := model.NewCredentialToken()
	runtime.handleRequest(context.Background(), requestWithData(t, 40, examAttemptConnectAction,
		validExamAttemptConnectRequest(t, f.sitting.ID, "connect", credential)))
	require.Equal(t, "ok", nextInboundResponse(t, runtime).Status)
	admission := f.store.log().connects[0]
	runtime.handleRequest(context.Background(), &Request{Sequence: 41, Action: "ping", Data: json.RawMessage(`{}`)})
	require.Equal(t, "ok", nextInboundResponse(t, runtime).Status)
	if len(f.store.log().renewals) != 0 {
		require.FailNow(t, "WebSocket ping renewed the Participation")
	}
	runtime.handleRequest(context.Background(), requestWithData(t, 42, examAttemptRenewAction, examAttemptRenewRequest{SecurityControl: securityControlFixture(),
		Generation: 4, Sequence: 1, ContinuityCredential: credential,
	}))
	response := nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status)
	require.Nil(t, response.Error)
	var body examAttemptRenewResponse
	err := json.Unmarshal(response.Data, &body)
	require.NoError(t, err)
	require.Equal(t, int64(4), body.Generation)
	require.Equal(t, int64(1), body.AcceptedSequence)
	require.Equal(t, databaseNow.Format(time.RFC3339Nano), body.DatabaseTime)
	require.Equal(t, databaseNow.Add(model.AttemptParticipationInitialLease).Format(time.RFC3339Nano), body.LeaseExpiresAt)
	for _, secret := range []string{credential, model.HashToken(credential), "continuity_credential", "credential_hash"} {
		require.NotContains(t, string(response.Data), secret, "renew response exposed")
	}
	renewals := f.store.log().renewals
	require.Len(t, renewals, 1)
	require.Equal(t, admission.AttemptID, renewals[0].AttemptID)
	require.Equal(t, admission.ParticipationID, renewals[0].ParticipationID)
	require.Equal(t, admission.ConnectionID, renewals[0].ConnectionID)
	require.Equal(t, int64(4), renewals[0].Generation)
	require.Equal(t, int64(1), renewals[0].Sequence)
	require.Equal(t, model.HashToken(credential), renewals[0].ContinuityCredentialHash)
}

func TestConnectionRuntimeBrowserActivityUsesAttemptBindingAndClosedWireContracts(t *testing.T) {
	t.Parallel()

	f := newAttemptFixture(t)
	// An earlier batch already delivered event 4 of this source.
	f.store.browserSeen = 4
	sourceID := model.BrowserSourceSessionID("018f47a0-6e53-4cc4-9d0b-97c9b6d98011")
	credential := model.NewCredentialToken()
	runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), f.service)
	binding := f.admit(runtime, 2)

	start := examAttemptBrowserStartRequest{Generation: 2, PolicyRevisionID: model.NewExamRevisionID().String(), PolicyDigest: "sha256:" + strings.Repeat("a", 64), Transition: model.BrowserStartTransition{Kind: "initial"},
		ContinuityCredential: credential, ParticipationID: binding.participationID.String(), SourceSessionID: string(sourceID)}
	runtime.handleRequest(context.Background(), requestWithData(t, 40, examAttemptBrowserStartAction, start))
	response := nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status)
	require.Nil(t, response.Error)
	var startAck examAttemptBrowserAcknowledgementResponse
	require.NoError(t, json.Unmarshal(response.Data, &startAck))
	require.Equal(t, string(sourceID), startAck.SourceSessionID)
	require.Equal(t, int64(0), startAck.HighestContiguous)
	require.Equal(t, int64(0), startAck.HighestSeen)
	require.NotNil(t, startAck.MissingRanges)
	require.Equal(t, f.at.Format(time.RFC3339Nano), startAck.ServerTime)
	require.NotContains(t, string(response.Data), credential, "Browser Activity start exposed continuity credential")

	eventDocument, err := json.Marshal(examAttemptBrowserEventRequest{Sequence: 1, Kind: string(model.BrowserActivityOpened),
		PolicyRevisionID: start.PolicyRevisionID, ClientOccurredAt: f.at.Format(time.RFC3339Nano)})
	require.NoError(t, err)
	appendRequest := examAttemptBrowserAppendRequest{Generation: 2, PolicyRevisionID: start.PolicyRevisionID, PolicyDigest: start.PolicyDigest,
		ContinuityCredential: credential, ParticipationID: binding.participationID.String(), SourceSessionID: string(sourceID),
		Events: []json.RawMessage{eventDocument}}
	runtime.handleRequest(context.Background(), requestWithData(t, 41, examAttemptBrowserAppendAction, appendRequest))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status)
	require.Nil(t, response.Error)
	var appendAck examAttemptBrowserAcknowledgementResponse
	err = json.Unmarshal(response.Data, &appendAck)
	require.NoError(t, err)
	require.Equal(t, int64(1), appendAck.HighestContiguous)
	require.Equal(t, int64(4), appendAck.HighestSeen)
	require.Len(t, appendAck.MissingRanges, 1)
	require.Equal(t, browserActivityMissingRangeResponse{First: 2, Last: 3}, appendAck.MissingRanges[0])

	delivered := f.store.log()
	startCalls, appendCalls := delivered.browserStarts, delivered.browserAppends
	require.Len(t, startCalls, 1)
	require.Equal(t, binding.attemptID, startCalls[0].Access.AttemptID)
	require.Equal(t, binding.connectionID, startCalls[0].Access.ConnectionID)
	require.Equal(t, binding.participationID, startCalls[0].ParticipationID)
	require.Equal(t, int64(2), startCalls[0].Generation)
	require.Equal(t, model.HashToken(credential), startCalls[0].Access.ContinuityCredentialHash)
	require.Equal(t, sourceID, startCalls[0].SourceSessionID)
	require.Len(t, appendCalls, 1)
	require.Equal(t, binding.attemptID, appendCalls[0].Access.AttemptID)
	require.Equal(t, binding.connectionID, appendCalls[0].Access.ConnectionID)
	require.Equal(t, binding.participationID, appendCalls[0].ParticipationID)
	require.Equal(t, int64(2), appendCalls[0].Generation)
	require.Equal(t, model.HashToken(credential), appendCalls[0].Access.ContinuityCredentialHash)
	require.Equal(t, sourceID, appendCalls[0].SourceSessionID)
	require.Len(t, appendCalls[0].Events, 1)
	require.Equal(t, model.BrowserActivityOpened, appendCalls[0].Events[0].Kind)

	stale := appendRequest
	stale.Generation = 3
	runtime.handleRequest(context.Background(), requestWithData(t, 42, examAttemptBrowserAppendAction, stale))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "error", response.Status)
	require.NotNil(t, response.Error)
	require.Equal(t, "exam.attempt.connection_closed", response.Error.Code)

	// A refusal the Store reports reaches the candidate under its own code
	// with the Browser Activity message.
	f.store.mu.Lock()
	f.store.appendErr = store.NewErrConflict("browser_activity", "browser_activity_sequence", nil)
	f.store.mu.Unlock()
	runtime.handleRequest(context.Background(), requestWithData(t, 43, examAttemptBrowserAppendAction, appendRequest))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "error", response.Status)
	require.NotNil(t, response.Error)
	require.Equal(t, "exam.attempt.browser_activity_conflict", response.Error.Code)
	require.Equal(t, "Browser activity could not be accepted.", response.Error.Message)
}

func TestDecodeBrowserActivityAppendAcceptsSafeBlockedLocations(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 29, 10, 0, 0, 0, time.UTC)
	revisionID := model.NewExamRevisionID().String()
	blocked := []examAttemptBrowserEventRequest{
		{Sequence: 1, Kind: string(model.BrowserActivityBlockedNavigation), PolicyRevisionID: revisionID,
			ClientOccurredAt: at.Format(time.RFC3339Nano), Location: json.RawMessage(`{"scheme":"http","host":"example.edu","path":"/outside"}`),
			BlockReason: json.RawMessage(`"scheme_not_allowed"`)},
		{Sequence: 2, Kind: string(model.BrowserActivityBlockedNavigation), PolicyRevisionID: revisionID,
			ClientOccurredAt: at.Add(time.Millisecond).Format(time.RFC3339Nano), Location: json.RawMessage(`{"scheme":"file","host":"","path":""}`),
			BlockReason: json.RawMessage(`"scheme_not_allowed"`)},
		{Sequence: 3, Kind: string(model.BrowserActivityBlockedNavigation), PolicyRevisionID: revisionID,
			ClientOccurredAt: at.Add(2 * time.Millisecond).Format(time.RFC3339Nano), Location: json.RawMessage(`{"scheme":"","host":"","path":""}`),
			BlockReason: json.RawMessage(`"invalid_url"`)},
	}
	documents := make([]json.RawMessage, len(blocked))
	for index := range blocked {
		encoded, err := json.Marshal(blocked[index])
		require.NoError(t, err)
		documents[index] = encoded
	}
	request := examAttemptBrowserAppendRequest{Generation: 1, PolicyRevisionID: revisionID, PolicyDigest: "sha256:" + strings.Repeat("a", 64),
		ContinuityCredential: model.NewCredentialToken(), ParticipationID: model.NewAttemptParticipationID().String(),
		SourceSessionID: "018f47a0-6e53-4cc4-9d0b-97c9b6d98011", Events: documents}
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	_, events, err := decodeExamAttemptBrowserAppendRequest(encoded)
	require.NoError(t, err, "decodeExamAttemptBrowserAppendRequest() events")
	require.Len(t, events, 3, "decodeExamAttemptBrowserAppendRequest() events")
	require.NotNil(t, events[0].Location, "decodeExamAttemptBrowserAppendRequest() events")
	require.Equal(t, "http", events[0].Location.Scheme, "decodeExamAttemptBrowserAppendRequest() events")
	require.NotNil(t, events[1].Location, "decodeExamAttemptBrowserAppendRequest() events")
	require.Equal(t, "file", events[1].Location.Scheme, "decodeExamAttemptBrowserAppendRequest() events")
	require.NotNil(t, events[2].Location, "decodeExamAttemptBrowserAppendRequest() events")
	require.Equal(t, model.BrowserLocation{}, *events[2].Location, "decodeExamAttemptBrowserAppendRequest() events")

	unsafe := blocked[1]
	unsafe.Location = json.RawMessage(`{"scheme":"javascript","host":"","path":"/alert(secret)"}`)
	unsafeDocument, err := json.Marshal(unsafe)
	require.NoError(t, err)
	request.Events = []json.RawMessage{unsafeDocument}
	encoded, err = json.Marshal(request)
	require.NoError(t, err)
	_, _, err = decodeExamAttemptBrowserAppendRequest(encoded)
	require.Error(t, err, "decodeExamAttemptBrowserAppendRequest() accepted a retained hostless-scheme payload")

	invalidLocations := []struct {
		eventIndex int
		location   json.RawMessage
	}{
		{2, json.RawMessage(`{}`)},
		{2, json.RawMessage(`{"scheme":"file"}`)},
		{2, json.RawMessage(`{"scheme":"","host":""}`)},
		{2, json.RawMessage(`{"scheme":null,"host":"","path":""}`)},
		{0, json.RawMessage(`{"scheme":"http","host":"example.edu","port":null,"path":"/"}`)},
		{0, json.RawMessage(`{"scheme":"http","host":"example.edu","port":"","path":"/"}`)},
	}
	for _, test := range invalidLocations {
		t.Run(fmt.Sprintf("eventIndex=%v", test.eventIndex), func(t *testing.T) {
			incomplete := blocked[test.eventIndex]
			incomplete.Location = test.location
			incompleteDocument, marshalErr := json.Marshal(incomplete)
			require.NoError(t, marshalErr)
			request.Events = []json.RawMessage{incompleteDocument}
			encoded, marshalErr = json.Marshal(request)
			require.NoError(t, marshalErr)
			_, _, err = decodeExamAttemptBrowserAppendRequest(encoded)
			require.Error(t, err, "decodeExamAttemptBrowserAppendRequest() accepted incomplete location %s", test.location)
		})
	}
}

func TestConnectionRuntimeRenewalUsesStrictPayloadAndSafeConnectionLossMessage(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), f.service)
	runtime.handleRequest(context.Background(), &Request{Sequence: 50, Action: examAttemptRenewAction,
		Data: json.RawMessage(`{"generation":1,"sequence":1,"sequence":2,"continuity_credential":"bad"}`)})
	response := nextInboundResponse(t, runtime)
	require.Equal(t, "error", response.Status)
	require.NotNil(t, response.Error)
	require.Equal(t, "websocket.request.invalid", response.Error.Code)
	require.Empty(t, f.store.log().renewals, "a non-strict renewal reached the Store")
	// The Participation's lease lapsed before the renewal arrived.
	f.store.renewErr = store.NewErrConflict("attempt_participation", "attempt_participation_expired", nil)
	f.admit(runtime, 1)
	credential := model.NewCredentialToken()
	runtime.handleRequest(context.Background(), requestWithData(t, 51, examAttemptRenewAction, examAttemptRenewRequest{SecurityControl: securityControlFixture(),
		Generation: 1, Sequence: 2, ContinuityCredential: credential,
	}))
	response = nextInboundResponse(t, runtime)
	require.Equal(t, "error", response.Status)
	require.NotNil(t, response.Error)
	require.Equal(t, "exam.attempt.connection_lost", response.Error.Code)
	require.Equal(t, "Secure connectivity could not be renewed. Ask a manager to re-allow access.", response.Error.Message)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), credential)
	require.NotContains(t, string(encoded), model.HashToken(credential))
	require.Len(t, f.store.log().renewals, 1)
}

func TestConnectionRuntimeSubmitsBoundFocusLossAndClearsPolicySuspendedBinding(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	f.store.generation = 4
	receivedAt := f.at.Add(2 * time.Second)
	f.store.databaseTime = receivedAt
	runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), f.service)
	credential := model.NewCredentialToken()
	runtime.handleRequest(context.Background(), requestWithData(t, 60, examAttemptConnectAction,
		validExamAttemptConnectRequest(t, f.sitting.ID, "connect", credential)))
	require.Equal(t, "ok", nextInboundResponse(t, runtime).Status)
	admission := f.store.log().connects[0]
	runtime.handleRequest(context.Background(), requestWithData(t, 61, examAttemptFocusLossAction, examAttemptFocusLossRequest{
		SchemaVersion: model.FocusLossSignalSchemaVersion,
		Generation:    4, Sequence: 3, DurationMilliseconds: 2500, Source: "fullscreen_exited",
		ContinuityCredential: credential,
	}))
	response := nextInboundResponse(t, runtime)
	require.Equal(t, "ok", response.Status)
	require.Nil(t, response.Error)
	var body examAttemptFocusLossResponse
	err := json.Unmarshal(response.Data, &body)
	require.NoError(t, err, "Focus Loss body=%#v error", body)
	require.Equal(t, int64(4), body.Generation, "Focus Loss body=%#v error", body)
	require.Equal(t, int64(3), body.AcceptedSequence, "Focus Loss body=%#v error", body)
	require.Equal(t, receivedAt.Format(time.RFC3339Nano), body.ReceivedAt, "Focus Loss body=%#v error", body)
	require.True(t, body.SuspensionCreated, "Focus Loss body=%#v error", body)
	encoded := string(response.Data)
	for _, forbidden := range []string{credential, model.HashToken(credential), "duration", "source", "outcome", "threshold", "qualified", "flag_created", "session"} {
		require.NotContains(t, strings.ToLower(encoded), strings.ToLower(forbidden), "Focus Loss response exposed")
	}
	// The Store's signal carries no schema version: the service refuses any
	// claim whose version is not model.FocusLossSignalSchemaVersion before
	// it gets there.
	signals := f.store.log().focusSignals
	require.Len(t, signals, 1)
	require.Equal(t, admission.AttemptID, signals[0].Access.AttemptID)
	require.Equal(t, admission.ParticipationID, signals[0].Access.ParticipationID)
	require.Equal(t, admission.ConnectionID, signals[0].Access.ConnectionID)
	require.Equal(t, int64(4), signals[0].Access.Generation)
	require.Equal(t, int64(3), signals[0].Sequence)
	require.Equal(t, int64(2500), signals[0].DurationMilliseconds)
	require.Equal(t, model.FocusLossSourceFullscreenExited, signals[0].Source)
	require.Equal(t, model.HashToken(credential), signals[0].Access.ContinuityCredentialHash)

	runtime.handleRequest(context.Background(), requestWithData(t, 62, examAttemptRenewAction, examAttemptRenewRequest{SecurityControl: securityControlFixture(),
		Generation: 4, Sequence: 4, ContinuityCredential: credential,
	}))
	rejected := nextInboundResponse(t, runtime)
	require.Equal(t, "error", rejected.Status)
	require.NotNil(t, rejected.Error)
	require.Equal(t, "exam.attempt.connection_closed", rejected.Error.Code)
	runtime.handleRequest(context.Background(), requestWithData(t, 63, examAttemptFocusLossAction, examAttemptFocusLossRequest{
		SchemaVersion: model.FocusLossSignalSchemaVersion,
		Generation:    4, Sequence: 4, DurationMilliseconds: 2500, ContinuityCredential: credential,
	}))
	rejected = nextInboundResponse(t, runtime)
	require.Equal(t, "error", rejected.Status)
	require.NotNil(t, rejected.Error)
	require.Equal(t, "exam.attempt.connection_closed", rejected.Error.Code)
	runtime.mu.Lock()
	binding, subscriptions := runtime.attempt, len(runtime.subscriptions)
	runtime.mu.Unlock()
	reached := f.store.log()
	require.Empty(t, reached.renewals, "post-suspension renewals")
	require.Len(t, reached.focusSignals, 1, "post-suspension renewals")
	require.Nil(t, binding, "post-suspension renewals")
	require.Equal(t, 0, subscriptions, "post-suspension renewals")
}

func TestConnectionRuntimePreservesInboundErrorContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		request     func(*testing.T, *admissionWorld) *Request
		options     admissionOptions
		wantCode    string
		wantMessage string
	}{
		{
			name: "invalid subscription",
			request: func(t *testing.T, _ *admissionWorld) *Request {
				return requestWithData(t, 21, "subscribe", Subscription{})
			},
			wantCode:    "websocket.subscription.invalid",
			wantMessage: "Invalid subscription.",
		},
		{
			name: "invalid unsubscribe",
			request: func(t *testing.T, _ *admissionWorld) *Request {
				return requestWithData(t, 22, "unsubscribe", Subscription{})
			},
			wantCode:    "websocket.subscription.invalid",
			wantMessage: "Invalid subscription.",
		},
		{
			name: "authorization denial",
			request: func(t *testing.T, _ *admissionWorld) *Request {
				return requestWithData(t, 23, "subscribe", validInboundSubscription())
			},
			options:     admissionOptions{withoutClassView: true},
			wantCode:    "authorization.denied",
			wantMessage: "WebSocket subscription denied.",
		},
		{
			name: "authorization failure",
			request: func(t *testing.T, world *admissionWorld) *Request {
				return requestWithData(t, 25, "subscribe", world.unreadableClass())
			},
			wantCode:    "authorization.unavailable",
			wantMessage: "WebSocket subscription failed.",
		},
		{
			name: "missing class subscription",
			request: func(t *testing.T, world *admissionWorld) *Request {
				return requestWithData(t, 27, "subscribe", world.missingClass())
			},
			wantCode:    "resource.not_found",
			wantMessage: "WebSocket subscription failed.",
		},
		{
			name: "unknown action",
			request: func(*testing.T, *admissionWorld) *Request {
				return &Request{Sequence: 26, Action: "future-command"}
			},
			wantCode:    "websocket.action.unknown",
			wantMessage: "Unknown WebSocket action.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			world := newAdmissionWorld(t, test.options)
			runtime, _ := newInboundRuntime(t, world, nil)
			request := test.request(t, world)

			runtime.handleRequest(context.Background(), request)

			response := nextInboundResponse(t, runtime)
			require.Equal(t, "error", response.Status, "response = %#v, want code %q and message", response, test.wantCode)
			require.Equal(t, request.Sequence, response.Sequence, "response = %#v, want code %q and message", response, test.wantCode)
			require.NotNil(t, response.Error, "response = %#v, want code %q and message", response, test.wantCode)
			require.Equal(t, test.wantCode, response.Error.Code, "response = %#v, want code %q and message", response, test.wantCode)
			require.Equal(t, test.wantMessage, response.Error.Message, "response = %#v, want code %q and message", response, test.wantCode)
			runtime.mu.Lock()
			count := len(runtime.subscriptions)
			runtime.mu.Unlock()
			require.Equal(t, 0, count, "failed command retained")
		})
	}
	// Real admission returns only application errors, so no row above
	// reaches the fallback for an untyped one; the mapping is tested directly.
	t.Run("untyped authorization error", func(t *testing.T) {
		t.Parallel()
		code, presentation := subscriptionFailure(errors.New("policy lookup denied"))
		require.Equal(t, apperror.Code("authorization.denied"), code)
		require.Equal(t, "WebSocket subscription denied.", websocketErrorMessage(presentation).fallback)
	})
}

func TestConnectionRuntimeRevokesInvalidPrincipal(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		world := newAdmissionWorld(t, admissionOptions{disabled: true})
		runtime, peer := newInboundRuntime(t, world, nil)
		peer.listen()
		done := make(chan struct{})
		go func() {
			runtime.sessionPump(context.Background())
			close(done)
		}()

		time.Sleep(sessionCheck)
		synctest.Wait()
		select {
		case <-done:
		default:
			require.FailNow(t, "principal-validation responsibility did not stop")
		}
		require.Equal(t, []peerClose{{code: CloseSessionRevoked, reason: "session no longer valid"}}, peer.closeFrames(), "close")
		require.True(t, peer.ended(), "invalid principal did not close the connection")
		runtime.mu.Lock()
		replayable := runtime.replayable
		runtime.mu.Unlock()
		require.False(t, replayable, "session-revoked connection remained replayable")
		// One revalidation read the principal's Session and found its User
		// disabled.
		sessions := world.sessions.read()
		require.Len(t, sessions, 1)
		require.Equal(t, runtime.principal.SessionID, sessions[0].id)
		require.Equal(t, []model.UserID{runtime.principal.UserID}, world.users.read())
	})
}

func TestConnectionRuntimeSubscriptionMembershipIsConcurrentSafe(t *testing.T) {
	t.Parallel()

	runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), nil)
	runtime.send = make(chan outboundMessage, 2048)
	subscription := validInboundSubscription()
	subscribe := requestWithData(t, 31, "subscribe", subscription)
	unsubscribe := requestWithData(t, 32, "unsubscribe", subscription)

	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 1000 {
				_ = runtime.hasSubscription(subscription)
			}
		}()
	}
	for range 500 {
		runtime.handleRequest(context.Background(), subscribe)
		runtime.handleRequest(context.Background(), unsubscribe)
	}
	readers.Wait()
	runtime.handleRequest(context.Background(), subscribe)

	require.True(t, runtime.hasSubscription(subscription), "final authorized subscription is not visible to publication")
}

func TestConnectionRuntimeJoinsSimultaneousInboundTermination(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		for iteration := range 20 {
			runtime, peer := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{disabled: true}), nil)
			peer.listen()
			done := make(chan struct{})
			go func() {
				runtime.run(context.Background())
				close(done)
			}()
			// The peer hangs up at the instant the revalidation fails.
			time.AfterFunc(sessionCheck, peer.hangUp)
			time.Sleep(sessionCheck)
			synctest.Wait()
			select {
			case <-done:
			default:
				require.FailNow(t, fmt.Sprintf("iteration %d: simultaneous termination leaked a responsibility", iteration))
			}
		}
	})
}

func validExamAttemptConnectRequest(t *testing.T, sittingID model.ExamSittingID, key, credential string) examAttemptConnectRequest {
	t.Helper()
	manifest := model.CurrentAttemptConfigurationManifestFingerprint()
	configuration := model.AttemptConfigurationCandidate{ManifestFingerprint: manifest, RegistryFingerprint: "fnv1a64:" + strings.Repeat("b", 16), UserSettingsRevision: model.NewUserSettingsRevision(), DesktopBuild: "test-build", DesktopTarget: "darwin-arm64", Presentation: model.AttemptConfigurationPresentation{ColorTheme: "dark", ZoomPercent: 100, EditorFontSizePX: 14, EditorLineHeightPX: 22, ScreenReaderMode: "auto", AnnouncementMode: "auto", CursorStyle: "line", CursorBlinking: "blink"}, ApprovedCommands: []string{}, ApprovedKeybindings: []string{}}
	err := configuration.Validate()
	require.NoError(t, err)
	return examAttemptConnectRequest{Security: model.ConnectSecurity{Kind: "preflight", PreflightID: "fixture-preflight", ReportDigest: model.SHA256Fingerprint([]byte("fixture-report"))}, ExamSittingID: sittingID.String(), IdempotencyKey: key,
		ContinuityCredential: credential, ConfigurationManifestFingerprint: manifest,
		InitialConfiguration: &configuration}
}

func securityControlFixture() model.SecurityControl {
	return model.SecurityControl{ControlSequence: 1, PolicyDigest: model.SHA256Fingerprint([]byte("renewal-policy")), SecuritySessionID: model.NewID(), StreamID: model.NewID(), DeliveryWatermarks: []model.DeliveryWatermark{}}
}
func securityControlResultFixture() model.SecurityControlResult {
	digest := model.SHA256Fingerprint([]byte("renewal-control"))
	return model.SecurityControlResult{ProcessedControlSequence: 1, ProcessedControlDigest: &digest, SecurityInteractionAllowed: true, DeliveryWatermarkRejections: []model.DeliveryWatermarkRejection{}}
}

type deliveryRecoveryTestError struct {
	error
	value *model.DeliveryRecovery
}

func (e *deliveryRecoveryTestError) DeliveryRecovery() *model.DeliveryRecovery { return e.value }
func TestDeliveryErrorHasBoundedPrivateRecoveryAndRetryDelay(t *testing.T) {
	for _, name := range []string{"valid", "invalid", "unrelated"} {
		t.Run(name, func(t *testing.T) {
			at := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
			runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), nil)
			state := &model.DeliveryRecovery{Family: "browser", Budget: model.DeliveryBudgetSnapshot{ParticipationID: model.NewAttemptParticipationID(), Generation: 1, ServerTime: at, Native: model.DeliveryFamilyBudget{Participation: model.NewDeliveryQuotaUsage(true, false), Attempt: model.NewDeliveryQuotaUsage(true, true), PendingByteLimit: model.DeliveryPendingByteLimit}, Browser: model.DeliveryFamilyBudget{Participation: model.NewDeliveryQuotaUsage(false, false), Attempt: model.NewDeliveryQuotaUsage(false, true), PendingByteLimit: model.DeliveryPendingByteLimit}}}
			code := apperror.Code("exam.delivery.pending_capacity")
			if name == "invalid" {
				state.Budget.Native.PendingBytes = -1
			}
			if name == "unrelated" {
				code = "exam.attempt.not_found"
			}
			runtime.enqueueDeliveryError(3, code, apperror.New(code).Wrap(&deliveryRecoveryTestError{error: errors.New("private-native-payload"), value: state}))
			response := nextInboundResponse(t, runtime)
			require.NotNil(t, response.Error, "invalid delivery error extension")
			require.Equal(t, int64(3), response.Sequence, "invalid delivery error extension")
			require.Equal(t, name == "valid", response.Error.Delivery != nil, "invalid delivery error extension")
			if name != "unrelated" {
				require.Equal(t, 1, response.Error.RetryAfterSeconds, "missing retry delay")
			}
			raw, err := json.Marshal(response)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-native-payload", "private cause leaked")
		})
	}
}

// TestConnectionRuntimeAppendBackpressurePreservesRenewalAndCancelsWorker
// holds one Browser Activity append in the Store: a transport-level blocked
// append must not monopolize the renewal reader.
func TestConnectionRuntimeAppendBackpressurePreservesRenewalAndCancelsWorker(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newAttemptFixture(t)
		gate := newAppendGate()
		f.store.appendGate = gate
		runtime, peer := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), f.service)
		binding := f.admit(runtime, 1)
		credential, revision := model.NewCredentialToken(), model.NewExamRevisionID().String()
		event, err := json.Marshal(examAttemptBrowserEventRequest{Sequence: 1, Kind: string(model.BrowserActivityOpened), PolicyRevisionID: revision, ClientOccurredAt: f.at.Format(time.RFC3339Nano)})
		require.NoError(t, err)
		body := examAttemptBrowserAppendRequest{Generation: 1, PolicyRevisionID: revision, PolicyDigest: "sha256:" + strings.Repeat("a", 64),
			ContinuityCredential: credential, ParticipationID: binding.participationID.String(), SourceSessionID: "018f47a0-6e53-4cc4-9d0b-97c9b6d98011", Events: []json.RawMessage{event}}
		done := make(chan struct{})
		go func() { runtime.readPump(context.Background()); close(done) }()
		require.NoError(t, peer.send(requestWithData(t, 1, examAttemptBrowserAppendAction, body)))
		synctest.Wait()
		select {
		case <-gate.entered:
		default:
			require.FailNow(t, "append did not reach the Store")
		}
		// The second request occupies the single waiting slot; the third is refused.
		require.NoError(t, peer.send(requestWithData(t, 2, examAttemptBrowserAppendAction, body)))
		require.NoError(t, peer.send(requestWithData(t, 3, examAttemptBrowserAppendAction, body)))
		require.NoError(t, peer.send(requestWithData(t, 4, examAttemptRenewAction, examAttemptRenewRequest{Generation: 1, Sequence: 1, ContinuityCredential: credential, SecurityControl: securityControlFixture()})))
		refused := nextInboundResponse(t, runtime)
		require.Equal(t, int64(3), refused.Sequence)
		require.NotNil(t, refused.Error)
		require.Equal(t, "exam.delivery.append_rate_limited", refused.Error.Code)
		require.Equal(t, 1, refused.Error.RetryAfterSeconds)
		require.Nil(t, refused.Error.Delivery)
		renewed := nextInboundResponse(t, runtime)
		require.Equal(t, int64(4), renewed.Sequence)
		require.Equal(t, "ok", renewed.Status)
		peer.hangUp()
		synctest.Wait()
		select {
		case <-done:
		default:
			require.FailNow(t, "reader failed to join canceled append")
		}
		select {
		case <-gate.finished:
		default:
			require.FailNow(t, "reader returned before append cancellation completed")
		}
		require.Len(t, f.store.log().browserAppends, 1, "only the first append reached the Store")
	})
}

func TestControlRateRefusalHasRetryAdviceWithoutClosingBinding(t *testing.T) {
	runtime, _ := newInboundRuntime(t, newAdmissionWorld(t, admissionOptions{}), nil)
	binding := &examAttemptBinding{attemptID: model.NewExamAttemptID()}
	runtime.attempt = binding
	runtime.enqueueError(77, "exam.delivery.control_rate_limited", websocketErrorAttemptRenewalFailed)
	response := nextInboundResponse(t, runtime)
	require.Equal(t, int64(77), response.Sequence, "control retry refusal changed authority or lost retry advice")
	require.NotNil(t, response.Error, "control retry refusal changed authority or lost retry advice")
	require.Equal(t, 1, response.Error.RetryAfterSeconds, "control retry refusal changed authority or lost retry advice")
	require.Nil(t, response.Error.Delivery, "control retry refusal changed authority or lost retry advice")
	require.Same(t, binding, runtime.attempt, "control retry refusal changed authority or lost retry advice")
}
