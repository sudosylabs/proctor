// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/sudosylabs/proctor/server/plugin"
)

// EvidenceStore stores Attempt evidence for the plugins. The host names
// the plugin's policy kind, "<plugin-id>.<kind>", on each call, so a plugin
// can never record under another's kind; the store answers with the plugin
// package's types and errors.
type EvidenceStore interface {
	Append(ctx context.Context, policyKind string, environment plugin.BoundEnvironment, record plugin.EvidenceRecord) (plugin.EvidenceReceipt, error)
}

// evidenceKind is a record kind within a plugin: the name half of a policy
// kind, as the integrity schema spells it.
var evidenceKind = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Evidence returns the plugin's Attempt evidence, scoped to its policy
// kinds and usable while this activation lasts. Only a plugin the core
// grants environment bindings can name a binding, so only it may append.
func (r *registrar) Evidence() plugin.Evidence {
	return pluginEvidence{
		store:      r.host.evidence,
		activation: r.activation,
		granted:    AttemptWorkspaceGranted(r.entry.manifest.ID),
	}
}

type pluginEvidence struct {
	store      EvidenceStore
	activation *activation
	granted    bool
}

var _ plugin.Evidence = pluginEvidence{}

func (e pluginEvidence) Append(
	ctx context.Context,
	environment plugin.BoundEnvironment,
	record plugin.EvidenceRecord,
) (plugin.EvidenceReceipt, error) {
	id := e.activation.entry.manifest.ID
	if !e.granted || e.store == nil || !e.activation.usable() {
		return plugin.EvidenceReceipt{}, fmt.Errorf("plugin %q evidence: %w", id, plugin.ErrUnavailable)
	}
	if !evidenceKind.MatchString(record.Kind) {
		return plugin.EvidenceReceipt{}, fmt.Errorf("plugin %q evidence kind %q: %w", id, record.Kind, plugin.ErrEvidenceInvalid)
	}
	record.Detail = slices.Clone(record.Detail)
	return e.store.Append(ctx, id+"."+record.Kind, environment, record)
}
