// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package apperror

import (
	"errors"

	"github.com/sudosylabs/proctor/server/model"
)

// DomainInvalid maps a domain ValidationError into a stable application code
// while preserving safe field context for transport mapping.
func DomainInvalid(code Code, err error) error {
	out := New(code)
	var validation *model.ValidationError
	if errors.As(err, &validation) {
		if fields := validation.SafeFields(); len(fields) > 0 {
			out = out.WithFields(fields)
		}
	}
	return out.Wrap(err)
}

// WorkCapacityExceeded marks a content error meaning the bounded content
// work is full. The content adapter cannot import the application's error
// types, so callers detect it with errors.As and present it as service.busy.
type WorkCapacityExceeded interface{ WorkCapacityExceeded() }

// InvalidExamResourceContent marks a content error meaning Exam resource
// bytes failed validation. Callers detect it with errors.As, for the same
// reason as WorkCapacityExceeded.
type InvalidExamResourceContent interface{ InvalidExamResourceContent() }
