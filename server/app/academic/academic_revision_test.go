// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAcademicClientRevisionPreconditions(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"academic_unit", "programme", "programme_level", "academic_period", "class"} {
		for _, operation := range []string{"update", "archive"} {
			for _, scenario := range []string{"omitted", "current", "stale", "zero", "denied"} {
				t.Run(kind+"/"+operation+"/"+scenario, func(t *testing.T) {
					events := []string{}
					denied := scenario == "denied"
					update, archive, revision := academicRevisionFixture(t, kind, &events, denied)
					var expected *int64
					value := int64(7)
					if scenario != "omitted" {
						expected = &value
					}
					if scenario == "stale" || denied {
						value = 6
					}
					if scenario == "zero" {
						value = 0
					}
					call := update
					if operation == "archive" {
						call = archive
					}
					err := call(expected)
					code := ""
					switch scenario {
					case "stale":
						code = kind + ".conflict"
					case "zero":
						code = "request.invalid"
					case "denied":
						code = "authorization.denied"
					}
					if code != "" {
						require.True(t, apperror.Is(err, code), "error = %v, want %s", err, code)
						decided := false
						for _, event := range events {
							require.False(t, strings.HasPrefix(event, "store-"))
							require.False(t, strings.HasPrefix(event, "audit-begin"))
							decided = decided || strings.HasPrefix(event, "decision:")
						}
						require.True(t, decided, "the caller is authorized before the revision is checked: %v", events)
					} else {
						require.NoError(t, err)
						want := int64(8)
						if operation == "archive" {
							want = 7
						}
						require.Equal(t, want, revision(operation), "Store revision")
					}
				})
			}
		}
	}
}

