package marketcollector

import "context"

// fetchScanBlockIdentityHeader reads a full block through the bounded primary
// client. A usable header with incomplete identities still supports the
// existing individual transaction and receipt path.
func (service *Service) fetchScanBlockIdentityHeader(ctx context.Context, reader marketBlockReceiptReader, number uint64, count int, expected map[string]receiptExpectation) (blockHeader, map[string]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return blockHeader{}, nil, err
	}
	block, err := reader.MarketFullBlock(ctx, number, service.settings.ChainKey, service.settings.ChainFallback)
	if err != nil {
		return blockHeader{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return blockHeader{}, nil, err
	}
	header, err := decodeRequestedBlockHeader(number, block)
	if err != nil {
		return blockHeader{}, nil, err
	}
	if count < 1 {
		return header, nil, nil
	}
	identities := verifiedScanBlockIdentities(header, block["transactions"], expected, count)
	if err := ctx.Err(); err != nil {
		return blockHeader{}, nil, err
	}
	return header, identities, nil
}

func verifiedScanBlockIdentities(header blockHeader, transactions any, expected map[string]receiptExpectation, count int) map[string]map[string]any {
	identities := scanBlockIdentities(header, transactions, expected, count)
	indices := make(map[uint64]struct{}, len(identities))
	for _, identity := range identities {
		blockNumber, _ := parseHexQuantity(rpcString(identity, "blockNumber"))
		index, _ := parseHexQuantity(rpcString(identity, "transactionIndex"))
		if _, duplicate := indices[index]; duplicate || blockNumber != header.Number || rpcString(identity, "blockHash") != header.Hash {
			return nil
		}
		indices[index] = struct{}{}
	}
	return identities
}
