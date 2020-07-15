package main

import (
	"log"
	"os"

	"github.com/quid/vault/api"
	"github.com/quid/vault/plugins/database/postgresql"
)

func main() {
	apiClientMeta := &api.PluginAPIClientMeta{}
	flags := apiClientMeta.FlagSet()
	flags.Parse(os.Args[1:])

	err := postgresql.Run(apiClientMeta.GetTLSConfig())
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
