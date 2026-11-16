// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package invocation holds the caller identity and request metadata that
// every use case takes after ctx. A transport builds it once and nothing
// changes it afterwards; it is never recovered from context.Context.
// Authorization is decided in app/access, not here.
package invocation

import "github.com/sudosylabs/proctor/server/model"

// Invocation is the immutable security and audit context of one use case.
type Invocation struct {
	principal model.Principal
	metadata  model.RequestMetadata
}

// New builds an invocation. Empty metadata is fine for callers with no
// request, such as jobs and tests.
func New(principal model.Principal, metadata model.RequestMetadata) Invocation {
	principal.CredentialScopes = append([]string(nil), principal.CredentialScopes...)
	return Invocation{principal: principal, metadata: metadata}
}

// Principal returns a defensive copy of the acting principal.
func (i Invocation) Principal() model.Principal {
	principal := i.principal
	principal.CredentialScopes = append([]string(nil), i.principal.CredentialScopes...)
	return principal
}

// RequestMetadata returns the audit-safe request metadata.
func (i Invocation) RequestMetadata() model.RequestMetadata { return i.metadata }

// System identifies a caller with no user behind it, without inventing a
// principal: either one job execution (JobID and AttemptID) or one named
// runtime task (Task) such as a periodic scan or an offline operator command.
// Its identities appear only in bounded system audit metadata.
type System struct {
	JobID     model.JobID
	AttemptID model.JobAttemptID
	Task      string
}

// Job names the work of one durable job execution.
func Job(jobID model.JobID, attemptID model.JobAttemptID) System {
	return System{JobID: jobID, AttemptID: attemptID}
}

// Task names work that no durable job carries.
func Task(name string) System { return System{Task: name} }

// Valid reports whether s names exactly one well-formed job execution or one
// task.
func (s System) Valid() bool {
	if s.Task != "" {
		return s.JobID.IsZero() && s.AttemptID.IsZero()
	}
	return s.JobID.IsValid() && s.AttemptID.IsValid()
}
