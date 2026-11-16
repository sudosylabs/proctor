// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type retryMode string

const (
	// retryAlways retries every transient failure.
	retryAlways retryMode = "always"
	// retryWithCommand retries only when the call carries a
	// *store.CommandIdempotency, whose durable outcome makes a replay after
	// an unknown commit safe. Without one the call runs once.
	retryWithCommand retryMode = "command"
)

const commandType = "*store.CommandIdempotency"

type retryEntry struct {
	contract string
	method   string
	mode     retryMode
	line     int
}

type retryMethods struct {
	imports []string
	source  bytes.Buffer
}

func renderRetryLayer(interfaces []interfaceSpec, source, allowlistPath string) ([]byte, error) {
	if allowlistPath == "" {
		return nil, fmt.Errorf("the retry layer needs -allowlist")
	}
	entries, err := parseAllowlist(allowlistPath)
	if err != nil {
		return nil, err
	}
	known, err := sourceImports(source)
	if err != nil {
		return nil, err
	}
	retried, err := renderRetryMethods(interfaces, entries, known)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", allowlistPath, err)
	}
	return renderEmbedded(interfaces, "retrylayer", "retry", retried)
}

// parseAllowlist reads lines of the form "Contract.Method mode". A # starts a
// comment that runs to the end of the line.
func parseAllowlist(allowlistPath string) ([]retryEntry, error) {
	content, err := os.ReadFile(allowlistPath)
	if err != nil {
		return nil, fmt.Errorf("read retry allowlist: %w", err)
	}
	var entries []retryEntry
	seen := make(map[string]int)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for line := 1; scanner.Scan(); line++ {
		text, _, _ := strings.Cut(scanner.Text(), "#")
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want \"Contract.Method mode\"", allowlistPath, line)
		}
		contract, method, ok := strings.Cut(fields[0], ".")
		if !ok || contract == "" || method == "" || strings.Contains(method, ".") {
			return nil, fmt.Errorf("%s:%d: %q is not Contract.Method", allowlistPath, line, fields[0])
		}
		mode := retryMode(fields[1])
		if mode != retryAlways && mode != retryWithCommand {
			return nil, fmt.Errorf("%s:%d: mode %q is neither %q nor %q", allowlistPath, line, mode, retryAlways, retryWithCommand)
		}
		if previous, ok := seen[fields[0]]; ok {
			return nil, fmt.Errorf("%s:%d: %s is already listed on line %d", allowlistPath, line, fields[0], previous)
		}
		seen[fields[0]] = line
		entries = append(entries, retryEntry{contract: contract, method: method, mode: mode, line: line})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read retry allowlist: %w", err)
	}
	return entries, nil
}

// renderRetryMethods writes one retrying override per allowlist entry, in
// contract order so the output does not depend on how the allowlist is
// arranged. An entry that names no contract method fails generation: a stale
// entry must not silently stop retrying.
func renderRetryMethods(interfaces []interfaceSpec, entries []retryEntry, known map[string]string) (*retryMethods, error) {
	wanted := make(map[string]retryEntry, len(entries))
	for _, entry := range entries {
		wanted[entry.contract+"."+entry.method] = entry
	}
	retried := &retryMethods{}
	qualifiers := map[string]bool{"context": true, "store": true, "sync": true}
	for _, spec := range interfaces {
		for _, method := range spec.methods {
			key := spec.name + "." + method.name
			entry, ok := wanted[key]
			if !ok {
				continue
			}
			delete(wanted, key)
			if err := renderRetryMethod(&retried.source, spec.name, method, entry.mode); err != nil {
				return nil, fmt.Errorf("line %d: %w", entry.line, err)
			}
			for _, value := range slices.Concat(method.params, method.results) {
				for _, match := range qualifierPattern.FindAllStringSubmatch(value.typeName, -1) {
					qualifiers[match[1]] = true
				}
			}
		}
	}
	if len(wanted) > 0 {
		stale := make([]retryEntry, 0, len(wanted))
		for _, entry := range wanted {
			stale = append(stale, entry)
		}
		slices.SortFunc(stale, func(left, right retryEntry) int { return left.line - right.line })
		first := stale[0]
		return nil, fmt.Errorf("line %d: %s.%s is not a store contract method", first.line, first.contract, first.method)
	}
	for qualifier := range qualifiers {
		importPath, ok := known[qualifier]
		if !ok {
			return nil, fmt.Errorf("no import for package %q used by a retried method", qualifier)
		}
		retried.imports = append(retried.imports, importPath)
	}
	slices.Sort(retried.imports)
	return retried, nil
}

