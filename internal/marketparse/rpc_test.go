package marketparse

import "github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"

func structRPCLog() chain.RPCLog {
	return chain.RPCLog{
		Address:          testPoolA,
		Topics:           []string{topicV2Swap},
		Data:             "0x",
		BlockNumber:      "0x10",
		TransactionHash:  testHash,
		TransactionIndex: "0x2",
		BlockHash:        testBlock,
		LogIndex:         "0x3",
	}
}
