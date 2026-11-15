// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"crypto/x509"
	"fmt"
	"net"
	"regexp"
	"strconv"
)

// MaximumExecutionHosts bounds the execution hosts one installation dials.
const MaximumExecutionHosts = 64

var executionHostNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Execution configures code execution: whether Exams may give candidates an
// execution environment, the execution hosts Proctor dials, and how many
// environments one node owns at once. The hosts and their tokens are
// deployment secrets; the environment profiles an Exam names are managed by
// administrators in the application. Changes take effect on restart.
type Execution struct {
	// Enabled lets Exams publish with execution. While it is off, no
	// environment is created and publishing an Exam that asks for one is
	// refused.
	Enabled bool `json:"Enabled"`
	// Hosts are the execution hosts environments are placed on.
	Hosts []ExecutionHost `json:"Hosts"`
	// Owners bounds how many environments this node owns at once; another
	// node takes the rest.
	Owners int `json:"Owners"`
}

// ExecutionHost is one execution host Proctor dials over TLS.
type ExecutionHost struct {
	// Name identifies the host in environment records and logs.
	Name string `json:"Name"`
	// Address is the host's host:port.
	Address string `json:"Address"`
	// ServerName is the name its TLS certificate is checked against.
	ServerName string `json:"ServerName"`
	// CAPEM is the PEM certificate authority that issued that certificate.
	CAPEM string `json:"CAPEM"`
	// Token authenticates Proctor to the host. It is never logged or shown.
	Token string `json:"Token"`
}

func defaultExecution() Execution {
	return Execution{Hosts: []ExecutionHost{}, Owners: 512}
}

func validateExecution(execution Execution, add func(string, string)) {
	if execution.Owners < 1 || execution.Owners > 100_000 {
		add("execution.owners", "must be from 1 to 100000")
	}
	if len(execution.Hosts) > MaximumExecutionHosts {
		add("execution.hosts", fmt.Sprintf("must list at most %d hosts", MaximumExecutionHosts))
	}
	if execution.Enabled && len(execution.Hosts) == 0 {
		add("execution.hosts", "must list at least one host when execution is enabled")
	}
	seen := make(map[string]struct{}, len(execution.Hosts))
	for index, host := range execution.Hosts {
		field := "execution.hosts[" + strconv.Itoa(index) + "]"
		if !executionHostNamePattern.MatchString(host.Name) {
			add(field+".name", "must be a lowercase letter, then up to 62 lowercase letters, digits or hyphens")
		} else if _, repeated := seen[host.Name]; repeated {
			add(field+".name", "must be unique")
		}
		seen[host.Name] = struct{}{}
		if _, port, err := net.SplitHostPort(host.Address); err != nil || port == "" {
			add(field+".address", "must be host:port")
		}
		if host.ServerName == "" {
			add(field+".server_name", "is required")
		}
		if host.Token == "" {
			add(field+".token", "is required")
		}
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(host.CAPEM)) {
			add(field+".ca_pem", "must hold at least one PEM certificate")
		}
	}
}

func redactExecution(execution *Execution) {
	for index := range execution.Hosts {
		execution.Hosts[index].Token = redactSecret(execution.Hosts[index].Token)
	}
}
