// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package executionhost

import (
	"context"
	"errors"
	"io"

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/model"
)

func (m *machine) ConnectPort(ctx context.Context, fence environment.Fence, port int) (environment.PortConn, error) {
	conn, err := m.env.ConnectPort(ctx, toFence(fence), port)
	if err != nil {
		return nil, translate("connect port", err)
	}
	return &portConn{conn: conn}, nil
}

func (m *machine) ListPorts(ctx context.Context) ([]environment.ListeningPort, error) {
	ports, err := m.env.ListPorts(ctx)
	if err != nil {
		return nil, m.fail("list ports", err)
	}
	result := make([]environment.ListeningPort, len(ports))
	for i, port := range ports {
		result[i] = environment.ListeningPort{Port: port.Port, Address: port.Address, Tag: model.AttemptEnvironmentProcessTag(port.Tag)}
	}
	return result, nil
}

type portConn struct{ conn execenv.PortConn }

// A guest socket failure is local to this connection. Forgetting the host
// client here would also interrupt unrelated previews and terminals.
func portError(operation string, err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	if errors.Is(err, execenv.ErrConnection) {
		return hostError(operation, environment.ErrPortConnection)
	}
	return translate(operation, err)
}

func (c *portConn) Read(p []byte) (int, error) {
	n, err := c.conn.Read(p)
	return n, portError("read port", err)
}

func (c *portConn) Write(p []byte) (int, error) {
	n, err := c.conn.Write(p)
	return n, portError("write port", err)
}

func (c *portConn) CloseWrite() error {
	return portError("half-close port", c.conn.CloseWrite())
}

func (c *portConn) Close() error {
	return portError("close port", c.conn.Close())
}
