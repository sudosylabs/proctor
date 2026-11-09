// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the Apache License, Version 2.0.
// See LICENSES/Apache-2.0.txt and NOTICE in the server module root for
// license and attribution information.
// SPDX-License-Identifier: Apache-2.0
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/public/model/audit_record.go. Proctor makes
// the rich attempt/result record durable and adds explicit resource, academic
// scope, request, node, and authentication fields.

package model

import (
	"encoding/json"
	"net/netip"
	"strings"
	"time"
)

const AuditJSONMaxBytes = 16 * 1024

type AuditStatus string

const (
	AuditStatusAttempt AuditStatus = "attempt"
	AuditStatusSuccess AuditStatus = "success"
	AuditStatusFail    AuditStatus = "fail"
)

// AuditErrorAttemptAbandoned marks an attempt whose outcome was never
// recorded and that maintenance terminalized after its bound. It means the
// outcome is unknown, not that the operation was refused.
const AuditErrorAttemptAbandoned = "audit.attempt_abandoned"

// AuditEvent is an append-oriented security record. Critical mutations are
// inserted as attempts and may transition exactly once; point-in-time
// authorization decisions are inserted directly with their terminal result.
//
// ActorID and SessionID are optional typed identifiers (zero means absent).
// SubjectUserID names the User whose account the event concerns, when there
// is one: a User resource is always its own subject, and writers whose
// resource is a scope, Class, or the Institution name the affected User
// explicitly. ScopeID remains a plain string because the target depends on
// ScopeType.
// Create/update times use UTC time.Time; public list DTOs still project millis.
type AuditEvent struct {
	ID        AuditEventID
	CreatedAt time.Time
	UpdatedAt time.Time
	ActorID   UserID
	SessionID SessionID
	// SubjectUserID is the User the event is about; see AuditEvent.
	SubjectUserID UserID
	Action        string
	Resource      Resource
	ScopeType     RoleScopeType
	ScopeID       string
	Status        AuditStatus
	RequestID     string
	NodeID        string
	ClientType    string
	AuthMethod    string
	IPAddress     string
	UserAgent     string
	ErrorCode     string
	Parameters    json.RawMessage
	PriorState    json.RawMessage
	Result        json.RawMessage
}

// AccountDisableReason returns the optional reason recorded on an account
// disable event, or empty for any other event or when none was given. It is
// the one audit parameter the institution audit list presents, and only to
// readers that receive parameters at all.
func (ae *AuditEvent) AccountDisableReason() string {
	if ae == nil || ae.Action != string(ActionUserManage) || len(ae.Parameters) == 0 {
		return ""
	}
	var parameters struct {
		Operation string `json:"operation"`
		Value     struct {
			Reason string `json:"reason"`
		} `json:"value"`
	}
	if json.Unmarshal(ae.Parameters, &parameters) != nil || parameters.Operation != AuditOperationDisableAccount {
		return ""
	}
	return parameters.Value.Reason
}

// AuditQuery is the application/store listing filter for audit events.
type AuditQuery struct {
	ActorID  string
	Action   string
	Resource *Resource
	// BeforeCreatedAt and BeforeID continue a page at microsecond precision.
	BeforeCreatedAt time.Time
	BeforeID        string
	Limit           int
}

// PrepareCreate applies application-owned identity and lifecycle fields and
// bounds string fields that arrive from transport or operational metadata.
func (ae *AuditEvent) PrepareCreate(id AuditEventID, at time.Time) {
	if ae == nil {
		return
	}
	ae.ID = id
	at = TimeUTC(at)
	ae.CreatedAt = at
	ae.UpdatedAt = at
	ae.normalize()
}

// PrepareUpdate applies the application-selected completion time.
func (ae *AuditEvent) PrepareUpdate(at time.Time) {
	if ae == nil {
		return
	}
	ae.UpdatedAt = TimeUTC(at)
	ae.ErrorCode = boundedString(strings.TrimSpace(ae.ErrorCode), 128)
	ae.Result = cloneRawMessage(ae.Result)
}

