// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/sudosylabs/proctor/server/internal/asyncapidoc"
)

type asyncAPIOptions struct {
	root   string
	source string
	output string
}

func newAsyncAPICommand() *cobra.Command {
	options := &asyncAPIOptions{}
	command := &cobra.Command{
		Use:   "asyncapi",
		Short: "Build and validate the realtime AsyncAPI contract",
	}
	command.PersistentFlags().StringVar(&options.root, "root", ".", "server module directory holding the source and "+asyncapidoc.SchemaDirectory)
	command.PersistentFlags().StringVar(&options.source, "source", "asyncapi", "human-authored AsyncAPI source directory, relative to --root")
	command.PersistentFlags().StringVar(&options.output, "output", "asyncapi.json", "generated AsyncAPI JSON artifact")
	command.AddCommand(newAsyncAPIBuildCommand(options), newAsyncAPICheckCommand(options))
	return command
}

func newAsyncAPIBuildCommand(options *asyncAPIOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Compile YAML sources and shared schemas into the reviewed JSON artifact",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			compiled, err := compileAsyncAPI(options)
			if err != nil {
				return err
			}
			if writeErr := writeFileAtomically(filepath.Clean(options.output), compiled); writeErr != nil {
				return writeErr
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "wrote %s from %s\n", options.output, options.source)
			return err
		},
	}
}

func newAsyncAPICheckCommand(options *asyncAPIOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Validate sources and examples, and fail when the JSON artifact is stale",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			compiled, err := compileAsyncAPI(options)
			if err != nil {
				return err
			}
			output := filepath.Clean(options.output)
			current, err := os.ReadFile(output)
			if err != nil {
				return fmt.Errorf("read generated AsyncAPI artifact %s: %w", output, err)
			}
			if !bytes.Equal(current, compiled) {
				return fmt.Errorf("generated AsyncAPI artifact %s is stale; run `go run ./cmd/ptool asyncapi build`", output)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "validated %s against %s\n", options.output, options.source)
			return err
		},
	}
}

func compileAsyncAPI(options *asyncAPIOptions) ([]byte, error) {
	root := filepath.Clean(options.root)
	compiled, err := asyncapidoc.Compile(os.DirFS(root), filepath.ToSlash(options.source))
	if err != nil {
		return nil, fmt.Errorf("compile AsyncAPI source %s: %w", filepath.Join(root, options.source), err)
	}
	return compiled, nil
}
