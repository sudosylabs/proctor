// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/model"
)

// The read bounds. A turn reads at most turnReadBytes for the model, so a
// workspace cannot crowd out the rest of the conversation.
const (
	listResultBytes  = 16 << 10
	readResultBytes  = 32 << 10
	searchResultMax  = 100
	searchFilesMax   = 64
	turnReadBytes    = 256 << 10
	terminalMaxBytes = 16 << 10
)

// charge counts a result against the turn's read bound, cutting what does
// not fit.
func (r *turnRun) charge(text string) (string, bool) {
	room := turnReadBytes - r.readBytes
	if room <= 0 {
		return "", false
	}
	text = truncateUTF8(text, room)
	r.readBytes += len(text)
	return text, true
}

func readResult(r *turnRun, text string, truncated bool) toolResult {
	charged, ok := r.charge(text)
	if !ok {
		return toolError("This turn has read as much as it may; answer with what you have.")
	}
	truncated = truncated || len(charged) < len(text)
	if truncated {
		charged += "\n[truncated]"
	}
	return toolResult{forModel: charged, record: charged, outcome: "ok", truncated: truncated}
}

func (r *turnRun) listFiles(arguments json.RawMessage) toolResult {
	var input struct {
		Prefix string `json:"prefix"`
	}
	if json.Unmarshal(arguments, &input) != nil {
		return toolError("The arguments do not match the tool.")
	}
	items, cut, err := r.manifest()
	if err != nil {
		return toolError("The workspace cannot be read now.")
	}
	var listing strings.Builder
	truncated := cut
	for _, item := range items {
		if !strings.HasPrefix(item.Path, input.Prefix) {
			continue
		}
		line := item.Path + "/\n"
		if item.Kind == model.StarterWorkspaceEntryFile {
			line = item.Path + " (" + strconv.FormatInt(item.SizeBytes, 10) + " bytes)\n"
		}
		if listing.Len()+len(line) > listResultBytes {
			truncated = true
			break
		}
		listing.WriteString(line)
	}
	if resources, resourceErr := r.visibleResources(); resourceErr == nil {
		for _, resource := range resources {
			path := resourcePath(resource.ResourceID)
			if !strings.HasPrefix(path, input.Prefix) {
				continue
			}
			line := path + " (Exam Resource: " + resource.DisplayName + ")\n"
			if listing.Len()+len(line) > listResultBytes {
				truncated = true
				break
			}
			listing.WriteString(line)
		}
	}

	if listing.Len() == 0 {
		return readResult(r, "No entries.", truncated)
	}
	return readResult(r, listing.String(), truncated)
}

func (r *turnRun) readFile(arguments json.RawMessage) toolResult {
	var input struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
	}
	if json.Unmarshal(arguments, &input) != nil || input.Path == "" || input.StartLine < 0 {
		return toolError("The arguments do not match the tool.")
	}
	if strings.HasPrefix(input.Path, "resource:") {
		id, parseErr := model.ParseExamResourceID(strings.TrimPrefix(input.Path, "resource:"))
		if parseErr != nil {
			return toolError("No such resource.")
		}
		text, cut, readErr := r.readResource(id, fileReadMaximumBytes)
		if readErr != nil {
			return toolError("That resource cannot be read as text now.")
		}
		lines := strings.Split(text, "\n")
		start := max(input.StartLine, 1)
		if start > len(lines) {
			return toolError("The resource has no line at that offset.")
		}
		result := strings.Join(lines[start-1:], "\n")
		bounded := truncateUTF8(result, readResultBytes)
		return readResult(r, bounded, cut || len(bounded) < len(result))
	}

	items, _, err := r.manifest()
	if err != nil {
		return toolError("The workspace cannot be read now.")
	}
	item, found := findFile(items, input.Path)
	if !found || item.Kind != model.StarterWorkspaceEntryFile {
		return toolError("No such file.")
	}
	text, truncated, err := r.readText(item, fileReadMaximumBytes)
	if errors.Is(err, errNotText) {
		return toolError("The file is not text.")
	}
	if err != nil {
		return toolError("The file cannot be read now.")
	}
	lines := strings.Split(text, "\n")
	start := max(input.StartLine, 1)
	if start > len(lines) {
		return toolError("The file has " + strconv.Itoa(len(lines)) + " lines.")
	}
	var numbered strings.Builder
	for index := start - 1; index < len(lines); index++ {
		line := fmt.Sprintf("%d\t%s\n", index+1, lines[index])
		if numbered.Len()+len(line) > readResultBytes {
			truncated = true
			break
		}
		numbered.WriteString(line)
	}
	return readResult(r, numbered.String(), truncated)
}

