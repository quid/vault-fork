package plugin_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	log "github.com/hashicorp/go-hclog"
	"github.com/quid/vault/api"
	"github.com/quid/vault/builtin/plugin"
	vaulthttp "github.com/quid/vault/http"
	"github.com/quid/vault/sdk/helper/consts"
	"github.com/quid/vault/sdk/helper/logging"
	"github.com/quid/vault/sdk/helper/pluginutil"
	"github.com/quid/vault/sdk/logical"
	logicalPlugin "github.com/quid/vault/sdk/plugin"
	"github.com/quid/vault/sdk/plugin/mock"
	"github.com/quid/vault/vault"
)

func TestBackend_impl(t *testing.T) {
	var _ logical.Backend = &plugin.PluginBackend{}
}

func TestBackend(t *testing.T) {
	config, cleanup := testConfig(t)
	defer cleanup()

	_, err := plugin.Backend(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackend_Factory(t *testing.T) {
	config, cleanup := testConfig(t)
	defer cleanup()

	_, err := plugin.Factory(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackend_PluginMain(t *testing.T) {
	args := []string{}
	if os.Getenv(pluginutil.PluginUnwrapTokenEnv) == "" && os.Getenv(pluginutil.PluginMetadataModeEnv) != "true" {
		return
	}

	caPEM := os.Getenv(pluginutil.PluginCACertPEMEnv)
	if caPEM == "" {
		t.Fatal("CA cert not passed in")
	}

	args = append(args, fmt.Sprintf("--ca-cert=%s", caPEM))

	apiClientMeta := &api.PluginAPIClientMeta{}
	flags := apiClientMeta.FlagSet()
	flags.Parse(args)
	tlsConfig := apiClientMeta.GetTLSConfig()
	tlsProviderFunc := api.VaultPluginTLSProvider(tlsConfig)

	err := logicalPlugin.Serve(&logicalPlugin.ServeOpts{
		BackendFactoryFunc: mock.Factory,
		TLSProviderFunc:    tlsProviderFunc,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T) (*logical.BackendConfig, func()) {
	cluster := vault.NewTestCluster(t, nil, &vault.TestClusterOptions{
		HandlerFunc: vaulthttp.Handler,
	})
	cluster.Start()
	cores := cluster.Cores

	core := cores[0]

	sys := vault.TestDynamicSystemView(core.Core)

	config := &logical.BackendConfig{
		Logger: logging.NewVaultLogger(log.Debug),
		System: sys,
		Config: map[string]string{
			"plugin_name": "mock-plugin",
			"plugin_type": "database",
		},
	}

	os.Setenv(pluginutil.PluginCACertPEMEnv, cluster.CACertPEMFile)

	vault.TestAddTestPlugin(t, core.Core, "mock-plugin", consts.PluginTypeDatabase, "TestBackend_PluginMain", []string{}, "")

	return config, func() {
		cluster.Cleanup()
	}
}
