// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type institutionNameStoreFake struct {
	store.InstitutionStore
	reads       int
	institution *model.Institution
	err         error
}

func (f *institutionNameStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	f.reads++
	return f.institution, f.err
}

func TestInstitutionNameCacheBacksOffAfterAFailedRead(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	persistence := &institutionNameStoreFake{err: errors.New("unavailable")}
	cache := &InstitutionNameCache{institutions: persistence, now: func() time.Time { return now }}

	assert.Empty(t, cache.Name())
	assert.Empty(t, cache.Name())
	assert.Equal(t, 1, persistence.reads, "a failed read is retried only after the retry delay")

	persistence.err, persistence.institution = nil, &model.Institution{DisplayName: " Lagoon City University "}
	now = now.Add(institutionNameRetryDelay)
	assert.Equal(t, "Lagoon City University", cache.Name())
	now = now.Add(institutionNameCacheLifetime - time.Second)
	assert.Equal(t, "Lagoon City University", cache.Name())
	assert.Equal(t, 2, persistence.reads, "a read name is reused for the cache lifetime")

	persistence.err = errors.New("unavailable")
	now = now.Add(time.Second)
	assert.Equal(t, "Lagoon City University", cache.Name(), "a failed refresh keeps the last known name")
}
