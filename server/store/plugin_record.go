// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	// PluginRecordKindMaxBytes bounds a record kind.
	PluginRecordKindMaxBytes = 64
	// PluginRecordKeyMaxBytes bounds a record key, a key prefix and a lease
	// key.
	PluginRecordKeyMaxBytes = 256
	// PluginRecordDocumentMaxBytes bounds a record document in its compact
	// encoding.
	PluginRecordDocumentMaxBytes = 64 << 10
	// PluginRecordListMaxLimit bounds one page of List.
	PluginRecordListMaxLimit = 100
)

// PluginRecord is one document a plugin keeps for itself, named by its kind
// and key within the plugin. It is operational state the plugin owns, such
// as which keys it wants leased or what it last told an outside system, not
// a security-sensitive record: writes are not audited.
type PluginRecord struct {
	PluginID model.PluginID
	Kind     string
	Key      string
	// Revision is 1 when the record is created and grows by one on every
	// Put.
	Revision int64
	// Document is a JSON object in compact encoding, with the members
	// PostgreSQL keeps: a repeated member keeps only its last value, and
	// members may come back in another order.
	Document  json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks a rehydrated record.
func (r *PluginRecord) Validate() error {
	if r == nil || !r.PluginID.IsValid() || !ValidPluginRecordKind(r.Kind) || !ValidPluginRecordKey(r.Key) ||
		r.Revision < 1 || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || !pluginRecordObject(r.Document) {
		return ErrInvalidState
	}
	return nil
}

// PluginRecordPut writes one record if it is still at ExpectedRevision: 0
// creates it and requires that it does not exist, any other value replaces
// it and requires that it is at that revision. Document must be a JSON
// object of at most PluginRecordDocumentMaxBytes once compacted.
type PluginRecordPut struct {
	PluginID         model.PluginID
	Kind             string
	Key              string
	Document         json.RawMessage
	ExpectedRevision int64
}

// PluginRecordDeletion removes one record if it is still at
// ExpectedRevision, which must be positive.
type PluginRecordDeletion struct {
	PluginID         model.PluginID
	Kind             string
	Key              string
	ExpectedRevision int64
}

// PluginRecordListOptions selects one page of a plugin's records of one
// kind, in key order: those whose key starts with Prefix and sorts after
// After, at most Limit of them (1 to PluginRecordListMaxLimit). Prefix and
// After may be empty; After is the last key of the previous page.
type PluginRecordListOptions struct {
	PluginID model.PluginID
	Kind     string
	Prefix   string
	After    string
	Limit    int
}

// PluginRecordStore keeps the plugin record store, one table shared by every
// plugin and keyed by plugin ID, kind and key. A record belongs to exactly
// one plugin: every operation names the plugin, and nothing reads or writes
// across plugins. Records of a plugin that is turned off are kept, and the
// plugin sees them again when it is turned on; nothing removes them on its
// behalf.
//
// Each operation is one statement on one row or one bounded page, so
// operations on different keys never wait for one another, and the
// revision decides every race on one key: of two Puts at the same expected
// revision, one wins and the other gets ErrPluginRecordRevisionConflict.
type PluginRecordStore interface {
	// Get returns one record, or ErrNotFound.
	Get(ctx context.Context, pluginID model.PluginID, kind, key string) (*PluginRecord, error)
	// Put creates or replaces a record and returns it as stored. A record
	// not at the expected revision, including one that exists when 0 was
	// expected or is missing when a revision was, is
	// ErrPluginRecordRevisionConflict; an oversized document is
	// ErrPluginRecordTooLarge.
	Put(ctx context.Context, put *PluginRecordPut) (*PluginRecord, error)
	// Delete removes a record; one that is not at the expected revision,
	// or is missing, is ErrPluginRecordRevisionConflict.
	Delete(ctx context.Context, deletion *PluginRecordDeletion) error
	// List returns one page of records in key order; no records is an
	// empty page.
	List(ctx context.Context, options PluginRecordListOptions) ([]*PluginRecord, error)
}

// ErrPluginRecordTooLarge is returned by Put for a document over
// PluginRecordDocumentMaxBytes.
var ErrPluginRecordTooLarge = errors.New("plugin record document is too large")

// ErrPluginRecordRevisionConflict reports the record's current revision, 0
// when it does not exist.
type ErrPluginRecordRevisionConflict struct {
	CurrentRevision int64
}

func (e *ErrPluginRecordRevisionConflict) Error() string {
	return "plugin record revision conflict"
}

// ValidPluginRecordKind reports whether kind is 1 to PluginRecordKindMaxBytes
// lowercase letters, digits, underscores, dots or hyphens, starting with a
// letter, for example "grant" or "sync.cursor".
func ValidPluginRecordKind(kind string) bool {
	if kind == "" || len(kind) > PluginRecordKindMaxBytes || kind[0] < 'a' || kind[0] > 'z' {
		return false
	}
	for i := 0; i < len(kind); i++ {
		c := kind[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// ValidPluginRecordKey reports whether key is 1 to PluginRecordKeyMaxBytes
// ASCII letters, digits and the punctuation - . _ ~ : @ = + /, that neither
// starts nor ends with a slash and has no two slashes in a row, for example
// "attempt/01hx7k2/terminal". Lease keys follow the same grammar.
func ValidPluginRecordKey(key string) bool {
	return key != "" && ValidPluginRecordKeyPrefix(key) && key[len(key)-1] != '/'
}

// ValidPluginRecordKeyPrefix reports whether prefix may begin a valid key:
// empty, or a key that may also end in a slash, such as "attempt/".
func ValidPluginRecordKeyPrefix(prefix string) bool {
	if len(prefix) > PluginRecordKeyMaxBytes || (prefix != "" && prefix[0] == '/') {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '/':
			if i > 0 && prefix[i-1] == '/' {
				return false
			}
		case c == '-', c == '.', c == '_', c == '~', c == ':', c == '@', c == '=', c == '+':
		default:
			return false
		}
	}
	return true
}

// CompactPluginRecordDocument returns document in compact encoding. A
// document that is not one JSON object is ErrInvalidInput; one over
// PluginRecordDocumentMaxBytes once compacted is ErrPluginRecordTooLarge.
func CompactPluginRecordDocument(document json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(document)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, NewErrInvalidInput("plugin_record", "document", nil)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, NewErrInvalidInput("plugin_record", "document", nil)
	}
	if compact.Len() > PluginRecordDocumentMaxBytes {
		return nil, ErrPluginRecordTooLarge
	}
	return compact.Bytes(), nil
}

// pluginRecordObject reports whether a rehydrated document is a JSON object.
// Its size is not checked again: PostgreSQL may print numbers longer than
// they were written.
func pluginRecordObject(document json.RawMessage) bool {
	trimmed := bytes.TrimSpace(document)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}
