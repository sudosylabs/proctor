// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type bootstrapStatusStoreFake struct {
	store.InstallationStore
	state *model.InstallationState
	err   error
}

func (s *bootstrapStatusStoreFake) Get(context.Context) (*model.InstallationState, error) {
	return s.state, s.err
}

func TestResolveBootstrapProtectionRequiresConfiguredSecretForPristineProduction(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Authentication.Bootstrap.DevelopmentMode = false
	cfg.Server.ListenAddress = "0.0.0.0:8065"
	cfg.Server.PublicURL = "https://proctor.example.edu"
	_, err := resolveFromConfig(context.Background(), cfg, &bootstrapStatusStoreFake{
		err: store.NewErrNotFound("installation", "singleton"),
	}, &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "bootstrap secret is required")
}

func TestResolveBootstrapProtectionGeneratesAndDisplaysLoopbackDevelopmentSecretOnce(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	policy, err := resolveFromConfig(context.Background(), config.Default(), &bootstrapStatusStoreFake{
		err: store.NewErrNotFound("installation", "singleton"),
	}, &output)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(policy.Secret), 43, "policy/output = %#v", policy)
	require.Contains(t, output.String(), policy.Secret, "policy/output = %#v", policy)
	require.Equal(t, 1, strings.Count(output.String(), policy.Secret), "policy/output = %#v", policy)
}

func TestResolveBootstrapProtectionDoesNotRequireReplacementSecretAfterInitialization(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Authentication.Bootstrap.DevelopmentMode = false
	policy, err := resolveFromConfig(context.Background(), cfg, &bootstrapStatusStoreFake{
		state: &model.InstallationState{InitializedAt: model.NowUTC(), InstitutionID: model.NewInstitutionID(), AdministratorUserID: model.NewUserID()},
	}, &bytes.Buffer{})
	require.NoError(t, err, "policy=%#v error", policy)
	require.Empty(t, policy.Secret, "policy=%#v error", policy)
}

func TestResolveBootstrapProtectionDoesNotGenerateDevelopmentSecretAfterInitialization(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	policy, err := resolveFromConfig(context.Background(), config.Default(), &bootstrapStatusStoreFake{
		state: &model.InstallationState{InitializedAt: model.NowUTC(), InstitutionID: model.NewInstitutionID(), AdministratorUserID: model.NewUserID()},
	}, &output)
	require.NoError(t, err, "policy=%#v output=%q error", policy, output.String())
	require.Empty(t, policy.Secret, "policy=%#v output=%q error", policy, output.String())
	require.Equal(t, 0, output.Len(), "policy=%#v output=%q error", policy, output.String())
}

func TestResolveBootstrapProtectionWithoutInstallationStateUsesConfiguredSecret(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Authentication.Bootstrap.Secret = "operator-provided-bootstrap-secret-32-bytes"
	var output bytes.Buffer
	policy, err := resolveFromConfig(context.Background(), cfg, nil, &output)
	require.NoError(t, err, "ResolveBootstrapProtection(nil) = %#v, %v, output %q", policy, err, output.String())
	require.Equal(t, cfg.Authentication.Bootstrap.Secret, policy.Secret, "ResolveBootstrapProtection(nil) = %#v, %v, output %q", policy, err, output.String())
	require.Equal(t, 0, output.Len(), "ResolveBootstrapProtection(nil) = %#v, %v, output %q", policy, err, output.String())
	cfg.Authentication.Bootstrap.Secret = ""
	policy, err = resolveFromConfig(context.Background(), cfg, nil, &output)
	require.NoError(t, err, "ResolveBootstrapProtection(nil, no secret) = %#v, %v, output %q", policy, err, output.String())
	require.Empty(t, policy.Secret, "ResolveBootstrapProtection(nil, no secret) = %#v, %v, output %q", policy, err, output.String())
	require.Equal(t, 0, output.Len(), "ResolveBootstrapProtection(nil, no secret) = %#v, %v, output %q", policy, err, output.String())
}

// resolveFromConfig resolves the policy from cfg's bootstrap settings, as
// composition does.
func resolveFromConfig(ctx context.Context, cfg config.Config, installations store.InstallationStore, output io.Writer) (BootstrapProtectionPolicy, error) {
	return ResolveBootstrapProtection(ctx, cfg.Authentication.Bootstrap.Secret, cfg.Authentication.Bootstrap.DevelopmentMode,
		installations, output)
}
