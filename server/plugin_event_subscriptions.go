// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"github.com/sudosylabs/proctor/server/app/events"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// eventSubscriptions hands the plugins' guarded event handlers to the
// domain-event dispatcher.
func eventSubscriptions(host *pluginhost.Host) []events.Subscription {
	var subscriptions []events.Subscription
	for _, subscription := range host.EventSubscriptions() {
		subscriptions = append(subscriptions, events.Subscription{
			Key:     subscription.Plugin + ":" + string(subscription.Topic),
			Topic:   subscription.Topic,
			Handler: subscription.Handler,
		})
	}
	return subscriptions
}
