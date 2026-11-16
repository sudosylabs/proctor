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

// DomainInvalid keeps a model.ValidationError's safe fields on the new code.
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

// WorkCapacityExceeded marks a content error raised when bounded content work
// is full. The content adapter cannot import this package, so callers detect
// it with errors.As and present it as service.busy.
type WorkCapacityExceeded interface{ WorkCapacityExceeded() }

// InvalidExamResourceContent marks a content error raised when exam resource
// bytes fail validation; detect it as for WorkCapacityExceeded.
type InvalidExamResourceContent interface{ InvalidExamResourceContent() }
