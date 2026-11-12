// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"net/url"
	"slices"

	"github.com/sudosylabs/proctor/server/httpapi/route"
)

// The two spellings of a list direction, shared by the `dir` and `order`
// query parameters and by every cursor that records one.
const (
	listAscending  = "asc"
	listDescending = "desc"
)

// listOrder is the order one collection read asked for: a key from the
// collection's closed set and a direction. A cursor records both, and a cursor
// presented with any other order is an invalid request rather than a position.
type listOrder struct {
	Sort string
	Dir  string
}

// Descending reports whether the order runs from the largest key down.
func (o listOrder) Descending() bool { return o.Dir == listDescending }

// continues reports whether a cursor recorded under sort and dir continues o.
func (o listOrder) continues(sort, dir string) bool { return o.Sort == sort && o.Dir == dir }

// parseListOrder reads the optional `sort` and `dir` parameters. sorts is the
// collection's closed key set and its first key the default; dir defaults to
// defaultDir. A repeated, empty, or unknown value is an invalid request.
func parseListOrder(values url.Values, sorts []string, defaultDir string) (listOrder, error) {
	order := listOrder{Sort: sorts[0], Dir: defaultDir}
	if raw, present, err := singleQueryValue(values, "sort"); err != nil {
		return order, err
	} else if present {
		if !slices.Contains(sorts, raw) {
			return order, route.InvalidRequest(errors.New("sort is not one of the listed keys"))
		}
		order.Sort = raw
	}

	dir, err := parseListDirection(values, "dir", defaultDir)
	order.Dir = dir
	return order, err
}

// parseListDirection reads one optional direction parameter, `dir` or
// `order`, defaulting to defaultDir.
func parseListDirection(values url.Values, name, defaultDir string) (string, error) {
	raw, present, err := singleQueryValue(values, name)
	if err != nil || !present {
		return defaultDir, err
	}
	if raw != listAscending && raw != listDescending {
		return defaultDir, route.InvalidRequest(errors.New(name + " must be asc or desc"))
	}
	return raw, nil
}

// validListDirection reports whether a decoded cursor names a direction.
func validListDirection(dir string) bool { return dir == listAscending || dir == listDescending }

func singleQueryValue(values url.Values, name string) (string, bool, error) {
	entries, present := values[name]
	if !present {
		return "", false, nil
	}
	if len(entries) != 1 || entries[0] == "" {
		return "", true, route.InvalidRequest(errors.New(name + " must be one non-empty value"))
	}
	return entries[0], true, nil
}