var qualifierPattern = regexp.MustCompile(`\b([a-z][A-Za-z0-9_]*)\.`)

func renderRetryMethod(output *bytes.Buffer, contract string, method methodSpec, mode retryMode) error {
	key := contract + "." + method.name
	valueCount := len(method.results) - 1
	if valueCount < 0 || method.results[valueCount].typeName != "error" {
		return fmt.Errorf("%s does not return error last", key)
	}
	if valueCount > 2 {
		return fmt.Errorf("%s returns %d values before error; retryCall handles at most 2", key, valueCount)
	}
	if len(method.params) == 0 || method.params[0].typeName != "context.Context" {
		return fmt.Errorf("%s does not take a context first", key)
	}
	params := slices.Clone(method.params)
	params[0].name = "ctx"
	commands := 0
	for index := range params {
		if params[index].typeName == commandType {
			params[index].name = "command"
			commands++
		}
	}
	if mode == retryWithCommand && commands != 1 {
		return fmt.Errorf("%s is listed as %q but takes %d %s arguments", key, mode, commands, commandType)
	}
	receiver, receiverType, layer := "s", lowerFirst(strings.TrimSuffix(contract, "Store"))+"Store", "s.layer"
	if contract == "Store" {
		receiver, receiverType, layer = "l", "Layer", "l"
	}
	names := map[string]bool{receiver: true}
	for _, param := range params {
		if names[param.name] {
			return fmt.Errorf("%s parameter %q clashes with the generated wrapper", key, param.name)
		}
		names[param.name] = true
	}

	forward := fmt.Sprintf("%s.%s.%s(%s)", receiver, contract, method.name, arguments(params))
	fmt.Fprintf(output, "func (%s *%s) %s(%s)%s {\n", receiver, receiverType, method.name, parameters(params), results(method.results))
	if mode == retryWithCommand {
		fmt.Fprintf(output, "\tif command == nil {\n\t\treturn %s\n\t}\n", forward)
	}
	fmt.Fprintf(output, "\treturn retryCall%d(ctx, %s, func()%s {\n", valueCount, layer, results(method.results))
	fmt.Fprintf(output, "\t\treturn %s\n", forward)
	output.WriteString("\t})\n}\n\n")
	return nil
}

// sourceImports maps each package name the store contracts import to its
// path, so generated signatures can name the same types. Package store
// itself and sync are added for the wrappers.
func sourceImports(source string) (map[string]string, error) {
	paths, err := sourceFiles(source)
	if err != nil {
		return nil, err
	}
	known := map[string]string{
		"store": "github.com/sudosylabs/proctor/server/store",
		"sync":  "sync",
	}
	for _, sourcePath := range paths {
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), sourcePath, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", sourcePath, parseErr)
		}
		for _, spec := range parsed.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return nil, fmt.Errorf("parse %s imports: %w", sourcePath, unquoteErr)
			}
			name := path.Base(importPath)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			known[name] = importPath
		}
	}
	return known, nil
}

// writeImports puts standard library packages first, as goimports would.
func writeImports(output *bytes.Buffer, imports []string) {
	var standard, module []string
	for _, importPath := range imports {
		first, _, _ := strings.Cut(importPath, "/")
		if strings.Contains(first, ".") {
			module = append(module, importPath)
		} else {
			standard = append(standard, importPath)
		}
	}
	output.WriteString("import (\n")
	for _, importPath := range standard {
		fmt.Fprintf(output, "\t%q\n", importPath)
	}
	if len(standard) > 0 && len(module) > 0 {
		output.WriteString("\n")
	}
	for _, importPath := range module {
		fmt.Fprintf(output, "\t%q\n", importPath)
	}
	output.WriteString(")\n\n")
}
