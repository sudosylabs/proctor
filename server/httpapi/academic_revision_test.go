// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAcademicRevisionHTTPContract(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"academic_unit", "programme", "programme_level", "academic_period", "class"} {
		t.Run(kind, func(t *testing.T) {
			logger, _ := routetest.NewLogger(t)
			principal := academicCaller()
			subject := academicRevisionHTTPFixture(t, kind, principal)
			api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, subject.resource)
			call := func(method, body string) *httptest.ResponseRecorder {
				request := httptest.NewRequestWithContext(t.Context(), method, subject.path, strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer credential")
				if body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				response := httptest.NewRecorder()
				api.ServeHTTP(response, request)
				return response
			}

			// A stated revision fences the change: the current one passes and
			// any other is a conflict that never reaches the Store.
			subject.setRevision(7)
			response := call(http.MethodPatch, `{"expected_revision":7}`)
			require.Equal(t, http.StatusOK, response.Code, "patch status = %d: %s", response.Code, response.Body.String())
			var result map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Equal(t, float64(8), result["revision"], "the response carries the revision the patch wrote: %#v", result)
			require.Equal(t, 1, subject.updates(), "the fenced patch reached the Store")
			assertHTTPProblem(t, call(http.MethodPatch, `{"expected_revision":8}`), http.StatusConflict, subject.conflict)
			require.Equal(t, 1, subject.updates(), "a stale patch reached the Store")
			for _, body := range []string{`{"expected_revision":null}`, `{"expected_revision":0}`, `{"expected_revision":-1}`, `{"expected_revision":1.5}`} {
				require.Equal(t, http.StatusBadRequest, call(http.MethodPatch, body).Code, "invalid patch %s", body)
			}
			require.Equal(t, 1, subject.updates(), "an invalid patch reached the Store")
			// A legacy patch states no revision, so any current one passes.
			subject.setRevision(9)
			require.Equal(t, http.StatusOK, call(http.MethodPatch, `{}`).Code, "legacy patch failed")
			require.Equal(t, 2, subject.updates(), "legacy patch failed")

			subject.setRevision(7)
			require.Equal(t, http.StatusNoContent, call(http.MethodDelete, `{"expected_revision":7}`).Code, "archive precondition failed")
			require.Equal(t, []int64{7}, subject.archived(), "archive precondition failed")
			assertHTTPProblem(t, call(http.MethodDelete, `{"expected_revision":8}`), http.StatusConflict, subject.conflict)
			for _, body := range []string{`{"expected_revision":null}`, `{"expected_revision":0}`, `{"expected_revision":-1}`, `{"expected_revision":"7"}`, `{"revision":7}`} {
				require.Equal(t, http.StatusBadRequest, call(http.MethodDelete, body).Code, "invalid archive %s", body)
			}
			require.Equal(t, []int64{7}, subject.archived(), "a stale or invalid archive reached the Store")
			subject.setRevision(9)
			require.Equal(t, http.StatusNoContent, call(http.MethodDelete, "").Code, "legacy archive failed")
			require.Equal(t, []int64{7, 9}, subject.archived(), "a legacy archive is fenced by the revision it read")
		})
	}
}

// academicRevisionSubject is one revisioned academic resource behind its
// route. setRevision changes its stored revision; updates counts the patches
// that reached its Store and archived lists the revision each archive was
// fenced by.
type academicRevisionSubject struct {
	resource    route.Resource
	path        string
	conflict    string
	setRevision func(int64)
	updates     func() int
	archived    func() []int64
}

func academicRevisionHTTPFixture(t *testing.T, kind string, principal model.Principal) academicRevisionSubject {
	t.Helper()
	world := newAcademicWorld()
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, world.unit.ID,
			model.ActionAcademicUnitManage, model.ActionProgrammeManage, model.ActionProgrammeLevelManage,
			model.ActionAcademicPeriodManage, model.ActionClassManage)},
		Units: world.units(),
	})
	fixture.hold(world)
	switch kind {
	case "academic_unit":
		return academicRevisionSubject{
			resource: academicUnitResource(fixture.service), path: "/api/v1/academic-units/" + world.unit.ID.String(),
			conflict: "academic_unit.conflict", setRevision: func(revision int64) { world.unit.Revision = revision },
			updates: func() int { return len(fixture.units.updates) },
			archived: func() []int64 {
				return academicArchivedRevisions(fixture.units.archives, func(input *store.AcademicUnitArchive) int64 { return input.ExpectedRevision })
			},
		}
	case "programme":
		return academicRevisionSubject{
			resource: programmeResource(fixture.service), path: "/api/v1/programmes/" + world.programme.ID.String(),
			conflict: "programme.conflict", setRevision: func(revision int64) { world.programme.Revision = revision },
			updates: func() int { return len(fixture.programmes.updates) },
			archived: func() []int64 {
				return academicArchivedRevisions(fixture.programmes.archives, func(input *store.ProgrammeArchive) int64 { return input.ExpectedRevision })
			},
		}
	case "programme_level":
		return academicRevisionSubject{
			resource: programmeLevelResource(fixture.service), path: "/api/v1/programme-levels/" + world.level.ID.String(),
			conflict: "programme_level.conflict", setRevision: func(revision int64) { world.level.Revision = revision },
			updates: func() int { return len(fixture.levels.updates) },
			archived: func() []int64 {
				return academicArchivedRevisions(fixture.levels.archives, func(input *store.ProgrammeLevelArchive) int64 { return input.ExpectedRevision })
			},
		}
	case "academic_period":
		return academicRevisionSubject{
			resource: academicPeriodResource(fixture.service), path: "/api/v1/academic-periods/" + world.period.ID.String(),
			conflict: "academic_period.conflict", setRevision: func(revision int64) { world.period.Revision = revision },
			updates: func() int { return len(fixture.periods.updates) },
			archived: func() []int64 {
				return academicArchivedRevisions(fixture.periods.archives, func(input *store.AcademicPeriodArchive) int64 { return input.ExpectedRevision })
			},
		}
	case "class":
		return academicRevisionSubject{
			resource: classResource(fixture.service, fixture.people()), path: "/api/v1/classes/" + world.class.ID.String(),
			conflict: "class.conflict", setRevision: func(revision int64) { world.class.Revision = revision },
			updates: func() int { return len(fixture.classes.updates) },
			archived: func() []int64 {
				return academicArchivedRevisions(fixture.classes.archives, func(input *store.ClassArchive) int64 { return input.ExpectedRevision })
			},
		}
	default:
		panic("unknown academic fixture")
	}
}

// academicArchivedRevisions is the revision each archive input was fenced by.
func academicArchivedRevisions[T any](inputs []T, revision func(T) int64) []int64 {
	revisions := []int64{}
	for _, input := range inputs {
		revisions = append(revisions, revision(input))
	}
	return revisions
}
