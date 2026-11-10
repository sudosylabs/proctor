// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package exam holds the Exam use cases that are not about a single Sitting
// or Attempt: creating Exams and editing their Draft, the Exam catalog,
// archiving, Exam Managers, publishing Revisions, records completion, and
// exports. Its services are Authoring, Publication, Records and Exports;
// app.App embeds them, so their exported methods are the public use cases.
//
// Vocabulary used across exam and its child packages:
//   - Exam: the lasting authoring identity, owned by one Academic Unit.
//   - Draft: the Exam's one mutable authoring state.
//   - Exam Revision: an immutable published snapshot of a Draft.
//   - Sitting: one scheduled delivery of a Revision to one Class.
//   - Attempt: one candidate's durable work in a Sitting.
//   - Participation: one fenced period of live candidate access to an
//     Attempt; an ended Participation generation never works again.
//   - Resource: a read-only file shown to candidates during the Exam.
//   - Starter Workspace: the published initial files copied into each new
//     Attempt Workspace.
//   - Workspace: an Attempt's mutable candidate files.
//   - Submission: the immutable seal of an Attempt's accepted Workspace.
//   - Integrity: flags and evidence observed during an Attempt; a flag is
//     a reason to review, not a finding of guilt.
//   - Review: a manager's decisions and remarks on one Submission's
//     Integrity record; it holds no grade.
//
// Child packages own the rest: sitting, attempt, review, resource, workspace
// (the Starter Workspace), correction, manageraccess and safemarkdown. HTTP,
// SQL, file bytes, Job execution and grading live elsewhere. The package may
// import only model, store, the app/apperror, app/invocation and
// app/idempotency packages, and app/exam/manageraccess; it never imports app
// itself, a transport, or a concrete adapter.
package exam
