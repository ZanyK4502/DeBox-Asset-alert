package chain

// MatchInteractionTransactions applies the existing first-match semantics to
// an immutable list from TransactionsByAccount. It performs no network reads.
func MatchInteractionTransactions(transactions []map[string]any, address, targetAddress, chainKey, fallback string) (InteractionResult, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return InteractionResult{}, err
	}
	wallet, err := ValidateAddress(address)
	if err != nil {
		return InteractionResult{}, err
	}
	target, err := ValidateAddress(targetAddress)
	if err != nil {
		return InteractionResult{}, err
	}
	for _, transaction := range transactions {
		if containsAddress(transaction, target) {
			sourceAt, basis := interactionSourceTime(transaction)
			cursor := firstString(transaction, "transactionHash", "hash", "txHash", "id")
			if cursor == "" {
				cursor = "matched"
			}
			return InteractionResult{SourceOccurredAt: sourceAt, SourceTimeBasis: basis, Cursor: cursor, Matched: true, Transaction: transaction, ChainKey: profile.Key, ChainID: profile.ChainID, WalletAddress: wallet, TargetAddress: target}, nil
		}
	}
	return InteractionResult{Cursor: "none", Matched: false, ChainKey: profile.Key, ChainID: profile.ChainID, WalletAddress: wallet, TargetAddress: target}, nil
}
