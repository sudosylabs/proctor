// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestMailDeliveryListCountsTheFirstPageThroughTheListedOptions(t *testing.T) {
	t.Parallel()
	delivery := &model.MailDelivery{ID: model.NewMailDeliveryID(), CreatedAt: model.TimeFromMillis(100)}
	persistence := &mailStoreFake{delivery: delivery, countTotal: 3}
	authorizer := &mailAuthorizerFake{}
	service := &MailService{deps: mailServiceDependencies{Mail: persistence, Authorization: authorizer}}
	caller := invocation.New(mailTestPrincipal(time.Now()), model.RequestMetadata{})
	query := ListMailDeliveriesQuery{States: []model.MailDeliveryState{model.MailDeliveryFailed}, Limit: 10}
	first, err := service.ListMailDeliveries(context.Background(), caller, query)
	require.NoError(t, err)
	require.Equal(t, paging.Total{Counted: true, Total: 3}, first.Total)
	require.Equal(t, query.States, persistence.countOptions.States, "the count reads the list's filters")

	query.BeforeCreatedAt, query.BeforeID = delivery.CreatedAt, delivery.ID
	persistence.counts = 0
	later, err := service.ListMailDeliveries(context.Background(), caller, query)
	require.NoError(t, err)
	require.Equal(t, paging.Total{}, later.Total)
	require.Zero(t, persistence.counts, "a later page is not counted")

	failed, err := service.countFailedMailDeliveries(context.Background(), caller, MaximumAdministrationCount)
	require.NoError(t, err)
	require.Equal(t, 3, failed)
	require.Equal(t, store.MailDeliveryListOptions{States: []model.MailDeliveryState{model.MailDeliveryFailed}}, persistence.countOptions)
	require.Equal(t, []model.Action{model.ActionMailView, model.ActionMailView, model.ActionMailView}, authorizer.actions,
		"every read, the summary included, authorizes mail.view")
}
