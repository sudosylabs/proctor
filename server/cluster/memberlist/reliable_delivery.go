// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package memberlist

import (
	"context"
	"errors"
	"sync"
	"time"

	hashimemberlist "github.com/hashicorp/memberlist"

	"github.com/sudosylabs/proctor/server/cluster"
)

const (
	// These bound one peer's waiting envelopes. An envelope is at most
	// maxWireBytes, so the byte bound always admits the newest one.
	reliablePeerQueueEnvelopes = 64
	reliablePeerQueueBytes     = 8 * maxWireBytes
	// reliablePeerIdleTimeout ends a peer's delivery goroutine once its queue
	// has stayed empty this long; the next envelope starts a new one.
	reliablePeerIdleTimeout = time.Minute
)

// errReliableQueueFull reports an envelope dropped, oldest first, to make room
// for a newer one.
var errReliableQueueFull = errors.New("cluster peer reliable queue is full")

// reliableEnvelope's done receives the send result, errReliableQueueFull when
// dropped, or cluster.ErrStopped when delivery stops first.
type reliableEnvelope struct {
	node    *hashimemberlist.Node
	payload []byte
	done    func(error)
}

// reliableDelivery sends envelopes too large for one UDP packet, with one
// goroutine per peer. Memberlist dials TCP for every reliable send and blocks
// up to its timeout without a context, so callers only enqueue: a slow peer
// delays its own queue, never the caller or another peer.
type reliableDelivery struct {
	send             func(*hashimemberlist.Node, []byte) error
	maximumEnvelopes int
	maximumBytes     int
	idleTimeout      time.Duration

	mu      sync.Mutex
	peers   map[string]*reliablePeer
	stopped bool
	workers sync.WaitGroup
}

type reliablePeer struct {
	queue []reliableEnvelope
	bytes int
	wake  chan struct{}
}

func newReliableDelivery(send func(*hashimemberlist.Node, []byte) error) *reliableDelivery {
	return &reliableDelivery{
		send:             send,
		maximumEnvelopes: reliablePeerQueueEnvelopes,
		maximumBytes:     reliablePeerQueueBytes,
		idleTimeout:      reliablePeerIdleTimeout,
		peers:            make(map[string]*reliablePeer),
	}
}

// enqueue never waits for the network. A full queue drops its oldest envelopes
// until the new one fits.
func (d *reliableDelivery) enqueue(node *hashimemberlist.Node, payload []byte, done func(error)) {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		done(cluster.ErrStopped)
		return
	}

	peer, exists := d.peers[node.Name]
	if !exists {
		peer = &reliablePeer{wake: make(chan struct{}, 1)}
		d.peers[node.Name] = peer
		d.workers.Add(1)
		go d.deliver(node.Name, peer)
	}

	var dropped []reliableEnvelope
	for len(peer.queue) > 0 && (len(peer.queue) >= d.maximumEnvelopes || peer.bytes+len(payload) > d.maximumBytes) {
		dropped = append(dropped, peer.queue[0])
		peer.bytes -= len(peer.queue[0].payload)
		peer.queue[0] = reliableEnvelope{}
		peer.queue = peer.queue[1:]
	}

	peer.queue = append(peer.queue, reliableEnvelope{node: node, payload: payload, done: done})
	peer.bytes += len(payload)
	d.mu.Unlock()
	select {
	case peer.wake <- struct{}{}:
	default:
	}

	for _, envelope := range dropped {
		envelope.done(errReliableQueueFull)
	}
}

func (d *reliableDelivery) deliver(name string, peer *reliablePeer) {
	defer d.workers.Done()
	idle := time.NewTimer(d.idleTimeout)
	defer idle.Stop()
	for {
		d.mu.Lock()
		if len(peer.queue) == 0 {
			if d.stopped {
				d.mu.Unlock()
				return
			}

			d.mu.Unlock()
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}

			idle.Reset(d.idleTimeout)
			select {
			case <-peer.wake:
				continue
			case <-idle.C:
			}

			d.mu.Lock()
			if len(peer.queue) == 0 {
				if d.peers[name] == peer {
					delete(d.peers, name)
				}

				d.mu.Unlock()
				return
			}

			d.mu.Unlock()
			continue
		}

		envelope := peer.queue[0]
		peer.queue[0] = reliableEnvelope{}
		peer.queue = peer.queue[1:]
		peer.bytes -= len(envelope.payload)
		d.mu.Unlock()
		envelope.done(d.send(envelope.node, envelope.payload))
	}
}

// close fails every waiting envelope with cluster.ErrStopped. A send already
// inside memberlist cannot be interrupted; memberlist shutdown makes it fail.
func (d *reliableDelivery) close() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	var abandoned []reliableEnvelope
	for _, peer := range d.peers {
		abandoned = append(abandoned, peer.queue...)
		peer.queue, peer.bytes = nil, 0
		select {
		case peer.wake <- struct{}{}:
		default:
		}
	}
	d.mu.Unlock()
	for _, envelope := range abandoned {
		envelope.done(cluster.ErrStopped)
	}
}

func (d *reliableDelivery) wait(ctx context.Context) error {
	finished := make(chan struct{})
	go func() {
		d.workers.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
