package inmem

import (
	"testing"

	log "github.com/hashicorp/go-hclog"
	"github.com/quid/vault/sdk/helper/logging"
	"github.com/quid/vault/sdk/physical"
)

func TestInmem(t *testing.T) {
	logger := logging.NewVaultLogger(log.Debug)

	inm, err := NewInmem(nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	physical.ExerciseBackend(t, inm)
	physical.ExerciseBackend_ListPrefix(t, inm)
}
