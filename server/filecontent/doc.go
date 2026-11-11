// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package filecontent reads and writes the bytes of the files Proctor manages:
// profile pictures, Exam Resources, Starter Workspace and Attempt Workspace
// files, onboarding import uploads and Exam export archives. Content is its one
// service, and each kind of file has its own source file (exam_resource.go,
// profile_picture.go, ...).
//
// Bytes go to the file store (packages/vfs) under private keys derived from
// the record's ID; callers never see a key or a raw file-store call. The
// package also validates or converts what it stores, such as checking an Exam
// Resource or normalizing a profile picture. It does not decide who may read a
// file or whether a file exists: an application use case records that in
// PostgreSQL, and bytes written here are invisible until that record commits.
//
// Validation and image work are limited to a fixed number of operations at a
// time on each node. A call over the limit is refused at once, not queued
// (work.go). Start with content.go.
package filecontent
