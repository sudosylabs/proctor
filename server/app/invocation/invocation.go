// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package invocation is the first argument after ctx of every use case: who is
// calling and from where. Invocation carries the authenticated Principal and
// the request metadata recorded in audit; a transport builds it and nothing
// changes it afterwards. System names work that has no user behind it, either
// one Job execution (Job) or a named runtime task such as a periodic scan
// (Task).
//
// It is a separate package so that app and every area package can share the
// type. Authorization is decided in app/access, not here.
package invocation

import "github.com/sudosylabs/proctor/server/model"

// Invocation is the immutable security and audit context for one
// actor-sensitive application use case. Transports, jobs, and tests construct
// it explicitly; it is never recovered from context.Context.
type Invocation struct {
	principal model.Principal
	metadata  model.RequestMetadata
}

// New builds an invocation from the acting principal and safe call metadata.
// Empty metadata fields are allowed when a caller has no request context (for
// example internal jobs and focused tests).
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

// RequestMetadata returns the safe audit metadata carried by the invocation.
func (i Invocation) RequestMetadata() model.RequestMetadata { return i.metadata }

// System identifies the actorless caller of a use case without manufacturing
// a user Principal: either one durable Job execution (JobID and AttemptID) or
// one named non-durable runtime task (Task), such as a periodic maintenance
// scan, startup reconciliation, or an offline host-operator command. Its
// identities enter only bounded system audit metadata.
type System struct {
	JobID     model.JobID
	AttemptID model.JobAttemptID
	Task      string
}

// Job names the actorless work of one durable Job execution.
func Job(jobID model.JobID, attemptID model.JobAttemptID) System {
	return System{JobID: jobID, AttemptID: attemptID}
}

// Task names actorless work that no durable Job carries.
func Task(name string) System { return System{Task: name} }

// Valid reports whether the System names exactly one well-formed Job
// execution or one runtime task.
func (s System) Valid() bool {
	if s.Task != "" {
		return s.JobID.IsZero() && s.AttemptID.IsZero()
	}
	return s.JobID.IsValid() && s.AttemptID.IsValid()
}
