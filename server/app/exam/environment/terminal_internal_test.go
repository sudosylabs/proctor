// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestProcessOriginsFollowDescent(t *testing.T) {
	t.Parallel()
	var origins processOrigins
	events := []ProcessEvent{
		{Sequence: 1, Kind: processStarted, PID: 10, ParentPID: 1, Tag: "terminal:1"},
		{Sequence: 2, Kind: processStarted, PID: 11, ParentPID: 10},
		{Sequence: 3, Kind: processStarted, PID: 12, ParentPID: 11},
		{Sequence: 4, Kind: processStarted, PID: 20, ParentPID: 1},
		{Sequence: 5, Kind: processExited, PID: 12},
	}
	origins.attribute("epoch-a", events)
	require.Equal(t, []string{"terminal:1", "terminal:1", "terminal:1", "", "terminal:1"},
		[]string{events[0].Origin, events[1].Origin, events[2].Origin, events[3].Origin, events[4].Origin})
	require.Equal(t, "terminal:1", events[0].Tag, "the host's tag stays")
	require.Empty(t, events[1].Tag)

	// A reused PID is the new process; a later page continues the map.
	later := []ProcessEvent{
		{Sequence: 6, Kind: processStarted, PID: 12, ParentPID: 20},
		{Sequence: 7, Kind: processStarted, PID: 13, ParentPID: 11},
	}
	origins.attribute("epoch-a", later)
	require.Empty(t, later[0].Origin)
	require.Equal(t, "terminal:1", later[1].Origin)

	// Another machine starts again.
	other := []ProcessEvent{{Sequence: 1, Kind: processStarted, PID: 13, ParentPID: 11}}
	origins.attribute("epoch-b", other)
	require.Empty(t, other[0].Origin)

	encoded, err := json.Marshal(events[1])
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"origin":"terminal:1"`)
	require.NotContains(t, string(encoded), `"tag"`)
}

// inputStore records terminal input appends.
type inputStore struct {
	store.ExamAttemptWorkspaceStore
	mu      sync.Mutex
	records []store.ExamAttemptTerminalInputAppend
}

func (s *inputStore) AppendTerminalInput(_ context.Context, input *store.ExamAttemptTerminalInputAppend) (*store.ExamAttemptEvidenceAppendResult, error) {
	if !input.Valid() {
		return nil, store.NewErrInvalidInput("attempt_evidence", "terminal_input", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, *input)
	return &store.ExamAttemptEvidenceAppendResult{}, nil
}

func (s *inputStore) all() []store.ExamAttemptTerminalInputAppend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.ExamAttemptTerminalInputAppend(nil), s.records...)
}

func recordedInput(t *testing.T, record store.ExamAttemptTerminalInputAppend) (uint64, string, []inputEvent) {
	t.Helper()
	var detail inputDetail
	require.NoError(t, json.Unmarshal(record.Detail, &detail))
	var data []byte
	for _, event := range detail.Events {
		data = append(data, event.Input...)
	}
	return detail.Offset, string(data), detail.Events
}

func TestInputRecorderCoalescesAndDeduplicates(t *testing.T) {
	t.Parallel()
	evidence := &inputStore{}
	terminals := &Terminals{config: TerminalsConfig{Evidence: evidence, Now: time.Now, InputFlush: time.Hour, InputChunkBytes: 8}}
	attemptID := model.NewExamAttemptID()
	// Four bytes were taken before the session attached: another session's.
	recorder := newInputRecorder(terminals, attemptID, 2, "terminal:1", 4)

	recorder.input(0, []byte("abcdef")) // only "ef" is this session's
	recorder.input(4, []byte("ef"))     // a resend records nothing
	recorder.resize(Window{Cols: 90, Rows: 20})
	recorder.input(6, []byte("ghijklmnop")) // fills the chunk at 8 bytes
	records := evidence.all()
	require.Len(t, records, 1)
	offset, data, _ := recordedInput(t, records[0])
	require.EqualValues(t, 4, offset)
	require.Equal(t, "efghijkl", data)
	require.EqualValues(t, 5, records[0].Sequence, "a record's sequence is its first offset plus one")
	require.Equal(t, model.NewEnvironmentWorkspaceAuthor(2, "terminal:1"), records[0].Author())

	// A gap another session filled starts a new record.
	recorder.input(20, []byte("xy"))
	recorder.close(context.Background())
	records = evidence.all()
	require.Len(t, records, 3)
	offset, data, _ = recordedInput(t, records[1])
	require.EqualValues(t, 12, offset)
	require.Equal(t, "mnop", data)
	offset, data, _ = recordedInput(t, records[2])
	require.EqualValues(t, 20, offset)
	require.Equal(t, "xy", data)
	require.EqualValues(t, 21, records[2].Sequence)

	// A window change with no input after it is not kept.
	recorder.resize(Window{Cols: 80, Rows: 24})
	recorder.close(context.Background())
	require.Len(t, evidence.all(), 3)
}

func TestInputRecorderFlushesAfterItsWindow(t *testing.T) {
	t.Parallel()
	evidence := &inputStore{}
	terminals := &Terminals{config: TerminalsConfig{Evidence: evidence, Now: time.Now, InputFlush: 10 * time.Millisecond,
		InputChunkBytes: DefaultInputChunkBytes}}
	recorder := newInputRecorder(terminals, model.NewExamAttemptID(), 0, "terminal:2", 0)
	recorder.resize(Window{Cols: 100, Rows: 30})
	recorder.input(0, []byte("ls\n"))
	require.Eventually(t, func() bool { return len(evidence.all()) == 1 }, time.Second, 5*time.Millisecond)
	_, data, events := recordedInput(t, evidence.all()[0])
	require.Equal(t, "ls\n", data)
	require.EqualValues(t, 100, events[0].Cols, "the window change is kept with the input after it")
}