func (ae *AuditEvent) normalize() {
	if ae == nil {
		return
	}
	ae.Action = strings.TrimSpace(ae.Action)
	if ae.SubjectUserID.IsZero() && ae.Resource.Type == ResourceUser {
		ae.SubjectUserID = UserID(ae.Resource.ID)
	}
	ae.RequestID = boundedString(strings.TrimSpace(ae.RequestID), 128)
	ae.NodeID = boundedString(strings.TrimSpace(ae.NodeID), 128)
	ae.ClientType = boundedString(strings.TrimSpace(ae.ClientType), 32)
	ae.AuthMethod = boundedString(strings.TrimSpace(ae.AuthMethod), 64)
	ae.IPAddress = boundedString(strings.TrimSpace(ae.IPAddress), 64)
	ae.UserAgent = boundedString(strings.TrimSpace(ae.UserAgent), 512)
	ae.ErrorCode = boundedString(strings.TrimSpace(ae.ErrorCode), 128)
	ae.Parameters = cloneRawMessage(ae.Parameters)
	ae.PriorState = cloneRawMessage(ae.PriorState)
	ae.Result = cloneRawMessage(ae.Result)
}

// Validate checks rehydrated audit-event state.
func (ae *AuditEvent) Validate() error {
	const where = "AuditEvent.Validate"
	if ae == nil {
		return invalidModelError(where, "audit_event", "value", "is required", "")
	}
	if !ae.ID.IsValid() {
		return invalidModelError(where, "audit_event", "id", "must be a valid identifier", "")
	}
	details := "id=" + ae.ID.String()
	if ae.CreatedAt.IsZero() || ae.UpdatedAt.IsZero() {
		return invalidModelError(where, "audit_event", "created_at", "must be set", details)
	}
	if ae.UpdatedAt.Before(ae.CreatedAt) {
		return invalidModelError(where, "audit_event", "updated_at", "must not precede created_at", details)
	}
	if !ae.ActorID.IsZero() && !ae.ActorID.IsValid() {
		return invalidModelError(where, "audit_event", "actor_id", "must be empty or a valid identifier", details)
	}
	if !ae.SessionID.IsZero() && !ae.SessionID.IsValid() {
		return invalidModelError(where, "audit_event", "session_id", "must be empty or a valid identifier", details)
	}
	if !ae.SubjectUserID.IsZero() && !ae.SubjectUserID.IsValid() {
		return invalidModelError(where, "audit_event", "subject_user_id", "must be empty or a valid identifier", details)
	}
	if ae.Resource.Type == ResourceUser && ae.SubjectUserID.String() != ae.Resource.ID {
		return invalidModelError(where, "audit_event", "subject_user_id", "must name the User resource", details)
	}
	if len(ae.Action) == 0 || len(ae.Action) > RolePermissionMaxLength || !validPermission.MatchString(ae.Action) {
		return invalidModelError(where, "audit_event", "action", "must be a valid action", details)
	}
	if err := ae.Resource.Validate(); err != nil {
		return invalidModelError(where, "audit_event", "resource", "must identify a valid resource", details)
	}
	if !ae.ScopeType.IsValid() || !IsValidOpaqueID(ae.ScopeID) {
		return invalidModelError(where, "audit_event", "scope", "must identify a valid authorization scope", details)
	}
	if ae.Status != AuditStatusAttempt && ae.Status != AuditStatusSuccess && ae.Status != AuditStatusFail {
		return invalidModelError(where, "audit_event", "status", "has an unknown value", details)
	}
	if ae.NodeID == "" {
		return invalidModelError(where, "audit_event", "node_id", "must not be empty", details)
	}
	if ae.IPAddress != "" {
		if _, err := netip.ParseAddr(ae.IPAddress); err != nil {
			return invalidModelError(where, "audit_event", "ip_address", "must be a valid IP address", details)
		}
	}
	for field, value := range map[string]json.RawMessage{
		"parameters":  ae.Parameters,
		"prior_state": ae.PriorState,
		"result":      ae.Result,
	} {
		if len(value) > AuditJSONMaxBytes || (len(value) > 0 && !json.Valid(value)) {
			return invalidModelError(where, "audit_event", field, "must be bounded valid JSON", details)
		}
	}
	return nil
}

// Clone returns a deep copy of the audit event including JSON payloads.
func (ae *AuditEvent) Clone() *AuditEvent {
	if ae == nil {
		return nil
	}
	cloned := *ae
	cloned.Parameters = cloneRawMessage(ae.Parameters)
	cloned.PriorState = cloneRawMessage(ae.PriorState)
	cloned.Result = cloneRawMessage(ae.Result)
	return &cloned
}

// EncodeAuditData marshals a value into a bounded audit JSON payload.
func EncodeAuditData(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > AuditJSONMaxBytes {
		return nil, invalidModelError(
			"EncodeAuditData", "audit_event", "data", "must be JSON encodable and bounded", "",
		)
	}
	return encoded, nil
}

func boundedString(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}
