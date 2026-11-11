// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
)

type mailRendererFake struct{ content FrozenContent }

func (r mailRendererFake) Render(RenderRequest) (FrozenContent, error) {
	return r.content, nil
}

type mailSenderFake struct {
	enabled   bool
	from      Address
	messages  []Outbound
	err       error
	outcome   TransportOutcome
	afterSend func()
}

func (s *mailSenderFake) Enabled() bool { return s.enabled }

func (s *mailSenderFake) From() Address { return s.from }

func (s *mailSenderFake) Send(_ context.Context, message Outbound) (TransportOutcome, error) {
	s.messages = append(s.messages, message)
	if s.afterSend != nil {
		s.afterSend()
	}
	if s.err != nil {
		return s.outcome, s.err
	}
	return TransportUnknown, nil
}

func mailTestSealer(t *testing.T) *secretseal.Sealer {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytesRepeat(7, 32))
	value, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	return value
}

func bytesRepeat(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func mailTestPrincipal(at time.Time) model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewSessionCredentialID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: at}
}

func mailTestUser(principal model.Principal, at time.Time) *model.User {
	user := &model.User{Email: "operator@example.test", DisplayName: "Operator", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(principal.UserID, at.Add(-time.Hour))
	return user
}

// newDirectMailPreparer builds the production Composer for a test.
func newDirectMailPreparer(renderer Renderer, sender Sender, sealer *secretseal.Sealer, institution func() string) (*Composer, error) {
	return NewComposer(ComposerDependencies{Renderer: renderer, Sender: sender, Sealer: sealer, Institution: institution})
}

// nopDeliveryRecorder records nothing, for tests that need no mail
// metrics.
type nopDeliveryRecorder struct{}

func (nopDeliveryRecorder) RecordMailDelivery(context.Context, DeliveryMetric) {}

func (nopDeliveryRecorder) RecordMailAttempt(context.Context, AttemptMetric) {}

func (nopDeliveryRecorder) RecordMailQueueSnapshot(context.Context, []QueueMetric) {
}

func (nopDeliveryRecorder) RecordMailHealth(context.Context, HealthMetric) {}

func (nopDeliveryRecorder) Snapshot() MetricsSnapshot { return MetricsSnapshot{} }

type recordingMailMetrics struct {
	deliveries []DeliveryMetric
	queues     []QueueMetric
	health     []HealthMetric
}

func (r *recordingMailMetrics) RecordMailDelivery(_ context.Context, metric DeliveryMetric) {
	r.deliveries = append(r.deliveries, metric)
}

func (*recordingMailMetrics) RecordMailAttempt(context.Context, AttemptMetric) {}

func (r *recordingMailMetrics) RecordMailQueueSnapshot(_ context.Context, metrics []QueueMetric) {
	r.queues = append(r.queues[:0], metrics...)
}

func (r *recordingMailMetrics) RecordMailHealth(_ context.Context, metric HealthMetric) {
	r.health = append(r.health, metric)
}

func (r *recordingMailMetrics) Snapshot() MetricsSnapshot { return MetricsSnapshot{} }
