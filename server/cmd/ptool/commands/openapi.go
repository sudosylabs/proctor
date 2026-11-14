// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package commands

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/sudosylabs/proctor/server/internal/openapidoc"
)

type openAPIOptions struct {
	source  string
	plugins string
	output  string
}

func newOpenAPICommand() *cobra.Command {
	options := &openAPIOptions{}
	command := &cobra.Command{
		Use:   "openapi",
		Short: "Build and validate the public OpenAPI contract",
	}
	command.PersistentFlags().StringVar(&options.source, "source", "openapi", "human-authored OpenAPI source directory")
	command.PersistentFlags().StringVar(&options.plugins, "plugins", "plugin",
		"directory of compiled plugins; each <id>/openapi/ folder adds that plugin's fragments")
	command.PersistentFlags().StringVar(&options.output, "output", "openapi.json", "generated OpenAPI JSON artifact")
	command.AddCommand(newOpenAPIBuildCommand(options), newOpenAPICheckCommand(options))
	return command
}

func newOpenAPIBuildCommand(options *openAPIOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Compile YAML sources into the reviewed JSON artifact",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			compiled, err := compileOpenAPI(options.source, options.plugins)
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

func newOpenAPICheckCommand(options *openAPIOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Validate sources and fail when the JSON artifact is stale",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			compiled, err := compileOpenAPI(options.source, options.plugins)
			if err != nil {
				return err
			}
			output := filepath.Clean(options.output)
			current, err := os.ReadFile(output)
			if err != nil {
				return fmt.Errorf("read generated OpenAPI artifact %s: %w", output, err)
			}
			if !bytes.Equal(current, compiled) {
				return fmt.Errorf("generated OpenAPI artifact %s is stale; run `go run ./cmd/ptool openapi build`", output)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "validated %s against %s\n", options.output, options.source)
			return err
		},
	}
}

func compileOpenAPI(source, plugins string) ([]byte, error) {
	source = filepath.Clean(source)
	pluginSources, err := pluginOpenAPISources(plugins)
	if err != nil {
		return nil, err
	}
	compiled, err := openapidoc.Compile(os.DirFS(source), pluginSources...)
	if err != nil {
		return nil, fmt.Errorf("compile OpenAPI source %s: %w", source, err)
	}
	return compiled, nil
}

// pluginOpenAPISources finds the OpenAPI fragments the compiled plugins
// ship: plugins/<id>/openapi/ for each plugin folder that has one. A plugin
// that serves routes is in a folder named after its ID.
func pluginOpenAPISources(plugins string) ([]openapidoc.PluginSource, error) {
	if plugins == "" {
		return nil, nil
	}
	plugins = filepath.Clean(plugins)
	entries, err := os.ReadDir(plugins)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plugin directory %s: %w", plugins, err)
	}
	var sources []openapidoc.PluginSource
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fragments := filepath.Join(plugins, entry.Name(), "openapi")
		info, statErr := os.Stat(fragments)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("inspect plugin OpenAPI fragments %s: %w", fragments, statErr)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("plugin OpenAPI fragments %s must be a directory", fragments)
		}
		sources = append(sources, openapidoc.PluginSource{ID: entry.Name(), Files: os.DirFS(fragments)})
	}
	return sources, nil
}

func writeFileAtomically(name string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(name); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect artifact %s: %w", name, err)
	}

	temporary, err := os.CreateTemp(filepath.Dir(name), ".ptool-artifact-*")
	if err != nil {
		return fmt.Errorf("create temporary artifact: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, name); err != nil {
		return fmt.Errorf("replace artifact %s: %w", name, err)
	}
	return nil
}
