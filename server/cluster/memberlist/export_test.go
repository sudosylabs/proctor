// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package memberlist

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Loopback test ports come from below the kernel ephemeral ranges (Linux
// 32768-60999, macOS 49152-65535). A port the kernel picks for 127.0.0.1:0 can
// be handed straight to an outbound connection of a parallel test node once the
// probe closes it, so the later bind fails with "address already in use".
const (
	testPortLow  = 20000
	testPortHigh = 32767
)

var (
	testPortsMu     sync.Mutex
	testPortsIssued = map[int]struct{}{}
)

// FreeTestPort returns a loopback port that is free for both TCP and UDP and
// that no other test in this binary has been given.
func FreeTestPort(t *testing.T) int {
	t.Helper()
	testPortsMu.Lock()
	defer testPortsMu.Unlock()
	for range 200 {
		offset, err := rand.Int(rand.Reader, big.NewInt(testPortHigh-testPortLow+1))
		require.NoError(t, err)
		port := testPortLow + int(offset.Int64())
		if _, issued := testPortsIssued[port]; issued || !loopbackPortFree(t.Context(), port) {
			continue
		}
		testPortsIssued[port] = struct{}{}
		return port
	}
	require.FailNow(t, "no free loopback test port")
	return 0
}

func loopbackPortFree(ctx context.Context, port int) bool {
	address := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port))
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", address)
	if err != nil {
		return false
	}
	_ = listener.Close()
	packet, err := config.ListenPacket(ctx, "udp", address)
	if err != nil {
		return false
	}
	_ = packet.Close()
	return true
}
