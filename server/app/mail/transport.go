// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// Address is a display name and bare address. ValidateAddress rejects one that
// could inject a header.
type Address struct {
	Name    string
	Address string
}

// Outbound is one message handed to a Sender, opened from a delivery's sealed
// payload at send time.
type Outbound struct {
	From         Address
	EnvelopeFrom string
	To           Address
	Subject      string
	Text         string
	HTML         string
	Headers      map[string][]string
	MessageID    string
	Date         time.Time
}

// TransportOutcome classifies a failed Send. Only TransportPermanent fails the
// delivery at once; every other outcome retries until attempts or the
// deadline run out.
type TransportOutcome string

// The TransportOutcome values. A Sender returns TransportUnknown with a nil
// error, when there is nothing to classify.
const (
	TransportUnknown             TransportOutcome = "unknown"
	TransportTemporary           TransportOutcome = "temporary"
	TransportPermanent           TransportOutcome = "permanent"
	TransportAcceptanceUncertain TransportOutcome = "acceptance_uncertain"
)

// Sender is the outbound transport. When Enabled reports false, composers
// still record each delivery but suppress it instead of queuing it.
type Sender interface {
	Enabled() bool
	From() Address
	Send(context.Context, Outbound) (TransportOutcome, error)
}

// ValidateAddress rejects an empty address, a NUL or line break anywhere in
// the name or address, and an address that does not parse back to itself.
func ValidateAddress(address Address) error {
	if address.Address == "" || strings.ContainsAny(address.Name+address.Address, "\x00\r\n") {
		return errors.New("mail address is invalid")
	}
	parsed, err := mail.ParseAddress(address.Address)
	if err != nil || parsed.Address != address.Address {
		return errors.New("mail address is invalid")
	}
	return nil
}

// StableMessageID derives the Message-ID from the delivery ID, so every retry
// of one delivery reuses it. The domain comes from from, or localhost when
// from does not parse.
func StableMessageID(id model.MailDeliveryID, from string) string {
	domain := "localhost"
	if parsed, err := mail.ParseAddress(from); err == nil {
		if at := strings.LastIndexByte(parsed.Address, '@'); at >= 0 {
			domain = parsed.Address[at+1:]
		}
	}
	return "<mail." + id.String() + "@" + strings.ToLower(domain) + ">"
}

// MaskAddress keeps the first character of the local part and the whole
// domain, as in "j***@example.edu". An address without a local part becomes
// "***".
func MaskAddress(address string) string {
	at := strings.LastIndexByte(address, '@')
	if at < 1 {
		return "***"
	}
	local := []rune(address[:at])
	prefix := "***"
	if len(local) > 1 {
		prefix = string(local[0]) + strings.Repeat("*", min(3, len(local)-1))
	}
	return prefix + address[at:]
}
