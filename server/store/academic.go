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

// AcademicUnitCommandResult is a unit command's result; Replayed means an
// earlier committed outcome was returned.
type AcademicUnitCommandResult struct {
	Value    *model.AcademicUnit
	Replayed bool
}

// AcademicPeriodCommandResult is a period command's result; Replayed means an
// earlier committed outcome was returned.
type AcademicPeriodCommandResult struct {
	Value    *model.AcademicPeriod
	Replayed bool
}

// InstitutionStore persists the installation's one institution.
type InstitutionStore interface {
	Get(context.Context, model.InstitutionID) (*model.Institution, error)
	GetSingleton(context.Context) (*model.Institution, error)
	Update(context.Context, *InstitutionUpdate) (*model.Institution, error)
	Archive(context.Context, *InstitutionArchive) error
}

type InstitutionUpdate struct {
	Institution  *model.Institution
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// AcademicUnitCreation creates a unit and completes its recorded audit attempt
// in one transaction.
type AcademicUnitCreation struct {
	Unit         *model.AcademicUnit
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type AcademicUnitUpdate struct {
	Unit         *model.AcademicUnit
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type AcademicUnitArchive struct {
	// ExpectedRevision zero skips the revision fence.
	ExpectedRevision int64
	ID               model.AcademicUnitID
	ArchiveAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// AcademicNameListOptions pages a listing by name then ID. Term is a literal,
// case-insensitive substring of the name or display name; empty matches all.
// AfterName and AfterID are both set or both empty. Limit (1..201) includes
// the one-row look-ahead.
type AcademicNameListOptions struct {
	Term       string
	Descending bool
	AfterName  string
	AfterID    string
	Limit      int
}

// AcademicPeriodListOptions pages periods by start, name, then ID (the
// default order) or by name then ID, reversed when Descending, with the Term,
// continuation and Limit rules of AcademicNameListOptions. A start-order
// continuation sets AfterStartAt, AfterName and AfterID; a name-order one
// sets AfterName and AfterID.
//
// OwnerType keeps the periods of one kind of owner, and OwnerID, when set, of
// that owner; a non-zero ActiveAt keeps the periods covering that instant.
// A non-zero OverlapsFrom or OverlapsUntil keeps the periods whose
// [start, end) shares an instant with [OverlapsFrom, OverlapsUntil).
type AcademicPeriodListOptions struct {
	Term          string
	OwnerType     model.ResourceType
	OwnerID       string
	ActiveAt      time.Time
	OverlapsFrom  time.Time
	OverlapsUntil time.Time
	Order         AcademicPeriodOrder
	Descending    bool
	AfterStartAt  time.Time
	AfterName     string
	AfterID       model.AcademicPeriodID
	Limit         int
}

// AcademicPeriodOrder is the order of a period page; the zero value is start.
type AcademicPeriodOrder string

const (
	AcademicPeriodOrderStart AcademicPeriodOrder = "start"
	AcademicPeriodOrderName  AcademicPeriodOrder = "name"
)

// AcademicUnitStore persists nodes in the institution's academic-unit tree.
type AcademicUnitStore interface {
	Create(context.Context, *AcademicUnitCreation) (*model.AcademicUnit, error)
	CreateIdempotently(context.Context, *AcademicUnitCreation, *CommandIdempotency) (*AcademicUnitCommandResult, error)
	Update(context.Context, *AcademicUnitUpdate) (*model.AcademicUnit, error)
	Archive(context.Context, *AcademicUnitArchive) (*model.AcademicUnit, error)
	Get(context.Context, model.AcademicUnitID) (*model.AcademicUnit, error)
	GetByName(context.Context, model.InstitutionID, string) (*model.AcademicUnit, error)
	ListChildren(context.Context, model.InstitutionID, model.AcademicUnitID, AcademicNameListOptions) ([]*model.AcademicUnit, error)
	ListAncestors(context.Context, model.AcademicUnitID) ([]*model.AcademicUnit, error)
	Search(context.Context, model.InstitutionID, AcademicNameListOptions) ([]*model.AcademicUnit, error)
	// ListWithinSubtrees returns the listed units that are, or lie beneath,
	// one of the unarchived roots, in no particular order. It reads at most
	// MaximumAcademicUnitCountUnits units.
	ListWithinSubtrees(ctx context.Context, ids, roots []model.AcademicUnitID) ([]model.AcademicUnitID, error)
}

type ProgrammeCreation struct {
	Programme    *model.Programme
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type ProgrammeUpdate struct {
	Programme    *model.Programme
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type ProgrammeArchive struct {
	// ExpectedRevision zero skips the revision fence.
	ExpectedRevision int64
	ID               model.ProgrammeID
	ArchiveAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ProgrammeStore persists courses of study owned by academic units.
type ProgrammeStore interface {
	Create(context.Context, *ProgrammeCreation) (*model.Programme, error)
	Update(context.Context, *ProgrammeUpdate) (*model.Programme, error)
	Archive(context.Context, *ProgrammeArchive) (*model.Programme, error)
	Get(context.Context, model.ProgrammeID) (*model.Programme, error)
	GetByName(context.Context, model.AcademicUnitID, string) (*model.Programme, error)
	ListByAcademicUnit(context.Context, model.AcademicUnitID) ([]*model.Programme, error)
	SearchByAcademicUnit(context.Context, model.AcademicUnitID, AcademicNameListOptions) ([]*model.Programme, error)
	// CountByAcademicUnit counts what SearchByAcademicUnit would return across
	// every page, ignoring the continuation, order and limit, capped at
	// maximum (see MaximumListCount).
	CountByAcademicUnit(ctx context.Context, academicUnitID model.AcademicUnitID, options AcademicNameListOptions, maximum int) (int, error)
}

type ProgrammeLevelCreation struct {
	Level        *model.ProgrammeLevel
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type ProgrammeLevelUpdate struct {
	Level        *model.ProgrammeLevel
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type ProgrammeLevelArchive struct {
	// ExpectedRevision zero skips the revision fence.
	ExpectedRevision int64
	ID               model.ProgrammeLevelID
	ArchiveAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ProgrammeLevelStore persists reusable curriculum stages owned by programmes.
// ProgrammeLevelCount is one programme's number of unarchived levels.
type ProgrammeLevelCount struct {
	ProgrammeID model.ProgrammeID
	Count       int
}

type ProgrammeLevelStore interface {
	Create(context.Context, *ProgrammeLevelCreation) (*model.ProgrammeLevel, error)
	Update(context.Context, *ProgrammeLevelUpdate) (*model.ProgrammeLevel, error)
	Archive(context.Context, *ProgrammeLevelArchive) (*model.ProgrammeLevel, error)
	Get(context.Context, model.ProgrammeLevelID) (*model.ProgrammeLevel, error)
	GetByName(context.Context, model.ProgrammeID, string) (*model.ProgrammeLevel, error)
	ListByProgramme(context.Context, model.ProgrammeID) ([]*model.ProgrammeLevel, error)
	SearchByProgramme(context.Context, model.ProgrammeID, AcademicNameListOptions) ([]*model.ProgrammeLevel, error)
	// CountByProgramme counts what SearchByProgramme would return across
	// every page, ignoring the continuation, order and limit, capped at
	// maximum (see MaximumListCount).
	CountByProgramme(ctx context.Context, programmeID model.ProgrammeID, options AcademicNameListOptions, maximum int) (int, error)
	// CountByProgrammes counts each listed programme's unarchived levels in
	// one read, capped at maximum; every listed programme has an entry.
	CountByProgrammes(ctx context.Context, programmeIDs []model.ProgrammeID, maximum int) ([]ProgrammeLevelCount, error)
}

// AcademicPeriodVisibilityScope limits a listing to institution periods plus
// the periods of units inside the authorized subtrees.
type AcademicPeriodVisibilityScope struct {
	InstitutionID       model.InstitutionID
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
}

type AcademicPeriodCreation struct {
	Period       *model.AcademicPeriod
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type AcademicPeriodUpdate struct {
	Period       *model.AcademicPeriod
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type AcademicPeriodArchive struct {
	// ExpectedRevision zero skips the revision fence.
	ExpectedRevision int64
	ID               model.AcademicPeriodID
	ArchiveAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// AcademicPeriodStore persists enrollment periods, owned by the institution or
// by an academic unit.
type AcademicPeriodStore interface {
	Create(context.Context, *AcademicPeriodCreation) (*model.AcademicPeriod, error)
	CreateIdempotently(context.Context, *AcademicPeriodCreation, *CommandIdempotency) (*AcademicPeriodCommandResult, error)
	Update(context.Context, *AcademicPeriodUpdate) (*model.AcademicPeriod, error)
	Archive(context.Context, *AcademicPeriodArchive) (*model.AcademicPeriod, error)
	Get(context.Context, model.AcademicPeriodID) (*model.AcademicPeriod, error)
	GetByOwnerName(context.Context, model.Resource, string) (*model.AcademicPeriod, error)
	ListVisible(context.Context, AcademicPeriodVisibilityScope, AcademicPeriodListOptions) ([]*model.AcademicPeriod, error)
}

type ClassCreation struct {
	Class        *model.Class
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type ClassUpdate struct {
	Class                  *model.Class
	ExpectedAcademicUnitID model.AcademicUnitID
	ExpectedRevision       int64
	AuditEventID           model.AuditEventID
	AuditAt                time.Time
}

type ClassArchive struct {
	ID                     model.ClassID
	ExpectedAcademicUnitID model.AcademicUnitID
	ExpectedRevision       int64
	ArchiveAt              time.Time
	AuditEventID           model.AuditEventID
	AuditAt                time.Time
}

// ClassVisibilityScope is the caller's class.view authority: institution-wide,
// the classes under AcademicUnitRootIDs, and ClassIDs.
type ClassVisibilityScope struct {
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// ClassSearchOptions selects up to Limit (1..201) unarchived classes, under
// unarchived levels and programmes, whose name or display name contains Term
// (literal, case-insensitive), by name then ID. An empty Visibility matches
// nothing.
type ClassSearchOptions struct {
	Term       string
	Limit      int
	Visibility ClassVisibilityScope
}

// ClassStore persists classes: one programme level's roster in one period.
type ClassStore interface {
	Create(context.Context, *ClassCreation) (*model.Class, error)
	Update(context.Context, *ClassUpdate) (*model.Class, error)
	Archive(context.Context, *ClassArchive) (*model.Class, error)
	Get(context.Context, model.ClassID) (*model.Class, error)
	GetByName(context.Context, model.ProgrammeLevelID, model.AcademicPeriodID, string) (*model.Class, error)
	ListByProgrammeLevel(context.Context, model.ProgrammeLevelID, ClassListOptions) ([]*model.Class, error)
	ListByAcademicPeriod(context.Context, model.AcademicPeriodID) ([]*model.Class, error)
	SearchByAcademicUnit(context.Context, model.AcademicUnitID, ClassListOptions) ([]*model.Class, error)
	// CountByProgrammeLevel and CountByAcademicUnit count what the matching
	// listing would return across every page, ignoring the continuation, order
	// and limit, capped at maximum (see MaximumListCount).
	CountByProgrammeLevel(ctx context.Context, programmeLevelID model.ProgrammeLevelID, options ClassListOptions, maximum int) (int, error)
	CountByAcademicUnit(ctx context.Context, academicUnitID model.AcademicUnitID, options ClassListOptions, maximum int) (int, error)
	SearchVisible(context.Context, ClassSearchOptions) ([]*model.Class, error)
	GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error)
	// Tally counts, for each listed container, its classes or their current
	// students inside the visibility, in one read. Every listed container has
	// an entry, zero when nothing counts.
	Tally(context.Context, ClassTallyOptions) ([]ClassTally, error)
}

// ClassListOptions is a name page of classes, narrowed to one Academic Period
// when AcademicPeriodID is set.
type ClassListOptions struct {
	AcademicNameListOptions
	AcademicPeriodID model.AcademicPeriodID
}

// ClassTallyContainer is what a tally groups classes by.
type ClassTallyContainer string

const (
	ClassTallyByProgramme      ClassTallyContainer = "programme"
	ClassTallyByProgrammeLevel ClassTallyContainer = "programme_level"
	ClassTallyByAcademicPeriod ClassTallyContainer = "academic_period"
)

// ClassTallyOptions counts the unarchived classes, under unarchived levels
// and programmes, of up to MaximumAcademicUnitCountUnits containers, keeping
// only the classes inside Visibility. A non-zero StudentsAt counts the
// distinct students current at that instant in those classes instead. Each
// count stops at Maximum.
type ClassTallyOptions struct {
	By           ClassTallyContainer
	ContainerIDs []string
	Visibility   ClassVisibilityScope
	StudentsAt   time.Time
	Maximum      int
}

// ClassTally is one container's count.
type ClassTally struct {
	ContainerID string
	Count       int
}

// InstitutionArchive is the input to InstitutionStore.Archive.
type InstitutionArchive struct {
	ID        model.InstitutionID
	ArchiveAt time.Time
}
