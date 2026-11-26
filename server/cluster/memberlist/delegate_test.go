// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package memberlist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	hashimemberlist "github.com/hashicorp/memberlist"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/cluster"
)

func TestNodeMetadataAdvertisesOnlyCompiledProtocolAndNeverTruncates(t *testing.T) {
	t.Parallel()

	transport := &Transport{cfg: Config{NodeID: "node-a", ServerVersion: "test"}}
	delegate := &delegate{transport: transport}
	payload := delegate.NodeMeta(hashimemberlist.MetaMaxSize)
	var meta nodeMeta
	require.NoError(t, json.Unmarshal(payload, &meta))
	require.Equal(t, supportedProtocolMin, meta.ProtocolMin, "advertised protocol")
	require.Equal(t, supportedProtocolMax, meta.ProtocolMax, "advertised protocol")
	require.Nil(t, delegate.NodeMeta(len(payload)-1))
}

func TestTransportConfigurationCopiesAndValidatesKeyring(t *testing.T) {
	t.Parallel()

	primary := bytes.Repeat([]byte{1}, 32)
	fallback := bytes.Repeat([]byte{2}, 32)
	cfg := validInternalConfig(primary)
	cfg.DecryptionKeys = [][]byte{fallback}
	transport, err := New(cfg)
	require.NoError(t, err)
	primary[0] = 9
	fallback[0] = 9
	require.Equal(t, byte(1), transport.cfg.EncryptionKey[0], "transport configuration aliases caller key material")
	require.Equal(t, byte(2), transport.cfg.DecryptionKeys[0][0], "transport configuration aliases caller key material")

	duplicate := validInternalConfig(bytes.Repeat([]byte{3}, 32))
	duplicate.DecryptionKeys = [][]byte{bytes.Repeat([]byte{3}, 32)}
	_, err = New(duplicate)
	require.Error(t, err)
	require.Equal(t, "cluster encryption keyring contains duplicate keys", err.Error())
}

func TestTransportRejectsOversizedLocalMetadataBeforeStart(t *testing.T) {
	t.Parallel()

	cfg := validInternalConfig(bytes.Repeat([]byte{1}, 32))
	cfg.ServerVersion = strings.Repeat("v", 129)
	_, err := New(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "server_version is invalid")
}

func TestBroadcastMembersStopsOnCancellationAndPreservesSendErrors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sendErr := errors.New("send failed")
	members := []*hashimemberlist.Node{
		{Name: "node-local"},
		{Name: "node-a"},
		{Name: "node-b"},
	}
	var sent []string
	err := broadcastMembers(
		ctx,
		"node-local",
		members,
		[]byte("payload"),
		func(member *hashimemberlist.Node, _ []byte) error {
			sent = append(sent, member.Name)
			cancel()
			return sendErr
		},
	)
	require.ErrorIs(t, err, sendErr, "broadcast error")
	require.ErrorIs(t, err, context.Canceled, "broadcast error")
	require.Len(t, sent, 1, "sent peers")
	require.Equal(t, "node-a", sent[0], "sent peers")
}

func validInternalConfig(key []byte) Config {
	return Config{
		NodeID:             "node-a",
		BindAddress:        "127.0.0.1:7946",
		AdvertiseAddress:   "127.0.0.1:7946",
		EncryptionKey:      key,
		Discovery:          NewMemoryDiscovery(),
		DiscoveryTTL:       5 * time.Second,
		DiscoveryHeartbeat: time.Second,
		ServerVersion:      "test",
		Logger:             recordingAdmissionLogger{},
	}
}

func TestMemberlistReceiveContinuesWhileAnotherHandlerWaits(t *testing.T) {
	transport, err := New(validInternalConfig(make([]byte, 32)))
	require.NoError(t, err)
	started := make(chan struct{})
	second := make(chan struct{})
	require.NoError(t, transport.RegisterHandler("first", func(ctx context.Context, _ *cluster.Message) error {
		close(started)
		<-ctx.Done()
		return nil
	}))
	require.NoError(t, transport.RegisterHandler("second", func(context.Context, *cluster.Message) error { close(second); return nil }))
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() { transport.receive(ctx) })
	}
	defer func() { cancel(); workers.Wait() }()
	send := func(event cluster.Event) {
		raw, err := json.Marshal(wireEnvelope{Version: wireProtocolVersion, Source: "node-b", Message: &cluster.Message{Event: event}})
		require.NoError(t, err)
		(&delegate{transport: transport}).NotifyMsg(raw)
	}
	send("first")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first handler did not start")
	}
	send("second")
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("second handler was blocked")
	}
}
