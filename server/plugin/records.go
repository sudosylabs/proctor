// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"encoding/json"
	"errors"
)

const (
	// MaxRecordDocumentBytes bounds a record's document in compact JSON.
	MaxRecordDocumentBytes = 64 << 10
	// MaxRecordListLimit bounds one page of Records.List.
	MaxRecordListLimit = 100
)

// The errors the record store and the leases answer with, besides
// ErrUnavailable once the plugin has failed or been turned off.
var (
	// ErrInvalidArgument reports a kind, key, prefix, document, revision or
	// limit outside the grammar and bounds Records and Leases document.
	ErrInvalidArgument = errors.New("plugin record or lease argument is invalid")
	// ErrRecordNotFound reports that Get found no record.
	ErrRecordNotFound = errors.New("plugin record does not exist")
	// ErrRevisionConflict reports that a Put or Delete found the record at
	// another revision, or missing or present against what was expected.
	ErrRevisionConflict = errors.New("plugin record is not at the expected revision")
	// ErrRecordTooLarge reports a document over MaxRecordDocumentBytes.
	ErrRecordTooLarge = errors.New("plugin record document is too large")
)

// Records is the plugin's own document store, which Host.Records returns.
// It is the one place a plugin keeps state between calls and across nodes;
// there is no plugin SQL and there are no plugin migrations.
//
// A record is named by a kind and a key and holds one JSON object of at
// most MaxRecordDocumentBytes. A kind is 1 to 64 lowercase letters, digits,
// underscores, dots or hyphens, starting with a letter ("grant"). A key is 1
// to 256 ASCII letters, digits and the characters - . _ ~ : @ = + /, with no
// slash at either end and no two in a row, so keys can form paths
// ("attempt/01hx7k2"). Keys sort bytewise.
//
// Every record has a revision: 1 when created, one more on every Put. Put
// and Delete take the revision the plugin last read and fail with
// ErrRevisionConflict when the record has moved on, so two nodes never
// overwrite each other unnoticed. An expected revision of 0 means the record
// must not exist yet.
//
// The store belongs to the plugin that was given it: the server adds the
// plugin's ID to every call, and no call can name another plugin. Writes are
// not audited, because records are the plugin's operational state, not
// security-sensitive changes; anything an auditor must see goes through a
// core capability that audits it. Records outlive the plugin being turned
// off: when it is turned on again it finds them as it left them, and
// nothing deletes them on its behalf.
type Records interface {
	// Get returns one record, or ErrRecordNotFound.
	Get(ctx context.Context, kind, key string) (Record, error)
	// Put writes document, a JSON object, as the record's whole content if
	// the record is at expectedRevision, and returns its new revision.
	Put(ctx context.Context, kind, key string, document json.RawMessage, expectedRevision int64) (int64, error)
	// Delete removes the record if it is at expectedRevision, which must be
	// positive.
	Delete(ctx context.Context, kind, key string, expectedRevision int64) error
	// List returns up to limit (1 to MaxRecordListLimit) records of kind
	// whose keys start with prefix and sort after after, in key order. Pass
	// the last key of one page as after to read the next; an empty page is
	// the end. prefix and after may be empty.
	List(ctx context.Context, kind, prefix, after string, limit int) ([]Record, error)
}

// Record is one record as the store holds it. Its Document is compact JSON
// whose members may come back in another order than they were written, and
// with only the last of any repeated member.
type Record struct {
	Key      string
	Document json.RawMessage
	Revision int64
}