func academicRevisionFixture(t *testing.T, kind string, events *[]string, denied bool) (func(*int64) error, func(*int64) error, func(string) int64) {
	t.Helper()
	ctx := context.Background()
	principal, call := newCaller()
	now := func() time.Time { return model.TimeFromMillis(500) }
	at := model.TimeFromMillis(100)
	// A denied caller holds only the view action of the same kind, at the same
	// scope, so the resource still resolves and the decision is a refusal.
	granted := func(manage, view model.Action) model.Action {
		if denied {
			return view
		}
		return manage
	}
	switch kind {
	case "academic_unit":
		current := &model.AcademicUnit{ID: model.NewAcademicUnitID(), CreatedAt: at, UpdatedAt: at, Revision: 7, Name: "academic", DisplayName: "Academic", InstitutionID: model.NewInstitutionID()}
		access := academicUnitAccess(t, events, nil, unitGrant(principal.UserID, current.ID, granted(model.ActionAcademicUnitManage, model.ActionAcademicUnitView)))
		persistence := &academicUnitMutationStore{events: events, current: current, updated: current, archived: current}
		service := mustNew(Dependencies{Store: &catalogFake{academicUnit: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: now, NewID: model.NewID, Publisher: academicUnitPublisherIn(events)})
		return func(expected *int64) error {
				_, err := service.UpdateAcademicUnit(ctx, call, UpdateAcademicUnitCommand{ID: current.ID, ExpectedRevision: expected})
				return err
			},
			func(expected *int64) error {
				return service.ArchiveAcademicUnit(ctx, call, ArchiveAcademicUnitCommand{ID: current.ID, ExpectedRevision: expected})
			},
			func(operation string) int64 {
				if operation == "archive" {
					return persistence.archiveInput.ExpectedRevision
				}
				return persistence.updateInput.Unit.Revision
			}
	case "programme":
		current := &model.Programme{ID: model.NewProgrammeID(), CreatedAt: at, UpdatedAt: at, Revision: 7, Name: "academic", DisplayName: "Academic", AcademicUnitID: model.NewAcademicUnitID()}
		persistence := &programmeStoreFake{events: events, current: current}
		access := accessWith(t, events, accesstest.Options{
			Grants:     []accesstest.Grant{unitGrant(principal.UserID, current.AcademicUnitID, granted(model.ActionProgrammeManage, model.ActionProgrammeView))},
			Programmes: persistence,
		})
		service := mustNew(Dependencies{Store: &catalogFake{programme: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: now, NewID: model.NewID})
		return func(expected *int64) error {
				_, err := service.UpdateProgramme(ctx, call, UpdateProgrammeCommand{ID: current.ID, ExpectedRevision: expected})
				return err
			},
			func(expected *int64) error {
				return service.ArchiveProgramme(ctx, call, ArchiveProgrammeCommand{ID: current.ID, ExpectedRevision: expected})
			},
			func(operation string) int64 {
				if operation == "archive" {
					return persistence.archiveInput.ExpectedRevision
				}
				return persistence.updateInput.Programme.Revision
			}
	case "programme_level":
		programme := &model.Programme{ID: model.NewProgrammeID(), AcademicUnitID: model.NewAcademicUnitID()}
		current := &model.ProgrammeLevel{ID: model.NewProgrammeLevelID(), CreatedAt: at, UpdatedAt: at, Revision: 7, Name: "academic", DisplayName: "Academic", ProgrammeID: programme.ID}
		persistence := &programmeLevelStoreFake{events: events, current: current}
		access := accessWith(t, events, accesstest.Options{
			Grants:          []accesstest.Grant{unitGrant(principal.UserID, programme.AcademicUnitID, granted(model.ActionProgrammeLevelManage, model.ActionProgrammeLevelView))},
			Programmes:      programmesOf(programme),
			ProgrammeLevels: persistence,
		})
		service := mustNew(Dependencies{Store: &catalogFake{programmeLevel: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: now, NewID: model.NewID})
		return func(expected *int64) error {
				_, err := service.UpdateProgrammeLevel(ctx, call, UpdateProgrammeLevelCommand{ID: current.ID, ExpectedRevision: expected})
				return err
			},
			func(expected *int64) error {
				return service.ArchiveProgrammeLevel(ctx, call, ArchiveProgrammeLevelCommand{ID: current.ID, ExpectedRevision: expected})
			},
			func(operation string) int64 {
				if operation == "archive" {
					return persistence.archiveInput.ExpectedRevision
				}
				return persistence.updateInput.Level.Revision
			}
	case "academic_period":
		current := &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), CreatedAt: at, UpdatedAt: at, Revision: 7, Name: "academic", DisplayName: "Academic", Owner: model.NewInstitutionAcademicPeriodOwner(model.NewInstitutionID()), StartsAt: at, EndsAt: at.Add(time.Hour)}
		persistence := &academicPeriodStoreFake{events: events, current: current}
		access := academicPeriodAccess(t, events, persistence, nil, institutionGrant(principal.UserID, granted(model.ActionAcademicPeriodManage, model.ActionAcademicPeriodView)))
		service := mustNew(Dependencies{Store: &catalogFake{academicPeriod: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: now, NewID: model.NewID})
		return func(expected *int64) error {
				_, err := service.UpdateAcademicPeriod(ctx, call, UpdateAcademicPeriodCommand{ID: current.ID, ExpectedRevision: expected})
				return err
			},
			func(expected *int64) error {
				return service.ArchiveAcademicPeriod(ctx, call, ArchiveAcademicPeriodCommand{ID: current.ID, ExpectedRevision: expected})
			},
			func(operation string) int64 {
				if operation == "archive" {
					return persistence.archiveInput.ExpectedRevision
				}
				return persistence.updateInput.Period.Revision
			}
	case "class":
		world := newClassWorld(1)
		current := &model.Class{ID: model.NewClassID(), CreatedAt: at, UpdatedAt: at, Revision: 7, Name: "academic", DisplayName: "Academic", ProgrammeLevelID: world.levels[0].ID, AcademicPeriodID: world.period.ID}
		persistence := &classStoreFake{events: events, current: current, unitID: world.unitID.String()}
		access := world.access(t, events, persistence, unitGrant(principal.UserID, world.unitID, granted(model.ActionClassManage, model.ActionClassView)))
		service := mustNew(Dependencies{Store: &catalogFake{class: persistence}, Authorizer: access.Authorizer, Auditor: access.Auditor, Now: now, NewID: model.NewID})
		return func(expected *int64) error {
				_, err := service.UpdateClass(ctx, call, UpdateClassCommand{ID: current.ID, ExpectedRevision: expected})
				return err
			},
			func(expected *int64) error {
				return service.ArchiveClass(ctx, call, ArchiveClassCommand{ID: current.ID, ExpectedRevision: expected})
			},
			func(operation string) int64 {
				if operation == "archive" {
					return persistence.archiveInput.ExpectedRevision
				}
				return persistence.updateInput.Class.Revision
			}
	default:
		require.FailNow(t, fmt.Sprintf("unknown academic kind %s", kind))
		return nil, nil, nil
	}
}
