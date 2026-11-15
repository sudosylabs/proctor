// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package logging

import (
	"context"
	"log/slog"
	"strings"
)

// Slog returns a standard-library logger that writes through l, for code that
// takes a *slog.Logger, such as plugins. Attribute values go through Any, so
// the same bounded-value rule applies; groups become dotted key prefixes.
func (l *Logger) Slog() *slog.Logger {
	return slog.New(slogHandler{logger: l})
}

type slogHandler struct {
	logger *Logger
	group  string
}

func (h slogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.logger.Enabled(level)
}

func (h slogHandler) Handle(ctx context.Context, record slog.Record) error {
	fields := make([]Field, 0, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		fields = appendSlogAttr(fields, h.group, attr)
		return true
	})
	h.logger.Log(ctx, record.Level, record.Message, fields...)
	return nil
}

func (h slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	fields := make([]Field, 0, len(attrs))
	for _, attr := range attrs {
		fields = appendSlogAttr(fields, h.group, attr)
	}
	return slogHandler{logger: h.logger.With(fields...), group: h.group}
}

func (h slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return slogHandler{logger: h.logger, group: groupKey(h.group, name)}
}

func appendSlogAttr(fields []Field, group string, attr slog.Attr) []Field {
	value := attr.Value.Resolve()
	if attr.Key == "" && value.Kind() != slog.KindGroup {
		return fields
	}
	if value.Kind() == slog.KindGroup {
		nested := groupKey(group, attr.Key)
		for _, member := range value.Group() {
			fields = appendSlogAttr(fields, nested, member)
		}
		return fields
	}
	return append(fields, Any(groupKey(group, attr.Key), value.Any()))
}

func groupKey(group, key string) string {
	if group == "" {
		return key
	}
	if key == "" {
		return group
	}
	return strings.Join([]string{group, key}, ".")
}
