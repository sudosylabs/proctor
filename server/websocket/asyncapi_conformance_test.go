// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package websocket

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/internal/asyncapidoc"
	"github.com/sudosylabs/proctor/server/model"
)

// protocolMessages are the AsyncAPI messages this package owns: everything on
// the channel that is not an application event. TestProtocolFramesMatchAsyncAPI
// sends a real frame for each one.
var protocolMessages = []string{
	"command_error",
	"exam_attempt.renew",
	"exam_attempt.security.update",
	"exam_attempt_renew_reply",
	"hello",
	"ping",
	"pong",
	"resync_required",
	"security_control_reply",
	"subscribe",
	"subscription_reply",
	"unsubscribe",
}

type asyncAPIContract struct {
	document map[string]any
	compiler *jsonschema.Compiler
}

func loadAsyncAPIContract(t *testing.T) asyncAPIContract {
	t.Helper()
	_, currentFile, _, ok := goruntime.Caller(0)
	require.True(t, ok)
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "asyncapi.json"))
	require.NoError(t, err)
	compiler, err := asyncapidoc.NewCompiler(encoded)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(encoded, &document))
	return asyncAPIContract{document: document, compiler: compiler}
}

func (c asyncAPIContract) messages() map[string]any {
	return c.document["components"].(map[string]any)["messages"].(map[string]any)
}

// requireFrame checks that a value, encoded as the socket writes it, is valid
// against the payload of the named message.
func (c asyncAPIContract) requireFrame(t *testing.T, key string, frame any) {
	t.Helper()
	encoded, err := json.Marshal(frame)
	require.NoError(t, err)
	schema, err := c.compiler.Compile(asyncapidoc.DocumentURL + "#/components/messages/" + key + "/payload")
	require.NoError(t, err, key)
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	require.NoError(t, err)
	require.NoError(t, schema.Validate(decoded), "%s frame %s", key, encoded)
}

func TestProtocolFramesMatchAsyncAPI(t *testing.T) {
	t.Parallel()

	contract := loadAsyncAPIContract(t)
	var owned []string
	for key, raw := range contract.messages() {
		event := false
		for _, tag := range raw.(map[string]any)["tags"].([]any) {
			event = event || tag.(map[string]any)["name"] == "event"
		}
		if !event {
			owned = append(owned, key)
		}
	}
	sort.Strings(owned)
	require.Equal(t, protocolMessages, owned, "every protocol message is checked below")

	application := &inboundTestApplication{}
	connection := newInboundRuntime(application, newInboundTestSocket(), newRuntimeTestClock(time.Now()))
	connection.nodeID = "node-a"

	connection.enqueueHello(false, true)
	contract.requireFrame(t, "hello", (<-connection.send).event)
	contract.requireFrame(t, "resync_required", (<-connection.send).event)

	subscription := Subscription{
		Action:   model.ActionExamSittingView,
		Resource: Resource{Type: model.ResourceExamSitting, ID: model.NewExamSittingID().String()},
	}
	ping := &Request{Sequence: 1, Action: "ping"}
	contract.requireFrame(t, "ping", ping)
	connection.handleRequest(context.Background(), ping)
	contract.requireFrame(t, "pong", nextInboundResponse(t, connection))

	subscribe := requestWithData(t, 2, "subscribe", subscription)
	contract.requireFrame(t, "subscribe", subscribe)
	connection.handleRequest(context.Background(), subscribe)
	contract.requireFrame(t, "subscription_reply", nextInboundResponse(t, connection))

	unsubscribe := requestWithData(t, 3, "unsubscribe", subscription)
	contract.requireFrame(t, "unsubscribe", unsubscribe)
	connection.handleRequest(context.Background(), unsubscribe)
	contract.requireFrame(t, "subscription_reply", nextInboundResponse(t, connection))

	connection.handleRequest(context.Background(), requestWithData(t, 4, "subscribe", Subscription{}))
	contract.requireFrame(t, "command_error", nextInboundResponse(t, connection))
	connection.handleRequest(context.Background(), &Request{Sequence: 5, Action: "teleport"})
	contract.requireFrame(t, "command_error", nextInboundResponse(t, connection))
	connection.enqueueError(6, "websocket.rate_limited", websocketErrorRateLimited)
	contract.requireFrame(t, "command_error", nextInboundResponse(t, connection))
	requireExamAttemptControlFrames(t, contract)
	application.authorizeErr = apperror.New("authorization.denied")
	connection.handleRequest(context.Background(), requestWithData(t, 7, "subscribe", subscription))
	contract.requireFrame(t, "command_error", nextInboundResponse(t, connection))
}

