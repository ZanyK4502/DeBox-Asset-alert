package marketparse

import "fmt"

func parseTokenTransfer(
	log Log,
	targets map[string]struct{},
) ([]Event, bool, error) {
	if len(targets) == 0 || len(log.Topics) == 0 ||
		log.Topics[0] != topicERC20Transfer {
		return nil, false, nil
	}
	if _, monitored := targets[log.Address]; !monitored {
		return nil, false, nil
	}
	if len(log.Topics) != 3 {
		return nil, true, fmt.Errorf("ERC20 Transfer requires 3 topics")
	}
	values, err := exactWords(log.Data, 1)
	if err != nil {
		return nil, true, fmt.Errorf("ERC20 Transfer: %w", err)
	}
	from, err := topicAddress(log.Topics[1])
	if err != nil {
		return nil, true, err
	}
	to, err := topicAddress(log.Topics[2])
	if err != nil {
		return nil, true, err
	}
	return []Event{{
		Type:             EventTokenTransfer,
		Protocol:         "erc20",
		Version:          "evm",
		TokenAddress:     log.Address,
		WalletAddress:    from,
		RecipientAddress: to,
		TokenAmountRaw:   unsigned(values[0]).String(),
		TransactionHash:  log.TransactionHash,
		TransactionIndex: log.TransactionIndex,
		LogIndex:         log.LogIndex,
		LogIndices:       []uint64{log.LogIndex},
		BlockNumber:      log.BlockNumber,
		BlockHash:        log.BlockHash,
		Source:           "onchain_log",
		Confidence:       "1.0000",
		Metadata: map[string]string{
			"from_address": from,
			"to_address":   to,
		},
	}}, true, nil
}
