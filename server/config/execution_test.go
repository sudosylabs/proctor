// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testCertificateAuthorityPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "execution test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func validExecutionHost(t *testing.T, name string) ExecutionHost {
	return ExecutionHost{Name: name, Address: name + ".execution.example.test:7443",
		ServerName: name + ".execution.example.test", CAPEM: testCertificateAuthorityPEM(t), Token: "token-" + name}
}

func executionFieldErrors(execution Execution) []string {
	var fields []string
	validateExecution(execution, func(field, _ string) { fields = append(fields, field) })
	return fields
}

func TestValidateExecution(t *testing.T) {
	t.Parallel()
	require.Empty(t, executionFieldErrors(defaultExecution()), "execution is off with no hosts by default")

	enabled := defaultExecution()
	enabled.Enabled = true
	require.Equal(t, []string{"execution.hosts"}, executionFieldErrors(enabled), "enabled execution needs a host")
	enabled.Hosts = []ExecutionHost{validExecutionHost(t, "exec-1"), validExecutionHost(t, "exec-2")}
	require.Empty(t, executionFieldErrors(enabled))

	repeated := enabled
	repeated.Hosts = []ExecutionHost{validExecutionHost(t, "exec-1"), validExecutionHost(t, "exec-1")}
	require.Equal(t, []string{"execution.hosts[1].name"}, executionFieldErrors(repeated))

	broken := validExecutionHost(t, "exec-1")
	broken.Name, broken.Address, broken.ServerName, broken.CAPEM, broken.Token = "Exec", "exec-1", "", "not a certificate", ""
	invalid := enabled
	invalid.Hosts = []ExecutionHost{broken}
	require.ElementsMatch(t, []string{"execution.hosts[0].name", "execution.hosts[0].address", "execution.hosts[0].server_name",
		"execution.hosts[0].token", "execution.hosts[0].ca_pem"}, executionFieldErrors(invalid))

	owners := defaultExecution()
	owners.Owners = 0
	require.Equal(t, []string{"execution.owners"}, executionFieldErrors(owners))

	many := defaultExecution()
	for index := 0; index <= MaximumExecutionHosts; index++ {
		many.Hosts = append(many.Hosts, ExecutionHost{})
	}
	require.Contains(t, executionFieldErrors(many), "execution.hosts")
}

func TestExecutionHostsCloneDeeplyAndRedactTokens(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Execution.Hosts = []ExecutionHost{validExecutionHost(t, "exec-1")}

	cloned := cfg.Clone()
	cloned.Execution.Hosts[0].Token = "changed"
	require.Equal(t, "token-exec-1", cfg.Execution.Hosts[0].Token, "a clone does not share hosts")

	redacted := cfg.Redacted()
	require.Equal(t, "[redacted]", redacted.Execution.Hosts[0].Token)
	require.Equal(t, "token-exec-1", cfg.Execution.Hosts[0].Token, "redaction leaves the original")
	encoded, err := cfg.RedactedJSON()
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "token-exec-1", "the token is never shown")
}
