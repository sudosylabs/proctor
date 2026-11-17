// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The assistant reads the Attempt Workspace, the authority, through the
// candidate's own read use cases: it never sees more than the candidate.
const (
	manifestMaximumItems = 2000
	fileReadMaximumBytes = 256 << 10
	knownMaximumFiles    = 128
	knownMaximumBytes    = 4 << 20
)

var (
	errNotText  = errors.New("the file is not text")
	errNotFound = errors.New("no such file")
)

// manifest lists the workspace at one cursor, up to its bound.
func (r *turnRun) manifest() ([]store.CandidateAttemptWorkspaceItem, bool, error) {
	var items []store.CandidateAttemptWorkspaceItem
	cursor := int64(-1)
	var after model.AttemptWorkspaceEntryID
	refreshes := 0
	for {
		page, err := r.service.attempts.ListCandidateExamWorkspace(r.ctx, r.inv, attempt.WorkspaceQuery{Access: r.access,
			ExpectedCursor: cursor, AfterEntryID: after, Limit: 200})
		if err != nil {
			return nil, false, err
		}
		if page.RefreshRequired {
			// The workspace moved while it was listed: list it again from the top.
			refreshes++
			if refreshes > 3 {
				return nil, false, errors.New("workspace changed repeatedly while listing")
			}
			items, cursor, after = nil, -1, ""
			continue
		}
		items = append(items, page.Items...)
		if !page.HasMore || len(page.Items) == 0 {
			return items, false, nil
		}
		if len(items) >= manifestMaximumItems {
			return items[:manifestMaximumItems], true, nil
		}
		cursor, after = page.Cursor, page.Items[len(page.Items)-1].EntryID
	}
}

func findFile(items []store.CandidateAttemptWorkspaceItem, path string) (store.CandidateAttemptWorkspaceItem, bool) {
	for _, item := range items {
		if item.Path == path {
			return item, true
		}
	}
	return store.CandidateAttemptWorkspaceItem{}, false
}

// readText reads up to limit bytes of a text file.
func (r *turnRun) readText(item store.CandidateAttemptWorkspaceItem, limit int) (string, bool, error) {
	if item.Kind != model.StarterWorkspaceEntryFile {
		return "", false, errNotFound
	}
	opened, err := r.service.attempts.OpenCandidateExamWorkspaceFile(r.ctx, r.inv, attempt.OpenWorkspaceFileQuery{Access: r.access,
		EntryID: item.EntryID})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = opened.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(opened.Body, int64(limit)+1))
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
		// A cut may split the last character; drop its first bytes.
		for back := 0; back < 3 && len(data) > 0 && !utf8.Valid(data); back++ {
			data = data[:len(data)-1]
		}
	}
	text := string(data)
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", false, errNotText
	}
	return text, truncated, nil
}

// knownLines are the workspace's lines, starter copies included. A reply's
// code that the candidate already has is not the solution.
func (r *turnRun) knownLines() map[[32]byte]struct{} {
	known := map[[32]byte]struct{}{}
	items, _, err := r.manifest()
	if err != nil {
		return known
	}
	files, total := 0, 0
	for _, item := range items {
		if item.Kind != model.StarterWorkspaceEntryFile || files >= knownMaximumFiles || total >= knownMaximumBytes {
			continue
		}
		text, _, readErr := r.readText(item, fileReadMaximumBytes)
		if readErr != nil {
			continue
		}
		files++
		total += len(text)
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(line) != "" {
				known[normalizedLineHash(line)] = struct{}{}
			}
		}
	}
	starter, starterErr := r.service.attempts.CandidateStarterWorkspaceText(r.ctx, r.inv, r.access)
	if starterErr == nil {
		for _, text := range starter {
			for _, line := range strings.Split(text, "\n") {
				if strings.TrimSpace(line) != "" {
					known[normalizedLineHash(line)] = struct{}{}
				}
			}
		}
	}

	return known
}
