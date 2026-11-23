// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	roleBindingExpiryPageSize = 128
	roleBindingExpiryPages    = 8
)

// NewRoleBindingExpiryTask closes local sockets whose bindings expired since
// this node last checked. Each node keeps its own cursor so expiry does not
// depend on receiving cluster messages.
func (s *Service) NewRoleBindingExpiryTask(bindings store.RoleBindingStore) job.PeriodicTask {
	return job.PeriodicTask{
		Name:     "role-binding-expiry",
		Interval: time.Second,
		Runner:   &roleBindingExpiry{bindings: bindings, closeUser: s.deps.Delivery.applyAuthorizationInvalidation},
	}
}

type roleBindingExpiry struct {
	bindings  store.RoleBindingStore
	closeUser func(string)
	cursor    store.RoleBindingExpiryCursor
}

func (r *roleBindingExpiry) Run(ctx context.Context) error {
	if r.bindings == nil {
		return errors.New("role binding expiry persistence is unavailable")
	}
	seen := make(map[model.UserID]struct{})
	for range roleBindingExpiryPages {
		page, err := r.bindings.ListExpiries(ctx, r.cursor, roleBindingExpiryPageSize)
		if err != nil {
			return err
		}
		for _, expiry := range page.Items {
			if !expiry.UserID.IsValid() {
				return errors.New("role binding expiry returned an invalid user")
			}
			if _, done := seen[expiry.UserID]; !done {
				r.closeUser(expiry.UserID.String())
				seen[expiry.UserID] = struct{}{}
			}
			r.cursor = expiry.Cursor
		}
		if len(page.Items) < roleBindingExpiryPageSize {
			r.cursor = store.RoleBindingExpiryCursor{At: page.CheckedAt}
			return nil
		}
	}
	return nil
}
