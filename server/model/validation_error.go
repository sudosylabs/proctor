// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"errors"
	"fmt"
	"strings"
)

// ValidationError is a transport-neutral validation failure with a stable code
// and a safe field name, but no HTTP status, localization or request context.
// The model builds it only through invalidModelError. Field is empty when the
// failure concerns the whole value. A non-empty Field reaches clients as a
// Problem Details field, so naming one is a public change. Conditions callers
// match with errors.Is are package sentinels instead.
type ValidationError struct {
	Code    string
	Field   string
	Reason  string
	Details string
	Model   string

	cause error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString(e.Code)
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	if e.Details != "" {
		b.WriteString(" (")
		b.WriteString(e.Details)
		b.WriteString(")")
	}
	if e.cause != nil {
		b.WriteString(": ")
		b.WriteString(e.cause.Error())
	}
	return b.String()
}

// Unwrap returns the failure a wrapping validation reported, or nil.
func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ErrorCode returns the stable machine-readable validation code.
func (e *ValidationError) ErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// SafeFields returns the structured context that is safe to show clients.
func (e *ValidationError) SafeFields() map[string]string {
	if e == nil || e.Field == "" {
		return nil
	}
	return map[string]string{"field": e.Field}
}

// invalidModelError builds every inline model failure. where names the
// reporting function for readability and is never serialized.
func invalidModelError(where, modelName, field, reason, details string) *ValidationError {
	_ = where
	code := fmt.Sprintf("model.%s.is_valid.%s.app_error", modelName, field)
	if field == "" {
		code = fmt.Sprintf("model.%s.is_valid.app_error", modelName)
	}
	return &ValidationError{
		Code:    code,
		Field:   field,
		Reason:  reason,
		Details: details,
		Model:   modelName,
	}
}

// wrap records cause. A whole-value failure takes the field of a wrapped
// ValidationError, so wrapping never hides the inner safe field.
func (e *ValidationError) wrap(cause error) *ValidationError {
	e.cause = cause
	var inner *ValidationError
	if e.Field == "" && errors.As(cause, &inner) && inner.Field != "" {
		e.Field = inner.Field
	}
	return e
}
