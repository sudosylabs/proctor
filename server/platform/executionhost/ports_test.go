// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package executionhost

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
)

func TestGuestPortAdapter(t *testing.T) {
	t.Parallel()
	hosts, host, _ := memoryHosts(t, true)
	m, err := hosts.Ensure(t.Context(), testGrantA, testSpec)
	require.NoError(t, err)
	fence := environment.Fence{GrantID: m.GrantID(), Epoch: m.Epoch(), Revision: 1}
	_, err = m.Control(t.Context(), fence, environment.ControlRunning)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "candidate preview")
	}))
	t.Cleanup(server.Close)
	listener, ok := server.Listener.(*net.TCPListener)
	require.True(t, ok)
	address, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := address.Port
	_, err = m.ConnectPort(t.Context(), fence, port)
	require.ErrorIs(t, err, environment.ErrPortRefused)

	adapter, ok := m.(*machine)
	require.True(t, ok)
	require.NoError(t, host.RegisterPortListener(adapter.env, listener, "terminal:1"))
	ports, err := m.ListPorts(t.Context())
	require.NoError(t, err)
	require.Equal(t, []environment.ListeningPort{{Port: port, Address: address.IP.String(), Tag: "terminal:1"}}, ports)

	conn, err := m.ConnectPort(t.Context(), fence, port)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = io.WriteString(conn, "GET / HTTP/1.0\r\nHost: localhost\r\n\r\n")
	require.NoError(t, err)
	require.NoError(t, conn.CloseWrite())
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "candidate preview", string(body))

	half, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = half.Close() })
	require.NoError(t, half.SetDeadline(time.Now().Add(5*time.Second)))
	halfAddress, ok := half.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.NoError(t, host.RegisterPortListener(adapter.env, half))
	client, err := m.ConnectPort(t.Context(), fence, halfAddress.Port)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	peer, err := half.AcceptTCP()
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	require.NoError(t, peer.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = io.WriteString(client, "ping")
	require.NoError(t, err)
	require.NoError(t, client.CloseWrite())
	sent, err := io.ReadAll(peer)
	require.NoError(t, err)
	require.Equal(t, "ping", string(sent))
	_, err = io.WriteString(peer, "pong")
	require.NoError(t, err)
	require.NoError(t, peer.CloseWrite())
	received, err := io.ReadAll(client)
	require.NoError(t, err)
	require.Equal(t, "pong", string(received))
}
