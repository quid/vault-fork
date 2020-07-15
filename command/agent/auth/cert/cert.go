package cert

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/go-hclog"
	"github.com/quid/vault/api"
	"github.com/quid/vault/command/agent/auth"
)

type certMethod struct {
	logger    hclog.Logger
	mountPath string
	name      string
}

func NewCertAuthMethod(conf *auth.AuthConfig) (auth.AuthMethod, error) {
	if conf == nil {
		return nil, errors.New("empty config")
	}

	// Not concerned if the conf.Config is empty as the 'name'
	// parameter is optional when using TLS Auth

	c := &certMethod{
		logger:    conf.Logger,
		mountPath: conf.MountPath,
		name:      "",
	}

	if conf.Config != nil {
		nameRaw, ok := conf.Config["name"]
		if !ok {
			nameRaw = ""
		}
		c.name, ok = nameRaw.(string)
		if !ok {
			return nil, errors.New("could not convert 'name' config value to string")
		}
	}

	return c, nil
}

func (c *certMethod) Authenticate(_ context.Context, client *api.Client) (string, http.Header, map[string]interface{}, error) {
	c.logger.Trace("beginning authentication")

	authMap := map[string]interface{}{}

	if c.name != "" {
		authMap["name"] = c.name
	}

	return fmt.Sprintf("%s/login", c.mountPath), nil, authMap, nil
}

func (c *certMethod) NewCreds() chan struct{} {
	return nil
}

func (c *certMethod) CredSuccess() {}

func (c *certMethod) Shutdown() {}
