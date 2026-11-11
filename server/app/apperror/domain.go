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
