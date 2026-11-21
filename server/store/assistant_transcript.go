// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// AssistantMessageOperation is the idempotency and audit operation of a
// candidate's assistant message.
const AssistantMessageOperation = "exam.attempt.assistant.message"

// AssistantRecipe is the immutable admission copy; no credential is stored in it.
type AssistantRecipe struct {
	Policy       model.AssistantPolicy
	Provider     string
	Model        string
	DisplayName  string
	Pseudonymize bool
	Execution    bool
}

// AssistantRunAccess fences background work by both live candidate proof and
// an unguessable owner token. Lease expiry cannot be renewed retroactively.
type AssistantRunAccess struct {
	Editor     ExamAttemptWorkspaceEditorAccess
	TurnID     model.AssistantTurnID
	OwnerToken string
}

type AssistantMessageStart struct {
	Access         ExamAttemptWorkspaceEditorAccess
	ConversationID model.AssistantConversationID
	TurnID         model.AssistantTurnID
	OwnerToken     string
	Text           string
	AuditEventID   model.AuditEventID
	AuditAt        time.Time
}
type AssistantRunStart struct {
	RemainingInputTokens  int64
	RemainingOutputTokens int64
	Recipe                AssistantRecipe
	ConversationID        model.AssistantConversationID
	TurnID                model.AssistantTurnID
	Replayed              bool
}

// AssistantModelReservation burns allowances before a provider call. Finishing
// reconciles accurate usage; a lost owner is charged its full reservation.
type AssistantModelReservation struct {
	Access       AssistantRunAccess
	InputTokens  int64
	OutputTokens int64
}

type AssistantModelCompletion struct {
	Access AssistantRunAccess
	Usage  model.AssistantUsage
}

// AssistantRunCancel is the candidate stopping a turn. It needs no owner
// token, so any node can stop a turn another node runs: the owner loses its
// fence and stops at its next store call.
type AssistantRunCancel struct {
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Editor       ExamAttemptWorkspaceEditorAccess
	TurnID       model.AssistantTurnID
}

// AssistantCommandReservation is one run_command charged before it starts.
type AssistantCommandReservation struct {
	CallID         model.AssistantCallID
	TimeoutSeconds int64
	Access         AssistantRunAccess
}

// AssistantCommandSlot names the process tag a reserved command runs under.
// The sequence is per attempt and never reused.
type AssistantCommandSlot struct {
	Tag            string
	TimeoutSeconds int64
}

// AssistantCallDecision is the candidate answering an approval request. Like
// a cancel it needs no owner token: the owner may run on another node and
// reads the decision back.
type AssistantCallDecision struct {
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Editor       ExamAttemptWorkspaceEditorAccess
	CallID       model.AssistantCallID
	Accept       bool
}

type AssistantTurnAppend struct {
	Access AssistantRunAccess
	Turn   model.AssistantTurn
	Gap    bool
}
type AssistantCallAppend struct {
	Access AssistantRunAccess
	Call   model.AssistantCall
	Gap    bool
}

// AssistantTranscriptOwnerAccess reads only the candidate's own history,
// including after submission. The owning application validates the principal.
type AssistantTranscriptOwnerAccess struct {
	AttemptID       model.ExamAttemptID
	CandidateUserID model.UserID
	SessionID       model.SessionID
	SealedOnly      bool
}

type AssistantTranscriptPage struct {
	Records      []model.AssistantRecord
	Inventory    model.AssistantInventory
	NextSequence int64
}
type AssistantTranscriptQuery struct {
	AttemptID          model.ExamAttemptID
	AfterSequence      int64
	Limit              int
	Owner              *AssistantTranscriptOwnerAccess
	ReviewSubmissionID model.SubmissionID
}

// AssistantTranscriptStore owns the append-only history and the durable run
// fence. A submission binds the inventory and records any unfinished turn in
// the same transaction, without waiting for a model or execution host.
type AssistantTranscriptStore interface {
	Start(context.Context, *AssistantMessageStart, *CommandIdempotency) (*AssistantRunStart, error)
	Renew(context.Context, AssistantRunAccess) error
	Cancel(context.Context, AssistantRunCancel) error
	ReserveCommand(context.Context, *AssistantCommandReservation) (*AssistantCommandSlot, error)
	Decide(context.Context, AssistantCallDecision) (*model.AssistantRecord, error)
	Decision(context.Context, AssistantRunAccess, model.AssistantCallID) (string, error)
	ReserveModel(context.Context, *AssistantModelReservation) error
	CompleteModel(context.Context, *AssistantModelCompletion) (model.AssistantUsage, error)
	AppendTurn(context.Context, *AssistantTurnAppend) (*model.AssistantRecord, error)
	AppendCall(context.Context, *AssistantCallAppend) (*model.AssistantRecord, error)
	CompleteCall(context.Context, *AssistantCallAppend) (*model.AssistantRecord, error)
	ListForAttempt(context.Context, AssistantTranscriptQuery) (*AssistantTranscriptPage, error)
	Inventory(context.Context, AssistantTranscriptQuery) (model.AssistantInventory, error)
}