func (r *turnRun) search(arguments json.RawMessage) toolResult {
	var input struct {
		Query  string `json:"query"`
		Prefix string `json:"prefix"`
	}
	if json.Unmarshal(arguments, &input) != nil || strings.TrimSpace(input.Query) == "" || len(input.Query) > 256 {
		return toolError("The arguments do not match the tool.")
	}
	items, cut, err := r.manifest()
	if err != nil {
		return toolError("The workspace cannot be read now.")
	}
	query := strings.ToLower(input.Query)
	var found strings.Builder
	matches, files := 0, 0
	truncated := cut
	for _, item := range items {
		if item.Kind != model.StarterWorkspaceEntryFile || !strings.HasPrefix(item.Path, input.Prefix) {
			continue
		}
		if files >= searchFilesMax || matches >= searchResultMax {
			truncated = true
			break
		}
		text, _, readErr := r.readText(item, fileReadMaximumBytes)
		if readErr != nil {
			continue
		}
		files++
		for number, line := range strings.Split(text, "\n") {
			if !strings.Contains(strings.ToLower(line), query) {
				continue
			}
			found.WriteString(fmt.Sprintf("%s:%d: %s\n", item.Path, number+1, truncateUTF8(line, 240)))
			matches++
			if matches >= searchResultMax {
				break
			}
		}
	}
	if resources, resourceErr := r.visibleResources(); resourceErr == nil {
		for _, resource := range resources {
			path := resourcePath(resource.ResourceID)
			if !strings.HasPrefix(path, input.Prefix) {
				continue
			}
			if files >= searchFilesMax || matches >= searchResultMax {
				truncated = true
				break
			}
			text, cut, readErr := r.readResource(resource.ResourceID, fileReadMaximumBytes)
			if readErr != nil {
				continue
			}
			files++
			truncated = truncated || cut
			for number, line := range strings.Split(text, "\n") {
				if !strings.Contains(strings.ToLower(line), query) {
					continue
				}
				found.WriteString(fmt.Sprintf("%s:%d: %s\n", path, number+1, truncateUTF8(line, 240)))
				matches++
				if matches >= searchResultMax {
					break
				}
			}
		}
	}

	if matches == 0 {
		return readResult(r, "No matches.", truncated)
	}
	return readResult(r, found.String(), truncated)
}

func (r *turnRun) readTerminal(arguments json.RawMessage) toolResult {
	var input struct {
		Terminal int `json:"terminal"`
	}
	if json.Unmarshal(arguments, &input) != nil || input.Terminal < 1 || input.Terminal > environment.MaxTerminalOpens {
		return toolError("The arguments do not match the tool.")
	}
	screen, err := r.service.terminals.ViewTerminal(r.ctx, r.inv, r.environmentAccess(), environment.TerminalTag(input.Terminal))
	if err != nil {
		return toolError("That terminal cannot be read now.")
	}
	text := strings.TrimRight(strings.Join(screen.Text, "\n"), "\n")
	if !screen.Running {
		text += "\n[the terminal has exited]"
	}
	truncated := len(text) > terminalMaxBytes
	if truncated {
		text = text[len(text)-terminalMaxBytes:]
		text = strings.ToValidUTF8(text, "")
	}
	return readResult(r, text, truncated)
}

func (r *turnRun) environmentAccess() environment.CandidateAccess {
	return environment.CandidateAccess{AttemptID: r.access.AttemptID, ConnectionID: r.access.ConnectionID,
		ContinuityCredential: r.access.ContinuityCredential}
}

func resourcePath(id model.ExamResourceID) string { return "resource:" + id.String() }
func (r *turnRun) visibleResources() ([]attempt.Resource, error) {
	presentation, err := r.service.attempts.GetCandidateExamPresentation(r.ctx, r.inv, r.access)
	if err != nil {
		return nil, err
	}
	return presentation.Resources, nil
}
func (r *turnRun) readResource(id model.ExamResourceID, limit int) (string, bool, error) {
	opened, err := r.service.attempts.OpenCandidateExamResource(r.ctx, r.inv, attempt.OpenResourceQuery{Access: r.access, ResourceID: id})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = opened.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(opened.Body, int64(limit)+1))
	if err != nil {
		return "", false, err
	}
	cut := len(data) > limit
	if cut {
		data = data[:limit]
		for back := 0; back < 3 && len(data) > 0 && !utf8.Valid(data); back++ {
			data = data[:len(data)-1]
		}
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", false, errNotText
	}
	return string(data), cut, nil
}
