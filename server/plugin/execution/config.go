// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/plugin"
)

// IgnoreFile is the Attempt Workspace file that names the paths the
// environment keeps to itself. execenv knows it only because the plugin
// names it when it initializes a projection.
const IgnoreFile = ".proctorignore"

// DefaultIgnorePatterns apply while the Attempt Workspace has no
// IgnoreFile, or one that does not parse. They play the part of a standard
// .gitignore: dependency installs and build output stay on the grant.
var DefaultIgnorePatterns = []string{
	"node_modules/",
	".venv/",
	"venv/",
	"__pycache__/",
	"*.pyc",
	".pytest_cache/",
	".mypy_cache/",
	"target/",
	"build/",
	"dist/",
	"out/",
	".gradle/",
	"*.o",
	"*.obj",
	"*.a",
	"*.so",
	"*.class",
	"*.jar",
	".cache/",
}

// Settings is the plugin's settings document, which an administrator edits.
type Settings struct {
	// HarvestQuietMS is how long the guest must stay quiet before its
	// changes are harvested.
	HarvestQuietMS int64 `json:"harvest_quiet_ms"`
	// HarvestMaxMS is the longest a guest change waits to be harvested.
	HarvestMaxMS int64 `json:"harvest_max_ms"`
}

const (
	defaultHarvestQuietMS = 2_000
	defaultHarvestMaxMS   = 10_000
	minHarvestQuietMS     = 250
	maxHarvestQuietMS     = 10_000
	minHarvestMaxMS       = 1_000
	maxHarvestMaxMS       = 60_000
)

func defaultSettings() Settings {
	return Settings{HarvestQuietMS: defaultHarvestQuietMS, HarvestMaxMS: defaultHarvestMaxMS}
}

func (s Settings) quiet() time.Duration { return time.Duration(s.HarvestQuietMS) * time.Millisecond }
func (s Settings) max() time.Duration   { return time.Duration(s.HarvestMaxMS) * time.Millisecond }

// errInvalidSettings refuses a settings or secrets document.
var errInvalidSettings = errors.New("execution plugin settings are invalid")

// parseSettings decodes and bounds a settings document; nil is the defaults.
func parseSettings(document json.RawMessage) (Settings, error) {
	settings := defaultSettings()
	if plugin.PresentDocument(document) == nil {
		return settings, nil
	}
	if err := decodeStrict(document, &settings); err != nil {
		return Settings{}, fmt.Errorf("%w: %w", errInvalidSettings, err)
	}
	if settings.HarvestQuietMS < minHarvestQuietMS || settings.HarvestQuietMS > maxHarvestQuietMS ||
		settings.HarvestMaxMS < minHarvestMaxMS || settings.HarvestMaxMS > maxHarvestMaxMS ||
		settings.HarvestMaxMS < settings.HarvestQuietMS {
		return Settings{}, fmt.Errorf("%w: harvest windows out of bounds", errInvalidSettings)
	}
	return settings, nil
}

// settingsSchema is the settings form.
var settingsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "harvest_quiet_ms": {
      "type": "integer", "minimum": 250, "maximum": 10000, "default": 2000,
      "title": "Quiet window",
      "description": "Milliseconds a terminal must stop changing files before they are saved to the Attempt Workspace."
    },
    "harvest_max_ms": {
      "type": "integer", "minimum": 1000, "maximum": 60000, "default": 10000,
      "title": "Longest wait",
      "description": "The longest a file changed in the terminal waits before it is saved, at least the quiet window."
    }
  }
}`)

// Secrets is the plugin's secrets document from the configuration file.
type Secrets struct {
	Hosts []HostConfig `json:"hosts"`
}

// HostConfig is one execenv host Proctor dials.
type HostConfig struct {
	// Name identifies the host in grant records and logs.
	Name string `json:"name"`
	// Address is the host's host:port.
	Address string `json:"address"`
	// ServerName is the name its TLS certificate is checked against.
	ServerName string `json:"server_name"`
	// CAPEM is the certificate authority that issued its certificate.
	CAPEM string `json:"ca_pem"`
	// Token authenticates Proctor to the host. It is never logged.
	Token string `json:"token"`
}

const maxHosts = 64

var hostNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// parseSecrets decodes and checks the secrets document. Error text never
// includes a secret.
func parseSecrets(document json.RawMessage) (Secrets, error) {
	var secrets Secrets
	if plugin.PresentDocument(document) == nil {
		return secrets, nil
	}
	if err := decodeStrict(document, &secrets); err != nil {
		return Secrets{}, fmt.Errorf("%w: secrets do not decode", errInvalidSettings)
	}
	if len(secrets.Hosts) > maxHosts {
		return Secrets{}, fmt.Errorf("%w: more than %d hosts", errInvalidSettings, maxHosts)
	}
	seen := make(map[string]struct{}, len(secrets.Hosts))
	for index, host := range secrets.Hosts {
		if !hostNamePattern.MatchString(host.Name) {
			return Secrets{}, fmt.Errorf("%w: host %d has an invalid name", errInvalidSettings, index)
		}
		if _, ok := seen[host.Name]; ok {
			return Secrets{}, fmt.Errorf("%w: host %q is listed twice", errInvalidSettings, host.Name)
		}
		seen[host.Name] = struct{}{}
		if host.Address == "" || host.ServerName == "" || host.Token == "" {
			return Secrets{}, fmt.Errorf("%w: host %q needs an address, a server name and a token", errInvalidSettings, host.Name)
		}
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(host.CAPEM)) {
			return Secrets{}, fmt.Errorf("%w: host %q has no usable certificate authority", errInvalidSettings, host.Name)
		}
	}
	return secrets, nil
}

// ignoreRules are the rules every projection is initialized with.
func ignoreRules() *execenv.IgnoreRules {
	return &execenv.IgnoreRules{File: IgnoreFile, Fallback: append([]string(nil), DefaultIgnorePatterns...)}
}

func decodeStrict(document []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data")
	}
	return nil
}
