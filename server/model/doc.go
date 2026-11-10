// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package model defines Proctor's domain types and the checks each value can
// make on its own, such as required fields and allowed values. Checks that
// need stored state, such as uniqueness or cycles in the Academic Unit tree,
// belong to app and the store. Both persistence and the transports import
// model, so a type they must agree on lives here.
//
// The academic hierarchy separates who owns a course of study from the
// students who take it in a given period:
//
//	Institution
//	└── AcademicUnit (a tree of faculties, schools, and departments)
//	    └── Programme
//	        └── ProgrammeLevel
//	            └── Class ── AcademicPeriod
//
// For example, a School of Computing owns the Bachelor of Computer Science
// Programme, whose Year 1 ProgrammeLevel has "Class A" in the 2026-2027
// AcademicPeriod. A student's placement comes from their active ClassMember.
// Staff belong to an AcademicUnit through AcademicUnitMember, but membership
// grants nothing; access comes from scoped RoleBinding values.
//
// Conventions:
//   - IDs: each entity has its own ID type (UserID, ClassID, ...). All share
//     one opaque 26-character z-base-32 form but are not interchangeable.
//   - Times: durable instants are UTC time.Time values, with OptionalTime
//     when absence matters. Edges that use integer milliseconds convert
//     explicitly.
//   - JSON: a type carries json tags only when model owns its one canonical
//     form: digest- or signature-bound documents, the desktop and browser
//     delivery agreements, stored replay results a transport returns as-is,
//     and durable Job payloads. Request and response types live in httpapi or
//     websocket, and stored-only spellings in sqlstore. A json:"-" tag on a
//     secret field is always allowed.
//   - Errors: a failure is either a package sentinel matched with errors.Is,
//     or a *ValidationError from invalidModelError naming the model, the
//     field (empty for the whole value) and the reason, optionally wrapping a
//     cause. No ad hoc error strings.
//   - Validity: closed vocabularies and identifiers answer IsValid() bool;
//     structured values answer Validate() error.
package model
