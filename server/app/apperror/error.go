// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package apperror is the error type the application returns for a failure a
// caller may see: a stable code, a few safe fields, and a wrapped cause that is
// for operators only and never reaches the caller. It knows nothing about HTTP;
// httpapi maps each code to a status from httpapi/route/error_catalog.yaml.
package apperror

import (
	"errors"
	"strings"
)

// Code is a dotted snake_case `<domain>[.<subdomain>].<condition>` name such
// as "exam.sitting.not_found". Every code must be listed in the HTTP error
// catalog; constant codes are checked against it.
type Code string

// Error is a transport-neutral application failure; only its code and fields
// are safe to show a caller.
type Error struct {
	code   Code
	fields map[string]string
	cause  error
}

// New panics if code is empty.
func New(code Code) *Error {
	if code == "" {
		panic("apperror: error code is empty")
	}
	return &Error{code: code}
}

// Invalid records the rejected input's name in the "field" public field.
func Invalid(code Code, field string) *Error {
	return New(code).WithField("field", field)
}

// Code returns "" for a nil receiver.
func (e *Error) Code() string {
	if e == nil {
		return ""
	}
	return string(e.code)
}

// Fields returns a copy, so callers cannot mutate the error.
func (e *Error) Fields() map[string]string {
	if e == nil {
		return nil
	}
	return cloneFields(e.fields)
}

// WithField attaches a public field; value must be safe to show a caller.
func (e *Error) WithField(key, value string) *Error {
	if e == nil {
		return nil
	}
	if e.fields == nil {
		e.fields = make(map[string]string, 1)
	}
	e.fields[key] = value
	return e
}

// WithFields copies fields into the error; values must be safe to show a
// caller.
func (e *Error) WithFields(fields map[string]string) *Error {
	if e == nil {
		return nil
	}
	if len(fields) == 0 {
		return e
	}
	if e.fields == nil {
		e.fields = make(map[string]string, len(fields))
	}
	for key, value := range fields {
		e.fields[key] = value
	}
	return e
}

// Wrap attaches a cause that is for operators only and must never be
// serialized to a client.
func (e *Error) Wrap(err error) *Error {
	if e != nil {
		e.cause = err
	}
	return e
}

// Error includes the cause, so it is for operator logs, not callers.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	var builder strings.Builder
	builder.WriteString(string(e.code))
	if e.cause != nil {
		builder.WriteString(": ")
		builder.WriteString(e.cause.Error())
	}
	return builder.String()
}

// Unwrap returns nil when there is no cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is reports whether err wraps an *Error with code. Matching never mints a
// code, so it accepts any string-typed value.
func Is[C ~string](err error, code C) bool {
	got, ok := As(err)
	return ok && string(got.code) == string(code)
}

// As finds the first *Error in err's chain.
func As(err error) (*Error, bool) {
	var applicationError *Error
	if errors.As(err, &applicationError) {
		return applicationError, true
	}
	return nil, false
}

// CodeOf returns the first *Error's code in err's chain, or fallback.
func CodeOf(err error, fallback Code) string {
	if got, ok := As(err); ok {
		return got.Code()
	}
	return string(fallback)
}

// Classify is CodeOf with a typed result. A presenter passes its fallback as
// a constant so the error catalog check sees every code it can present.
func Classify(err error, fallback Code) Code {
	if got, ok := As(err); ok {
		return got.code
	}
	return fallback
}

func cloneFields(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
