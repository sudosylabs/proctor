// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// DecodeStrictJobDocument decodes one Job command, checkpoint or result
// into target. The document must be a single JSON object with no duplicate
// member, no member target does not declare, and nothing after it.
func DecodeStrictJobDocument(document json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("Job document must be an object")
	}

	seen := make(map[string]struct{})
	for decoder.More() {
		member, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}

		name, ok := member.(string)
		if !ok {
			return errors.New("Job document member is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return errors.New("Job document contains a duplicate member")
		}

		seen[name] = struct{}{}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return err
		}
	}

	if _, err = decoder.Token(); err != nil {
		return err
	}

	strict := json.NewDecoder(bytes.NewReader(document))
	strict.DisallowUnknownFields()
	if err = strict.Decode(target); err != nil {
		return err
	}

	var trailing any
	if err = strict.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("Job document contains trailing JSON")
		}
		return err
	}

	return nil
}
