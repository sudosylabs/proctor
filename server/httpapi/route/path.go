// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sudosylabs/proctor/server/model"
)

// The route-path vocabulary is deliberately private. Resource modules write
// a path template in endpoint.Path; the kernel parses it into these values,
// and gorilla/mux syntax remains an implementation detail of the catalog
// compiler.

type parameterKind uint8

const (
	parameterCanonicalID parameterKind = iota + 1
	parameterProviderID
	parameterPositiveSequence
	parameterCanonicalUUID
)

type pathPart interface {
	compile() (template string, normalized string, err error)
}

type pathLiteral string

func literal(value string) pathPart { return pathLiteral(value) }

func (p pathLiteral) compile() (string, string, error) {
	value := string(p)
	if !regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`).MatchString(value) {
		return "", "", fmt.Errorf("path literal %q is not canonical", value)
	}
	return value, value, nil
}

type pathParameter struct {
	name string
	kind parameterKind
}

func canonicalID(name string) pathPart {
	return pathParameter{name: name, kind: parameterCanonicalID}
}

func canonicalUUID(name string) pathPart {
	return pathParameter{name: name, kind: parameterCanonicalUUID}
}

func positiveSequence(name string) pathPart {
	return pathParameter{name: name, kind: parameterPositiveSequence}
}

func providerID(name string) pathPart {
	return pathParameter{name: name, kind: parameterProviderID}
}

func (p pathParameter) compile() (string, string, error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(p.name) {
		return "", "", fmt.Errorf("path parameter name %q is not canonical", p.name)
	}
	var pattern, normalized string
	switch p.kind {
	case parameterCanonicalID:
		pattern = canonicalIDRoutePattern()
		normalized = "canonical_id"
	case parameterCanonicalUUID:
		pattern = "[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}"
		normalized = "canonical_uuid"
	case parameterPositiveSequence:
		pattern = "[1-9][0-9]{0,15}"
		normalized = "positive_sequence"
	case parameterProviderID:
		pattern = providerIDRoutePattern()
		normalized = "provider_id"
	default:
		return "", "", fmt.Errorf("path parameter %q has unsupported parameter kind %d", p.name, p.kind)
	}
	return "{" + p.name + ":" + pattern + "}", "{" + normalized + "}", nil
}

// routePath is a canonical route path. The kernel parses it from an
// endpoint's Path, a readable template such as
// "/programme-levels/{programme_level_id}/classes".
type routePath struct {
	parts       []pathPart
	rootMounted bool
	// err records a malformed template. The catalog compiler reports it when
	// the route is compiled, so a bad template fails New rather than package
	// initialisation.
	err error
}

// path parses a route template relative to the API root. The template starts
// with "/" and has no trailing slash. A plain segment is a canonical literal;
// "{name}" is a canonical identifier parameter; "{name:uuid}",
// "{name:sequence}" and "{name:provider}" select a canonical UUID, a positive
// sequence number and a provider identifier. Malformed templates are reported
// by the catalog compiler.
func path(template string) routePath {
	parts, err := parsePathTemplate(template)
	return routePath{parts: parts, err: err}
}

// rootPath parses a route template like path but mounts it at the server root
// instead of under the API prefix.
func rootPath(template string) routePath {
	parts, err := parsePathTemplate(template)
	return routePath{parts: parts, err: err, rootMounted: true}
}

func parsePathTemplate(template string) ([]pathPart, error) {
	if !strings.HasPrefix(template, "/") {
		return nil, fmt.Errorf("route template %q must start with /", template)
	}

	segments := strings.Split(template[1:], "/")
	parts := make([]pathPart, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			return nil, fmt.Errorf("route template %q has an empty segment", template)
		}

		if !strings.ContainsAny(segment, "{}") {
			parts = append(parts, literal(segment))
			continue
		}

		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			return nil, fmt.Errorf("route template %q has a malformed parameter %q", template, segment)
		}

		name, kind, hasKind := strings.Cut(segment[1:len(segment)-1], ":")
		if strings.ContainsAny(name, "{}:") || strings.ContainsAny(kind, "{}:") {
			return nil, fmt.Errorf("route template %q has a malformed parameter %q", template, segment)
		}

		switch {
		case !hasKind:
			parts = append(parts, canonicalID(name))
		case kind == "uuid":
			parts = append(parts, canonicalUUID(name))
		case kind == "sequence":
			parts = append(parts, positiveSequence(name))
		case kind == "provider":
			parts = append(parts, providerID(name))
		default:
			return nil, fmt.Errorf("route template %q has unknown parameter kind %q", template, kind)
		}
	}

	return parts, nil
}

func (p routePath) compile(apiPrefix string) (string, string, error) {
	if p.err != nil {
		return "", "", p.err
	}
	if apiPrefix == "" || !strings.HasPrefix(apiPrefix, "/") || strings.HasSuffix(apiPrefix, "/") {
		return "", "", fmt.Errorf("API prefix %q is not canonical", apiPrefix)
	}
	if len(p.parts) == 0 {
		return "", "", errors.New("route path is empty")
	}
	template := make([]string, 0, len(p.parts))
	normalized := make([]string, 0, len(p.parts))
	for _, part := range p.parts {
		if part == nil {
			return "", "", errors.New("route path contains a nil part")
		}
		compiled, shape, err := part.compile()
		if err != nil {
			return "", "", err
		}
		template = append(template, compiled)
		normalized = append(normalized, shape)
	}
	prefix := apiPrefix
	if p.rootMounted {
		prefix = ""
	}
	return prefix + "/" + strings.Join(template, "/"),
		prefix + "/" + strings.Join(normalized, "/"), nil
}

func canonicalIDRoutePattern() string {
	return "[" + model.IDAlphabet + "]{" + strconv.Itoa(model.IDLength) + "}"
}

func providerIDRoutePattern() string {
	return "[a-z0-9][a-z0-9._-]{0,63}"
}
