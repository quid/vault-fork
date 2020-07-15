package command

import (
	"github.com/quid/vault/command/server"
	"github.com/quid/vault/vault"
)

var (
	adjustCoreConfigForEnt = adjustCoreConfigForEntNoop
)

func adjustCoreConfigForEntNoop(config *server.Config, coreConfig *vault.CoreConfig) {
}
