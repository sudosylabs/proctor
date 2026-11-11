// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package search holds the one use case that finds things across areas:
// Service.Search looks for Exams by Draft title, Classes by name and Users by
// directory fields in a single call. It owns no data and no visibility rule
// of its own. Exams and Users are found through the Exam catalog and User
// directory use cases, and Classes through the Class search authorization and
// the Class Store, so each type is authorized, audited and scoped exactly as
// its own list is.
//
// Start with service.go: Query and Result are the input and output, Service
// the use case, and Dependencies what composition supplies.
//
// The package depends on the services it searches through (app/exam and
// app/user), app/access, the shared application leaves and the Store
// contracts. It never imports the parent application or a transport.
package search
