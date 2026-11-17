// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/invocation"
)

// CandidateStarterWorkspaceText returns bounded text from the admission
// revision for the assistant's local output check. It exposes no new tool.
func (s *Service) CandidateStarterWorkspaceText(ctx context.Context, inv invocation.Invocation, access CandidateAccess) (_ []string, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := candidateSelector(inv, access)
	if err != nil {
		return nil, err
	}
	files, err := s.attempts.ListCandidateStarterWorkspace(ctx, selector, 128)
	if err != nil {
		return nil, storeError(storeErrors, err)
	}
	result := []string{}
	remaining := int64(4 << 20)
	for _, file := range files {
		if remaining <= 0 {
			break
		}
		body, openErr := s.content.OpenStarterWorkspaceObject(ctx, file.ObjectID)
		if openErr != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(body, min(remaining, 256<<10)+1))
		closeErr := body.Close()
		if readErr != nil || closeErr != nil || len(data) > 256<<10 || int64(len(data)) > remaining || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			continue
		}
		remaining -= int64(len(data))
		result = append(result, string(data))
	}
	return result, nil
}
