// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package resource manages the Exam Resources of an Exam Draft: adding them,
// replacing their content, editing their details, reordering and removing
// them, and opening them for managers. Each change is authorized, audited and
// safe to retry.
//
// Freezing Resources into a Revision belongs to the parent exam package, and
// showing them to candidates during a Sitting belongs to attempt. HTTP, SQL
// and file storage live elsewhere. The package imports only model, store,
// app/apperror, app/invocation, app/idempotency and app/exam/manageraccess.
// Start with Service in service.go.
package resource
