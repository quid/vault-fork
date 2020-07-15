package consul

import (
	"context"
	"fmt"

	"github.com/hashicorp/consul/api"
	"github.com/quid/vault/sdk/logical"
)

func (b *backend) client(ctx context.Context, s logical.Storage) (*api.Client, error, error) {
	conf, userErr, intErr := b.readConfigAccess(ctx, s)
	if intErr != nil {
		return nil, nil, intErr
	}
	if userErr != nil {
		return nil, userErr, nil
	}
	if conf == nil {
		return nil, nil, fmt.Errorf("no error received but no configuration found")
	}

	consulConf := api.DefaultNonPooledConfig()
	consulConf.Address = conf.Address
	consulConf.Scheme = conf.Scheme
	consulConf.Token = conf.Token
	consulConf.TLSConfig.CAPem = []byte(conf.CACert)
	consulConf.TLSConfig.CertPEM = []byte(conf.ClientCert)
	consulConf.TLSConfig.KeyPEM = []byte(conf.ClientKey)

	client, err := api.NewClient(consulConf)
	return client, nil, err
}
