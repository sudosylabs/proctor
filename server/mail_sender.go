// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"

	mailpkg "github.com/sudosylabs/proctor/packages/mail"
	appmail "github.com/sudosylabs/proctor/server/app/mail"
)

// accountMailerAdapter implements the application's mail sending port over
// the platform mailer: it converts the outbound message, attaches the inline
// images its HTML references, and classifies a transport failure into the
// application's retry vocabulary.
type accountMailerAdapter struct {
	mailer borrowedMailer
	assets *appmail.InlineAssets
}

func (a accountMailerAdapter) Enabled() bool {
	return a.mailer.Enabled()
}

func (a accountMailerAdapter) From() appmail.Address {
	from := a.mailer.From()
	return appmail.Address{Name: from.Name, Address: from.Address}
}

func (a accountMailerAdapter) Send(ctx context.Context, message appmail.Outbound) (appmail.TransportOutcome, error) {
	images, err := a.assets.ForHTML(message.HTML)
	if err != nil {
		return appmail.TransportPermanent, err
	}

	attachments := make([]mailpkg.Attachment, 0, len(images))
	for _, image := range images {
		attachments = append(attachments, mailpkg.Attachment{
			Filename:    image.Filename,
			ContentType: "image/png",
			ContentID:   image.ContentID,
			Inline:      true,
			Data:        image.Data,
		})
	}

	_, err = a.mailer.Send(ctx, mailpkg.Message{
		From:         mailpkg.Address{Name: message.From.Name, Address: message.From.Address},
		EnvelopeFrom: message.EnvelopeFrom,
		To:           []mailpkg.Address{{Name: message.To.Name, Address: message.To.Address}},
		Subject:      message.Subject,
		Text:         message.Text,
		HTML:         message.HTML,
		Headers:      message.Headers,
		MessageID:    message.MessageID,
		Date:         message.Date,
		Attachments:  attachments,
	})
	if err == nil {
		return appmail.TransportUnknown, nil
	}

	return classifyMailTransportError(err), err
}

type portableMailOutcome interface {
	MailOutcome() string
}

func classifyMailTransportError(err error) appmail.TransportOutcome {
	// A relay that cannot be secured or authenticated is an outage, not a
	// property of the message: retry, and let mail health report it. This
	// holds even when an older pinned mail module annotates it permanent.
	if errors.Is(err, mailpkg.ErrTLS) || errors.Is(err, mailpkg.ErrAuthentication) {
		return appmail.TransportTemporary
	}

	var portable portableMailOutcome
	if errors.As(err, &portable) {
		switch portable.MailOutcome() {
		case "temporary":
			return appmail.TransportTemporary
		case "permanent":
			return appmail.TransportPermanent
		case "acceptance_uncertain":
			return appmail.TransportAcceptanceUncertain
		}
	}

	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, mailpkg.ErrConnection):
		return appmail.TransportTemporary
	case errors.Is(err, mailpkg.ErrInvalidMessage),
		errors.Is(err, mailpkg.ErrInvalidAddress),
		errors.Is(err, mailpkg.ErrInvalidHeader),
		errors.Is(err, mailpkg.ErrMessageTooLarge),
		errors.Is(err, mailpkg.ErrRejected),
		errors.Is(err, mailpkg.ErrUnsupported):
		return appmail.TransportPermanent
	default:
		return appmail.TransportUnknown
	}
}

func (a accountMailerAdapter) Probe(ctx context.Context) error {
	return a.mailer.Test(ctx)
}
