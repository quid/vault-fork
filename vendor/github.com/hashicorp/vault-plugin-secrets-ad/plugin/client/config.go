package client

import (
	"time"

	"github.com/quid/vault/sdk/helper/ldaputil"
)

type ADConf struct {
	*ldaputil.ConfigEntry
	LastBindPassword         string    `json:"last_bind_password"`
	LastBindPasswordRotation time.Time `json:"last_bind_password_rotation"`
}