// TestApplicationEventFramesMatchAsyncAPI sends application events through
// the same path the Hub uses, for one subscription event and one event
// addressed to a User; app/realtime checks every event's data.
func TestApplicationEventFramesMatchAsyncAPI(t *testing.T) {
	t.Parallel()

	contract := loadAsyncAPIContract(t)
	connection := newInboundRuntime(&inboundTestApplication{}, newInboundTestSocket(), newRuntimeTestClock(time.Now()))

	archived, err := realtime.NewExamArchivedEvent(model.NewExamID(), 3, time.Now())
	require.NoError(t, err)
	archived.ID = model.NewID()
	connection.enqueueEvent(eventFromRealtime(archived))
	contract.requireFrame(t, "exam_archived", (<-connection.send).event)

	userContext, err := realtime.NewCurrentUserContextChangedEvent(connection.principal.UserID)
	require.NoError(t, err)
	userContext.ID = model.NewID()
	connection.enqueueEvent(eventFromRealtime(userContext))
	contract.requireFrame(t, "current_user.context.changed", (<-connection.send).event)
}

func TestCloseCodesMatchAsyncAPI(t *testing.T) {
	t.Parallel()

	contract := loadAsyncAPIContract(t)
	channel := contract.document["channels"].(map[string]any)["websocket"].(map[string]any)
	documented := map[string]int{}
	for _, raw := range channel["x-proctor-close-codes"].([]any) {
		entry := raw.(map[string]any)
		documented[entry["name"].(string)] = int(entry["code"].(float64))
	}
	require.Equal(t, map[string]int{
		"server":                CloseServer,
		"session_revoked":       CloseSessionRevoked,
		"backpressure":          CloseBackpressure,
		"authorization_changed": CloseAuthorizationChanged,
		"connection_limit":      CloseLimit,
		"rate_limited":          CloseRateLimited,
	}, documented)
}

// requireExamAttemptControlFrames checks the renewal and security update
// commands as the decoder accepts them, and their replies as the handlers
// encode them.
func requireExamAttemptControlFrames(t *testing.T, contract asyncAPIContract) {
	t.Helper()
	control := model.SecurityRenewal{
		ControlSequence: 3, PolicyDigest: model.SHA256Fingerprint([]byte("policy")),
		SecuritySessionID: "security-session-1", StreamID: "native-stream-1",
		DeliveryWatermarks: []model.DeliveryWatermark{
			{Family: "native", SourceID: "native-stream-1", AllocatedThroughSequence: 9, AcknowledgedThroughSequence: 7},
			{Family: "browser", SourceID: "00000000-0000-4000-8000-000000000001", AllocatedThroughSequence: 2},
		},
	}
	renew := requestWithData(t, 20, examAttemptRenewAction, examAttemptRenewRequest{
		SecurityControl: control, Generation: 1, Sequence: 4, ContinuityCredential: model.NewCredentialToken(),
	})
	_, err := decodeExamAttemptRenewRequest(renew.Data)
	require.NoError(t, err, "the documented renewal is one the server accepts")
	contract.requireFrame(t, "exam_attempt.renew", renew)

	update := requestWithData(t, 21, examAttemptSecurityUpdateAction, examAttemptSecurityUpdateRequest{
		Generation: 1, ContinuityCredential: model.NewCredentialToken(), SecurityControl: control,
	})
	_, err = decodeExamAttemptSecurityUpdateRequest(update.Data)
	require.NoError(t, err, "the documented security update is one the server accepts")
	contract.requireFrame(t, "exam_attempt.security.update", update)

	digest := model.SHA256Fingerprint([]byte("control"))
	result := securityControlFrom(model.SecurityRenewalResult{
		ProcessedControlSequence: 3, ProcessedControlDigest: &digest, SecurityInteractionAllowed: true,
		DeliveryWatermarkRejections: []model.DeliveryWatermarkRejection{{Family: "browser", SourceID: "00000000-0000-4000-8000-000000000001", Reason: "closed_source"}},
	})
	now := time.Now()
	renewed, err := json.Marshal(examAttemptRenewResponse{
		SecurityControl: result, Generation: 1, AcceptedSequence: 4,
		DatabaseTime: model.FormatRFC3339(now), LeaseExpiresAt: model.FormatRFC3339(now.Add(15 * time.Second)),
	})
	require.NoError(t, err)
	contract.requireFrame(t, "exam_attempt_renew_reply", &Response{Status: "ok", Sequence: 20, Data: renewed})
	processed, err := json.Marshal(result)
	require.NoError(t, err)
	contract.requireFrame(t, "security_control_reply", &Response{Status: "ok", Sequence: 21, Data: processed})
	empty, err := json.Marshal(securityControlFrom(model.SecurityRenewalResult{DeliveryWatermarkRejections: []model.DeliveryWatermarkRejection{}}))
	require.NoError(t, err)
	contract.requireFrame(t, "security_control_reply", &Response{Status: "ok", Sequence: 22, Data: empty})
}
