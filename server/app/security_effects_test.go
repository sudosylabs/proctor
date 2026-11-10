// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/store"
)

// accessCredentialCachePrefix is the namespace the identity area's cache
// invalidator deletes access-credential entries from.
const accessCredentialCachePrefix = "authentication/access/"

type securityInstitutionStore struct{ store.InstitutionStore }

func TestRealtimeServiceRequiresSecurityDependencies(t *testing.T) {
	t.Parallel()

	invalidator, err := identity.NewCacheInvalidator(identity.CacheInvalidatorDependencies{Cache: newAuthenticationCacheFake(), Diagnostics: newDiscardLogger(t)})
	require.NoError(t, err)
	_, err = newRealtimeService(nil, newDiscardLogger(t))
	require.Error(t, err, "nil authentication invalidator was accepted")
	_, err = newRealtimeService(invalidator, nil)
	require.Error(t, err, "nil realtime diagnostics were accepted")
}

// TestAuthenticationAndRealtimeRetainOnlyNarrowSiblingPorts keeps the
// realtime service from holding authentication. Authentication cannot hold
// the realtime service at all: it lives in the identity area, which never
// imports this package, and its own fields are checked there.
func TestAuthenticationAndRealtimeRetainOnlyNarrowSiblingPorts(t *testing.T) {
	t.Parallel()

	realtimeType := reflect.TypeOf(RealtimeService{})
	identityPointerType := reflect.TypeOf((*identity.Service)(nil))
	for index := 0; index < realtimeType.NumField(); index++ {
		field := realtimeType.Field(index)
		require.NotEqual(t, identityPointerType, field.Type, "RealtimeService retains the identity Service in field %q", field.Name)
	}
}
