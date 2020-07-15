package api

import (
	"encoding/base64"
	"testing"

	log "github.com/hashicorp/go-hclog"
	"github.com/quid/vault/api"
	"github.com/quid/vault/audit"
	auditFile "github.com/quid/vault/builtin/audit/file"
	credUserpass "github.com/quid/vault/builtin/credential/userpass"
	"github.com/quid/vault/builtin/logical/database"
	"github.com/quid/vault/builtin/logical/pki"
	"github.com/quid/vault/builtin/logical/transit"
	"github.com/quid/vault/helper/builtinplugins"
	"github.com/quid/vault/http"
	"github.com/quid/vault/sdk/logical"
	"github.com/quid/vault/vault"
)

// testVaultServer creates a test vault cluster and returns a configured API
// client and closer function.
func testVaultServer(t testing.TB) (*api.Client, func()) {
	t.Helper()

	client, _, closer := testVaultServerUnseal(t)
	return client, closer
}

// testVaultServerUnseal creates a test vault cluster and returns a configured
// API client, list of unseal keys (as strings), and a closer function.
func testVaultServerUnseal(t testing.TB) (*api.Client, []string, func()) {
	t.Helper()

	return testVaultServerCoreConfig(t, &vault.CoreConfig{
		DisableMlock: true,
		DisableCache: true,
		Logger:       log.NewNullLogger(),
		CredentialBackends: map[string]logical.Factory{
			"userpass": credUserpass.Factory,
		},
		AuditBackends: map[string]audit.Factory{
			"file": auditFile.Factory,
		},
		LogicalBackends: map[string]logical.Factory{
			"database":       database.Factory,
			"generic-leased": vault.LeasedPassthroughBackendFactory,
			"pki":            pki.Factory,
			"transit":        transit.Factory,
		},
		BuiltinRegistry: builtinplugins.Registry,
	})
}

// testVaultServerCoreConfig creates a new vault cluster with the given core
// configuration. This is a lower-level test helper.
func testVaultServerCoreConfig(t testing.TB, coreConfig *vault.CoreConfig) (*api.Client, []string, func()) {
	t.Helper()

	cluster := vault.NewTestCluster(t, coreConfig, &vault.TestClusterOptions{
		HandlerFunc: http.Handler,
	})
	cluster.Start()

	// Make it easy to get access to the active
	core := cluster.Cores[0].Core
	vault.TestWaitActive(t, core)

	// Get the client already setup for us!
	client := cluster.Cores[0].Client
	client.SetToken(cluster.RootToken)

	// Convert the unseal keys to base64 encoded, since these are how the user
	// will get them.
	unsealKeys := make([]string, len(cluster.BarrierKeys))
	for i := range unsealKeys {
		unsealKeys[i] = base64.StdEncoding.EncodeToString(cluster.BarrierKeys[i])
	}

	return client, unsealKeys, func() { defer cluster.Cleanup() }
}
