// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"

	"github.com/sudosylabs/proctor/server/model"
)

// auditExportRecord is one event in an export file. Its fields are the
// listing's own, under the listing's names: never parameters, prior state or
// result, and nothing the caller's projection has already cleared. The
// summary and names are left out because they are written for one reader,
// in one language, at one moment.
type auditExportRecord struct {
	ID           string `json:"id"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	ActorID      string `json:"actor_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	ScopeType    string `json:"scope_type,omitempty"`
	ScopeID      string `json:"scope_id,omitempty"`
	Status       string `json:"status"`
	RequestID    string `json:"request_id,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	ClientType   string `json:"client_type,omitempty"`
	AuthMethod   string `json:"authentication_method,omitempty"`
	IPAddress    string `json:"ip_address,omitempty"`
	UserAgent    string `json:"user_agent,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

var auditExportColumns = []string{
	"id", "created_at", "updated_at", "actor_id", "session_id", "action", "resource_type", "resource_id",
	"scope_type", "scope_id", "status", "request_id", "node_id", "client_type", "authentication_method",
	"ip_address", "user_agent", "error_code", "reason",
}

func newAuditExportRecord(event *model.AuditEvent) auditExportRecord {
	return auditExportRecord{
		ID:           event.ID.String(),
		CreatedAt:    model.FormatRFC3339(event.CreatedAt),
		UpdatedAt:    model.FormatRFC3339(event.UpdatedAt),
		ActorID:      event.ActorID.String(),
		SessionID:    event.SessionID.String(),
		Action:       event.Action,
		ResourceType: string(event.Resource.Type),
		ResourceID:   event.Resource.ID,
		ScopeType:    string(event.ScopeType),
		ScopeID:      event.ScopeID,
		Status:       string(event.Status),
		RequestID:    event.RequestID,
		NodeID:       event.NodeID,
		ClientType:   event.ClientType,
		AuthMethod:   event.AuthMethod,
		IPAddress:    event.IPAddress,
		UserAgent:    event.UserAgent,
		ErrorCode:    event.ErrorCode,
		Reason:       event.AccountDisableReason(),
	}
}

func (r auditExportRecord) cells() []string {
	cells := []string{
		r.ID, r.CreatedAt, r.UpdatedAt, r.ActorID, r.SessionID, r.Action, r.ResourceType, r.ResourceID,
		r.ScopeType, r.ScopeID, r.Status, r.RequestID, r.NodeID, r.ClientType, r.AuthMethod,
		r.IPAddress, r.UserAgent, r.ErrorCode, r.Reason,
	}
	for i := range cells {
		cells[i] = csvSafe(cells[i])
	}
	return cells
}

type auditExportEncoder interface {
	encode(*model.AuditEvent) error
	close() error
}

func newAuditExportEncoder(format model.AuditExportFormat, w io.Writer) (auditExportEncoder, error) {
	switch format {
	case model.AuditExportCSV:
		out := csv.NewWriter(w)
		if err := out.Write(auditExportColumns); err != nil {
			return nil, err
		}
		return csvAuditExport{out}, nil
	case model.AuditExportJSONL:
		return jsonlAuditExport{json.NewEncoder(w)}, nil
	}
	return nil, errors.New("unknown audit export format")
}

type csvAuditExport struct{ out *csv.Writer }

func (e csvAuditExport) encode(event *model.AuditEvent) error {
	return e.out.Write(newAuditExportRecord(event).cells())
}

func (e csvAuditExport) close() error {
	e.out.Flush()
	return e.out.Error()
}

// jsonlAuditExport writes one JSON object per line; encoding/json ends each
// with a newline.
type jsonlAuditExport struct{ out *json.Encoder }

func (e jsonlAuditExport) encode(event *model.AuditEvent) error {
	return e.out.Encode(newAuditExportRecord(event))
}

func (jsonlAuditExport) close() error { return nil }
