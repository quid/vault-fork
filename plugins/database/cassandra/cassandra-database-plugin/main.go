package main

import (
	"log"
	"os"

	"github.com/quid/vault/api"
	"github.com/quid/vault/plugins/database/cassandra"
)

func main() {
	apiClientMeta := &api.PluginAPIClientMeta{}
	flags := apiClientMeta.FlagSet()
	flags.Parse(os.Args[1:])

	err := cassandra.Run(apiClientMeta.GetTLSConfig())
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
