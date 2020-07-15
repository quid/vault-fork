package sealhelper

import (
	"path"

	"github.com/hashicorp/go-hclog"
	"github.com/quid/vault/api"
	"github.com/quid/vault/builtin/logical/transit"
	"github.com/quid/vault/helper/testhelpers/teststorage"
	"github.com/quid/vault/http"
	"github.com/quid/vault/internalshared/configutil"
	"github.com/quid/vault/sdk/helper/logging"
	"github.com/quid/vault/sdk/logical"
	"github.com/quid/vault/vault"
	"github.com/quid/vault/vault/seal"
	"github.com/mitchellh/go-testing-interface"
)

type TransitSealServer struct {
	*vault.TestCluster
}

func NewTransitSealServer(t testing.T) *TransitSealServer {
	conf := &vault.CoreConfig{
		LogicalBackends: map[string]logical.Factory{
			"transit": transit.Factory,
		},
	}
	opts := &vault.TestClusterOptions{
		NumCores:    1,
		HandlerFunc: http.Handler,
		Logger:      logging.NewVaultLogger(hclog.Trace).Named(t.Name()).Named("transit"),
	}
	teststorage.InmemBackendSetup(conf, opts)
	cluster := vault.NewTestCluster(t, conf, opts)
	cluster.Start()

	if err := cluster.Cores[0].Client.Sys().Mount("transit", &api.MountInput{
		Type: "transit",
	}); err != nil {
		t.Fatal(err)
	}

	return &TransitSealServer{cluster}
}

func (tss *TransitSealServer) MakeKey(t testing.T, key string) {
	client := tss.Cores[0].Client
	if _, err := client.Logical().Write(path.Join("transit", "keys", key), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Logical().Write(path.Join("transit", "keys", key, "config"), map[string]interface{}{
		"deletion_allowed": true,
	}); err != nil {
		t.Fatal(err)
	}
}

func (tss *TransitSealServer) MakeSeal(t testing.T, key string) vault.Seal {
	client := tss.Cores[0].Client
	wrapperConfig := map[string]string{
		"address":     client.Address(),
		"token":       client.Token(),
		"mount_path":  "transit",
		"key_name":    key,
		"tls_ca_cert": tss.CACertPEMFile,
	}
	transitSeal, _, err := configutil.GetTransitKMSFunc(nil, &configutil.KMS{Config: wrapperConfig})
	if err != nil {
		t.Fatalf("error setting wrapper config: %v", err)
	}

	return vault.NewAutoSeal(&seal.Access{
		Wrapper: transitSeal,
	})
}
