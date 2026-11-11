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
// app.App holds them as ExamAuthoring, ExamPublication, ExamRecords and
// ExamExports, so their exported methods are the public use cases.
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
// Child packages hold the rest: sitting (scheduling and lifecycle), attempt
// (the candidate side, from joining to Submission), review (Integrity Review
// and result release), resource (Exam Resources), workspace (the Starter
// Workspace), correction (changes to a running Sitting), manageraccess (which
// action an Exam Manager needs) and safemarkdown. Nothing in the server
// grades a Submission.
//
// Each package builds the realtime events of its own committed changes in
// its effects.go and publishes them through a realtime.Publisher. Start with
// authoring.go, publication.go, records.go or export.go for the service you
// are changing. The package never imports app or a transport.
package exam
