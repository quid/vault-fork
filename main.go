package main // import "github.com/quid/vault"

import (
	"os"

	"github.com/quid/vault/command"
)

func main() {
	os.Exit(command.Run(os.Args[1:]))
}
