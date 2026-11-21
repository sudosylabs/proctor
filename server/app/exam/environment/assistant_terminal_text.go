// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const assistantOutputSeparator = "\n[output omitted]\n"

type assistantEscape uint8

const (
	assistantPlain assistantEscape = iota
	assistantEscapeStart
	assistantEscapeIntermediate
	assistantCSI
	assistantControlString
	assistantControlStringEscape
)

// assistantTerminalText decodes across attachment events. An incomplete rune
// or escape at the end of a poll is replayed from its original host offset.
type assistantTerminalText struct {
	mode          assistantEscape
	escapeOffset  uint64
	partial       []byte
	partialOffset uint64
}

func (s *assistantTerminalText) append(data []byte, offset uint64) []byte {
	if len(s.partial) != 0 {
		offset = s.partialOffset
		data = append(s.partial, data...)
		s.partial = nil
	}
	text := make([]byte, 0, len(data))
	for len(data) != 0 {
		if !utf8.FullRune(data) {
			s.partial, s.partialOffset = append([]byte(nil), data...), offset
			break
		}
		r, size := utf8.DecodeRune(data)
		if s.printable(r, offset) {
			text = utf8.AppendRune(text, r)
		}
		data, offset = data[size:], offset+uint64(size) //nolint:gosec // G115: DecodeRune consumed one to four bytes.
	}
	return text
}

func (s *assistantTerminalText) printable(r rune, offset uint64) bool {
	switch s.mode {
	case assistantEscapeStart:
		switch r {
		case '[':
			s.mode = assistantCSI
		case ']', 'P', 'X', '^', '_':
			s.mode = assistantControlString
		default:
			s.mode = assistantPlain
			if r >= 0x20 && r <= 0x2f {
				s.mode = assistantEscapeIntermediate
			}
		}
	case assistantEscapeIntermediate:
		if r < 0x20 || r > 0x2f {
			s.mode = assistantPlain
		}
	case assistantCSI:
		if r >= 0x40 && r <= 0x7e {
			s.mode = assistantPlain
		}
	case assistantControlString, assistantControlStringEscape:
		switch {
		case r == '\a' || r == '\u009c' || (s.mode == assistantControlStringEscape && r == '\\'):
			s.mode = assistantPlain
		case r == '\x1b':
			s.mode = assistantControlStringEscape
		default:
			s.mode = assistantControlString
		}
	case assistantPlain:
		switch r {
		case '\x1b':
			s.mode = assistantEscapeStart
		case '\u009b':
			s.mode = assistantCSI
		case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
			s.mode = assistantControlString
		default:
			return !unicode.IsControl(r) || r == '\n' || r == '\t'
		}
		s.escapeOffset = offset
	}
	return false
}

func (s *assistantTerminalText) checkpoint(next uint64) uint64 {
	if s.mode != assistantPlain {
		return s.escapeOffset
	}
	if len(s.partial) != 0 {
		return s.partialOffset
	}
	return next
}

func sessionStreamBytes(data AssistantStream) []byte {
	if !data.Dropped {
		return append(data.Head, data.Tail...)
	}
	// The stream kept two disjoint spans, and either edge can split a rune.
	head := strings.ToValidUTF8(string(data.Head), "")
	tail := strings.ToValidUTF8(string(data.Tail), "")
	return []byte(head + assistantOutputSeparator + tail)
}

// CleanAssistantScreen strips control sequences from a complete screen.
func CleanAssistantScreen(screen string) string {
	var terminal assistantTerminalText
	return string(terminal.append([]byte(screen), 0))
}
