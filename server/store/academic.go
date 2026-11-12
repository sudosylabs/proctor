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

// AcademicUnitCommandResult and AcademicPeriodCommandResult report whether a
// named mutation executed or returned its previously committed outcome.
type AcademicUnitCommandResult struct {
	Value    *model.AcademicUnit
	Replayed bool
}

type AcademicPeriodCommandResult struct {
	Value    *model.AcademicPeriod
	Replayed bool
}

// InstitutionStore persists the institution represented by this installation.
type InstitutionStore interface {
	Save(context.Context, *model.Institution) (*model.Institution, error)
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

// AcademicUnitCreation is the complete durable input for creating an Academic
// Unit and completing its already-persisted mutation audit in one transaction.
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
	// ExpectedRevision is optional for compatibility; positive values fence stale callers.
	ExpectedRevision int64
	ID               model.AcademicUnitID
	ArchiveAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// AcademicNameListOptions selects one page of an academic listing ordered by
// name then identity. Term matches the name or display name as a literal,
// case-insensitive substring; empty matches every row. AfterName and AfterID
// are the exclusive continuation, both set or both empty. Limit is 1..201 so
// the caller can probe one row past a full page. Descending reverses the
// order; the continuation then moves toward smaller names.
type AcademicNameListOptions struct {
	Term       string
	Descending bool
	AfterName  string
	AfterID    string
	Limit      int
}

// AcademicPeriodListOptions selects one page of Academic Periods ordered by
// start, name, then identity, with the same Term, continuation, and Limit
// rules as AcademicNameListOptions.
type AcademicPeriodListOptions struct {
	Term         string
	AfterStartAt time.Time
	AfterName    string
	AfterID      model.AcademicPeriodID
	Limit        int
}

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
	// ExpectedRevision is optional for compatibility; positive values fence stale callers.
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
	// CountByAcademicUnit counts the Programmes SearchByAcademicUnit would
	// return across every page for the same Term, ignoring the continuation,
	// order, and limit, and stops at maximum (see MaximumListCount).
	CountByAcademicUnit(ctx context.Context, academicUnitID model.AcademicUnitID, options AcademicNameListOptions, maximum int) (int, error)
	// SeedUpdate is an unaudited fixture write; production code never calls it.
	SeedUpdate(context.Context, *model.Programme) (*model.Programme, error)
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
	// ExpectedRevision is optional for compatibility; positive values fence stale callers.
	ExpectedRevision int64
	ID               model.ProgrammeLevelID
	ArchiveAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ProgrammeLevelStore persists reusable curriculum stages owned by programmes.
type ProgrammeLevelStore interface {
	Create(context.Context, *ProgrammeLevelCreation) (*model.ProgrammeLevel, error)
	Update(context.Context, *ProgrammeLevelUpdate) (*model.ProgrammeLevel, error)
	Archive(context.Context, *ProgrammeLevelArchive) (*model.ProgrammeLevel, error)
	Get(context.Context, model.ProgrammeLevelID) (*model.ProgrammeLevel, error)
	GetByName(context.Context, model.ProgrammeID, string) (*model.ProgrammeLevel, error)
	ListByProgramme(context.Context, model.ProgrammeID) ([]*model.ProgrammeLevel, error)
	SearchByProgramme(context.Context, model.ProgrammeID, AcademicNameListOptions) ([]*model.ProgrammeLevel, error)
	// CountByProgramme counts the Programme Levels SearchByProgramme would
	// return across every page for the same Term, ignoring the continuation,
	// order, and limit, and stops at maximum (see MaximumListCount).
	CountByProgramme(ctx context.Context, programmeID model.ProgrammeID, options AcademicNameListOptions, maximum int) (int, error)
	// SeedUpdate is an unaudited fixture write; production code never calls it.
	SeedUpdate(context.Context, *model.ProgrammeLevel) (*model.ProgrammeLevel, error)
}

// AcademicPeriodVisibilityScope constrains list/search to institution-owned
// periods plus unit-owned periods visible through authorized subtree roots.
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
	// ExpectedRevision is optional for compatibility; positive values fence stale callers.
	ExpectedRevision int64
	ID               model.AcademicPeriodID
	ArchiveAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// AcademicPeriodStore persists institution- or Academic-Unit-owned enrollment
// periods.
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

// ClassVisibilityScope is the persistence-ready class.view authority of a
// cross-scope Class search: every Class Institution-wide, the Classes under
// the named Academic Unit subtrees, and the named Classes.
type ClassVisibilityScope struct {
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// ClassSearchOptions selects up to Limit (1..201) unarchived Classes under
// unarchived Programme Levels and Programmes whose name or display name
// contains Term as a literal, case-insensitive substring, restricted to
// Visibility and ordered by name then identity. An empty Visibility matches
// nothing.
type ClassSearchOptions struct {
	Term       string
	Limit      int
	Visibility ClassVisibilityScope
}

// ClassStore persists concrete programme-level rosters for academic periods.
type ClassStore interface {
	Create(context.Context, *ClassCreation) (*model.Class, error)
	Update(context.Context, *ClassUpdate) (*model.Class, error)
	Archive(context.Context, *ClassArchive) (*model.Class, error)
	Get(context.Context, model.ClassID) (*model.Class, error)
	GetByName(context.Context, model.ProgrammeLevelID, model.AcademicPeriodID, string) (*model.Class, error)
	ListByProgrammeLevel(context.Context, model.ProgrammeLevelID, AcademicNameListOptions) ([]*model.Class, error)
	ListByAcademicPeriod(context.Context, model.AcademicPeriodID) ([]*model.Class, error)
	SearchByAcademicUnit(context.Context, model.AcademicUnitID, AcademicNameListOptions) ([]*model.Class, error)
	// CountByProgrammeLevel and CountByAcademicUnit count the Classes the
	// matching listing would return across every page for the same Term,
	// ignoring the continuation, order, and limit, and stop at maximum (see
	// MaximumListCount).
	CountByProgrammeLevel(ctx context.Context, programmeLevelID model.ProgrammeLevelID, options AcademicNameListOptions, maximum int) (int, error)
	CountByAcademicUnit(ctx context.Context, academicUnitID model.AcademicUnitID, options AcademicNameListOptions, maximum int) (int, error)
	SearchVisible(context.Context, ClassSearchOptions) ([]*model.Class, error)
	GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error)
}

// InstitutionArchive is the input to InstitutionStore.Archive.
type InstitutionArchive struct {
	ID        model.InstitutionID
	ArchiveAt time.Time
}
