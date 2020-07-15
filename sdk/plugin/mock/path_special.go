package mock

import (
	"context"

	"github.com/quid/vault/sdk/framework"
	"github.com/quid/vault/sdk/logical"
)

// pathSpecial is used to test special paths.
func pathSpecial(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "special",
		Callbacks: map[logical.Operation]framework.OperationFunc{
			logical.ReadOperation: b.pathSpecialRead,
		},
	}
}

func (b *backend) pathSpecialRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	// Return the secret
	return &logical.Response{
		Data: map[string]interface{}{
			"data": "foo",
		},
	}, nil

}
