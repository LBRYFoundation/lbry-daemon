package wallet

import (
	_ "embed"
	"encoding/hex"
	"fmt"
)

const (
	checkpointInterval   = 1000
	checkpointDigestSize = 32
)

// mainnetCheckpointData stores the pinned Python SDK checkpoint hashes as raw
// digest bytes. A string keeps the embedded table immutable after startup.
//
//go:embed mainnet_checkpoints.bin
var mainnetCheckpointData string

var (
	mainnetCheckpoints = mustCheckpointTable(mainnetCheckpointData)
	emptyCheckpoints   = mustCheckpointTable("")
)

type checkpointTable struct {
	data string
}

func newCheckpointTable(data string) (checkpointTable, error) {
	if len(data)%checkpointDigestSize != 0 {
		return checkpointTable{}, fmt.Errorf(
			"checkpoint data is %d bytes, not a multiple of %d",
			len(data), checkpointDigestSize,
		)
	}
	return checkpointTable{data: data}, nil
}

func mustCheckpointTable(data string) checkpointTable {
	table, err := newCheckpointTable(data)
	if err != nil {
		panic(err)
	}
	return table
}

func (table checkpointTable) len() int {
	return len(table.data) / checkpointDigestSize
}

func (table checkpointTable) lastHeight() int {
	if table.len() == 0 {
		return -1
	}
	return (table.len() - 1) * checkpointInterval
}

// lookup returns the checkpoint hash for an exact checkpoint height in the
// same lowercase hexadecimal form as lbry.wallet.checkpoints.HASHES.
func (table checkpointTable) lookup(height int) (string, bool) {
	if height < 0 || height%checkpointInterval != 0 {
		return "", false
	}
	return table.at(height / checkpointInterval)
}

// at returns the checkpoint hash at its implicit ascending table index.
func (table checkpointTable) at(index int) (string, bool) {
	if index < 0 || index >= table.len() {
		return "", false
	}
	start := index * checkpointDigestSize
	return hex.EncodeToString([]byte(table.data[start : start+checkpointDigestSize])), true
}
