// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"errors"
	"fmt"
)

// distinctIdentifiers spells a bounded batch of typed identifiers for an
// ANY(?) read, once each in first-seen order. An invalid identifier or more
// than maximum distinct identifiers is rejected.
func distinctIdentifiers[T interface {
	~string
	IsValid() bool
}](ids []T, maximum int) ([]string, error) {
	seen := make(map[T]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if !id.IsValid() {
			return nil, errors.New("invalid identifier")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, string(id))
	}
	if len(result) > maximum {
		return nil, fmt.Errorf("more than %d identifiers", maximum)
	}
	return result, nil
}
