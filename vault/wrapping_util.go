// +build !enterprise

package vault

import (
	"context"

	"github.com/quid/vault/sdk/logical"
)

func forwardWrapRequest(context.Context, *Core, *logical.Request, *logical.Response, *logical.Auth) (*logical.Response, error) {
	return nil, nil
}
