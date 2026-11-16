// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package commands is the ptool command tree, one file per subcommand.
package commands

import (
	"context"
	"io"

	"github.com/spf13/cobra"
)

// Execute runs ptool on args, writing to stdout and stderr rather than the
// process streams.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	command := NewRootCommand()
	command.SetArgs(args)
	command.SetOut(stdout)
	command.SetErr(stderr)
	return command.ExecuteContext(ctx)
}

// NewRootCommand returns a new ptool command tree; cobra commands keep parsed
// flag state, so build one per run.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "ptool",
		Short:         "Proctor repository development tools",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newErrorsCommand())
	root.AddCommand(newI18nCommand())
	root.AddCommand(newOpenAPICommand())
	root.AddCommand(newAsyncAPICommand())
	root.AddCommand(newReleaseCommand())
	root.AddCommand(newDevCommand())
	return root
}
