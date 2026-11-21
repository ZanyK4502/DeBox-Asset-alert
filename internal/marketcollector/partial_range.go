package marketcollector

import (
	"context"
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// A previous partial pass may have stored canonical rows before a failed
// confirmation/coverage write. A reorg inside that unadvanced range must be
// reconciled before a new inclusion is persisted, not inferred from a DB error.
func (service *Service) reconcilePartialScanRange(ctx context.Context, cursor store.MarketChainCursor, ancestor blockHeader, headers map[uint64]blockHeader, from, through int64) (bool, error) {
	limit := int(through - from + 2)
	if limit > int(MaxScanBatchSize) {
		limit = int(MaxScanBatchSize)
	}
	stored, err := service.repository.ListCanonicalMarketScannedBlocks(ctx, service.settings.ChainID, service.settings.CursorKey, through, limit)
	if err != nil {
		return false, err
	}
	for _, previous := range stored {
		if previous.BlockNumber < from || previous.BlockNumber > through {
			continue
		}
		number := uint64(previous.BlockNumber)
		header, exists := headers[number]
		if !exists {
			header, err = service.fetchBlockHeader(ctx, number)
			if err != nil {
				return false, err
			}
			headers[number] = header
		}
		if previous.BlockHash != header.Hash {
			return true, service.rewindChangedScanRange(ctx, cursor, ancestor)
		}
	}
	return false, nil
}

func (service *Service) rewindChangedScanRange(ctx context.Context, cursor store.MarketChainCursor, ancestor blockHeader) error {
	fresh, err := service.fetchBlockHeader(ctx, ancestor.Number)
	if err != nil {
		return err
	}
	if fresh.Hash != ancestor.Hash {
		// The predecessor itself changed: the original deeper-reorg path must
		// locate a real canonical ancestor. Never promote this changed header.
		if cursor.LastBlockHash == nil {
			return ErrNoCanonicalAncestor
		}
		_, _, err := service.verifyCursorContinuityWithHeader(ctx, cursor)
		return err
	}
	if fresh.Number != ancestor.Number || !fresh.Timestamp.Equal(ancestor.Timestamp) || fresh.ParentHash != ancestor.ParentHash {
		return fmt.Errorf("market ancestor header changed without a corresponding hash change")
	}
	_, err = service.repository.ReconcileMarketReorg(ctx, service.settings.ChainID, service.settings.CursorKey,
		int64(ancestor.Number), ancestor.Hash, "canonical block changed inside an unadvanced scan range; retry from verified predecessor")
	return err
}
