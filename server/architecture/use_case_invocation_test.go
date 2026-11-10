// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// nonUseCaseSurface names the exported App methods that are not use cases,
// with the reason each takes no Invocation. Every other exported method is a
// use case.
var nonUseCaseSurface = map[string]string{
	"Jobs":                           "runtime handle that composition borrows",
	"AttachRealtimeSink":             "composition attaches the WebSocket sink once before readiness",
	"AttachRealtimeClusterFanout":    "composition attaches the cluster fan-out once before readiness",
	"AuthenticateAccess":             principalResolution,
	"AuthenticateBearer":             principalResolution,
	"AuthenticateDPoP":               principalResolution,
	"ValidateWebSocketPrincipal":     "WebSocket admission revalidates the Principal a connection was opened with before it builds any Invocation",
	"ValidatePrincipal":              "WebSocket admission and onboarding import revalidate a Principal established earlier before they build an Invocation from it",
	"AuthenticationCacheInvalidated": realtimePropagation,
	"SessionsRevoked":                realtimePropagation,
	"InvalidateAuthorization":        realtimePropagation,
	"InvalidateCurrentUserContext":   realtimePropagation,
	"PublishRealtimeEvent":           realtimePropagation,
	"UnbindExamAttemptConnection":    realtimePropagation,
	"ResolveAttemptDesktopBuild":     attemptPort,
	"ResolveNativeDeliveryBuild":     attemptPort,
	"CreateLocalUser":                "unauthorized provisioning seam that only integration tests reach; production creates Users through bootstrap, registration and Invitations",
}

const (
	principalResolution = "credential resolution establishes the Principal that an Invocation then carries"
	realtimePropagation = "post-commit realtime propagation port that the realtime child and cluster peers call after the use case that owns the Invocation"
	attemptPort         = "port of the Attempt child that the parent implements; it runs inside an Attempt use case that already holds the Invocation"
)

// TestUseCasesTakeAnInvocationFirst keeps one call shape for every use case:
// each exported method of the application surface (the method set of
// *app.App, promoted from its embedded services, and of each named area
// field such as App.Academic) takes (context.Context,
// invocation.Invocation, ...) or, for actorless Job, periodic, startup and
// host-operator work, (context.Context, invocation.System, ...).
func TestUseCasesTakeAnInvocationFirst(t *testing.T) {
	t.Parallel()
	for _, violation := range useCaseInvocationViolations(typedServerPackages(t), serverModule, nonUseCaseSurface) {
		assert.Fail(t, violation)
	}
}

func TestUseCaseInvocationRuleRejectsPlantedViolations(t *testing.T) {
	t.Parallel()
	loaded := loadFixturePackages(t, map[string]string{
		"app/invocation/invocation.go": "package invocation\n\ntype Invocation struct{}\n\ntype System struct{}\n",
		"app/app.go": `package app

import (
	"context"

	"fixture/app/invocation"
)

type service struct{}

func (*service) GetThing(context.Context, invocation.Invocation, string) error { return nil }

func (*service) SweepThings(context.Context, invocation.System, int) error { return nil }

func (*service) ListThings(context.Context, string) error { return nil }

func (*service) CountThings(invocation.Invocation) int { return 0 }

func (*service) Attach(string) error { return nil }

func (*service) helper(context.Context) {}

type area struct{}

func (*area) CreateThing(context.Context, invocation.Invocation, string) error { return nil }

func (*area) ArchiveThing(context.Context, string) error { return nil }

type App struct {
	*service
	Area *area
}
`,
	})
	require.Equal(t, []string{
		"fixture/app.App.Area.ArchiveThing takes (context.Context, string); a use case takes (context.Context, invocation.Invocation or invocation.System, ...)",
		"fixture/app.App.CountThings takes (invocation.Invocation); a use case takes (context.Context, invocation.Invocation or invocation.System, ...)",
		"fixture/app.App.ListThings takes (context.Context, string); a use case takes (context.Context, invocation.Invocation or invocation.System, ...)",
	}, useCaseInvocationViolations(loaded, "fixture", map[string]string{"Attach": "composition hook"}))
}

func useCaseInvocationViolations(loaded []*packages.Package, module string, exempt map[string]string) []string {
	var violations []string
	for _, pkg := range loaded {
		if pkg.PkgPath != module+"/app" {
			continue
		}
		application, ok := pkg.Types.Scope().Lookup("App").(*types.TypeName)
		if !ok {
			continue
		}
		violations = append(violations, surfaceInvocationViolations(pkg.PkgPath+".App", types.NewPointer(application.Type()), module, exempt)...)
		// A named area field (app.Academic) is surface too: its service's
		// exported methods are the area's use cases.
		if structure, ok := application.Type().Underlying().(*types.Struct); ok {
			for field := range structure.Fields() {
				if field.Exported() && !field.Embedded() {
					violations = append(violations, surfaceInvocationViolations(pkg.PkgPath+".App."+field.Name(), field.Type(), module, exempt)...)
				}
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func surfaceInvocationViolations(owner string, surface types.Type, module string, exempt map[string]string) []string {
	var violations []string
	methods := types.NewMethodSet(surface)
	for index := range methods.Len() {
		method := methods.At(index).Obj()
		if !method.Exported() {
			continue
		}
		if _, ok := exempt[method.Name()]; ok {
			continue
		}
		signature := method.Type().(*types.Signature)
		if takesInvocationFirst(signature, module) {
			continue
		}
		parameters := make([]string, 0, signature.Params().Len())
		for parameter := range signature.Params().Variables() {
			parameters = append(parameters, types.TypeString(parameter.Type(), func(p *types.Package) string { return p.Name() }))
		}
		violations = append(violations, fmt.Sprintf("%s.%s takes (%s); a use case takes (context.Context, invocation.Invocation or invocation.System, ...)",
			owner, method.Name(), strings.Join(parameters, ", ")))
	}
	return violations
}

func takesInvocationFirst(signature *types.Signature, module string) bool {
	parameters := signature.Params()
	if parameters.Len() < 2 || types.TypeString(parameters.At(0).Type(), nil) != "context.Context" {
		return false
	}
	named, ok := types.Unalias(parameters.At(1).Type()).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != module+"/app/invocation" {
		return false
	}
	return named.Obj().Name() == "Invocation" || named.Obj().Name() == "System"
}
