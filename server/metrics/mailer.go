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
