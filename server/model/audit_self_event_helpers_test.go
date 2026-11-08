// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "sort"

// AuditSelfEvents returns every self-history event name, sorted.
func AuditSelfEvents() []AuditSelfEvent {
	seen := make(map[AuditSelfEvent]struct{}, len(auditSelfEventRules))
	names := make([]AuditSelfEvent, 0, len(auditSelfEventRules))
	for _, rule := range auditSelfEventRules {
		if _, ok := seen[rule.event]; ok {
			continue
		}
		seen[rule.event] = struct{}{}
		names = append(names, rule.event)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}
