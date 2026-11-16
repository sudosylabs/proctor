// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sudosylabs/proctor/packages/mail"
	"github.com/sudosylabs/proctor/server/platform"
)

// WrapMailer returns next with each send and connection test timed and its
// SMTP stage, outcome, recipient count and payload size recorded. It adds
// measurement only; Close closes next.
func WrapMailer(next platform.Mailer, module *Module) platform.Mailer {
	return &measuredMailer{next: next, metrics: module}
}

type measuredMailer struct {
	next    platform.Mailer
	metrics *Module
}

func (m *measuredMailer) Enabled() bool      { return m.next.Enabled() }
func (m *measuredMailer) From() mail.Address { return m.next.From() }
func (m *measuredMailer) Send(ctx context.Context, message mail.Message) (mail.Receipt, error) {
	started := time.Now()
	receipt, err := m.next.Send(ctx, message)
	operation := "send"
	var operationError *mail.OpError
	if errors.As(err, &operationError) {
		operation = boundedSMTPOperation(operationError.Op)
	}
	m.metrics.ObserveSMTP(operation, err, time.Since(started))
	m.metrics.ObserveSMTPMessage(smtpOutcome(err), mailRecipientCount(message), mailPayloadBytes(message))
	return receipt, err
}
func (m *measuredMailer) Test(ctx context.Context) error {
	started := time.Now()
	err := m.next.Test(ctx)
	m.metrics.ObserveSMTP("test", err, time.Since(started))
	return err
}
func (m *measuredMailer) Close() error { return m.next.Close() }

func boundedSMTPOperation(operation string) string {
	switch operation {
	case "send",
		"test",
		"connect",
		"greeting",
		"hello",
		"starttls",
		"authenticate",
		"mail-from",
		"recipient",
		"data",
		"write",
		"commit",
		"quit":
		return operation
	default:
		return "unknown"
	}
}

func smtpOutcome(err error) string {
	if err == nil {
		return "accepted"
	}
	return string(mail.Classify(err))
}

func mailRecipientCount(message mail.Message) int {
	return len(message.To) + len(message.CC) + len(message.BCC)
}

func mailPayloadBytes(message mail.Message) int {
	total := len(message.Subject) + len(message.Text) + len(message.HTML)
	for _, attachment := range message.Attachments {
		total += len(attachment.Data)
	}
	return total
}

var _ platform.Mailer = (*measuredMailer)(nil)

// newMailCollectors creates the SMTP, durable mail and application event collectors.
func (m *Module) newMailCollectors() {
	m.smtpOperations = operationCounter("smtp", "SMTP transport operations.", []string{"operation", "outcome"})
	m.smtpDuration = operationDuration("smtp", "SMTP transport operation duration.", []string{"operation"})
	m.smtpMessages = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "smtp",
			Name:      "messages_total",
			Help:      "SMTP submission attempts by portable outcome.",
		},
		[]string{"outcome"},
	)
	m.smtpRecipients = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "smtp",
			Name:      "recipients_total",
			Help:      "Aggregate SMTP envelope recipients by portable outcome.",
		},
		[]string{"outcome"},
	)
	m.smtpBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "smtp",
			Name:      "message_bytes_total",
			Help:      "Aggregate pre-composition SMTP message payload bytes by portable outcome.",
		},
		[]string{"outcome"},
	)
	m.mailDeliveries = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "mail",
			Name:      "deliveries_total",
			Help:      "Durable mail delivery transitions by bounded template, state, and public outcome.",
		},
		[]string{"template", "state", "outcome"},
	)
	m.mailAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "mail",
			Name:      "attempts_total",
			Help:      "Aggregate durable mail delivery attempts.",
		},
		[]string{"template", "state"},
	)
	m.mailProcessingLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "mail",
			Name:      "processing_latency_seconds",
			Help:      "Durable mail processing latency.",
			Buckets:   prometheus.ExponentialBuckets(0.25, 2, 18),
		},
		[]string{"template", "state"},
	)
	m.mailQueueCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Namespace: "proctor", Subsystem: "mail", Name: "queue", Help: "Durable mail queue entries by template and state."},
		[]string{"template", "state"},
	)
	m.mailQueueOldest = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "proctor",
			Subsystem: "mail",
			Name:      "queue_oldest_age_seconds",
			Help:      "Age of the oldest durable mail queue entry.",
		},
		[]string{"template", "state"},
	)
	m.mailQueueTruncated = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "proctor",
		Subsystem: "mail",
		Name:      "queue_snapshot_truncated",
		Help:      "Whether the current durable mail queue snapshot reached its row bound.",
	})
	m.mailHealth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "proctor",
			Subsystem: "mail",
			Name:      "health",
			Help:      "Current bounded mail health state (one active code has value 1).",
		},
		[]string{"code"},
	)
	m.applicationEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{Namespace: "proctor", Subsystem: "application", Name: "events_total", Help: "Named bounded application outcomes."},
		[]string{"subsystem", "event", "outcome"},
	)
}
func (m *Module) ObserveSMTP(operation string, err error, duration time.Duration) {
	m.smtpOperations.WithLabelValues(operation, outcome(err)).Inc()
	m.smtpDuration.WithLabelValues(operation).Observe(duration.Seconds())
}
func (m *Module) ObserveSMTPMessage(result string, recipients, bytes int) {
	if m == nil {
		return
	}
	m.smtpMessages.WithLabelValues(result).Inc()
	if recipients > 0 {
		m.smtpRecipients.WithLabelValues(result).Add(float64(recipients))
	}
	if bytes > 0 {
		m.smtpBytes.WithLabelValues(result).Add(float64(bytes))
	}
}
func (m *Module) ObserveMailDelivery(template, state, result string, processingLatency time.Duration) {
	if m == nil {
		return
	}
	m.mailDeliveries.WithLabelValues(template, state, result).Inc()
	if processingLatency >= 0 {
		m.mailProcessingLatency.WithLabelValues(template, state).Observe(processingLatency.Seconds())
	}
}
func (m *Module) ObserveMailAttempt(template, state string, attemptDelta int) {
	if m != nil && attemptDelta > 0 {
		m.mailAttempts.WithLabelValues(template, state).Add(float64(attemptDelta))
	}
}
func (m *Module) SetMailQueue(template, state string, count int64, oldestAge time.Duration) {
	if m == nil {
		return
	}
	m.mailQueueCount.WithLabelValues(template, state).Set(float64(max(count, int64(0))))
	m.mailQueueOldest.WithLabelValues(template, state).Set(max(oldestAge.Seconds(), 0))
}
func (m *Module) SetMailQueueSnapshotTruncated(truncated bool) {
	if m == nil {
		return
	}
	if truncated {
		m.mailQueueTruncated.Set(1)
		return
	}
	m.mailQueueTruncated.Set(0)
}
func (m *Module) SetMailHealth(code string) {
	if m == nil {
		return
	}
	m.mailHealth.Reset()
	m.mailHealth.WithLabelValues(code).Set(1)
}
