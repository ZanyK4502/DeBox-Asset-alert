package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type CreateOrderParams struct {
	DeBoxUserID      string
	PayerAddress     string
	PlanCode         string
	ChainKey         string
	ChainID          int32
	TokenAddress     *string
	TokenSymbol      string
	TokenDecimals    int32
	TotalAmount      string
	RecipientAddress string
}

func (s *Store) ExpirePendingOrders(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE orders
		SET status = 'expired'
		WHERE status = 'pending' AND expires_at < NOW()
	`)
	if err != nil {
		return 0, fmt.Errorf("expire pending orders: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) CreateOrder(ctx context.Context, params CreateOrderParams) (Order, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (Order, error) {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", params.DeBoxUserID); err != nil {
			return Order{}, fmt.Errorf("lock payment orders: %w", err)
		}

		var activePlan string
		hasActivePlan := true
		err := tx.QueryRow(ctx, `
			SELECT plan_code
			FROM subscriptions
			WHERE debox_user_id = $1 AND status = 'active' AND expires_at > NOW()
			ORDER BY expires_at DESC
			LIMIT 1
		`, params.DeBoxUserID).Scan(&activePlan)
		if isNoRows(err) {
			hasActivePlan = false
		} else if err != nil {
			return Order{}, fmt.Errorf("get active plan for order: %w", err)
		}
		if hasActivePlan && activePlan != "free" && activePlan != params.PlanCode {
			return Order{}, ErrActiveSubscriptionConflict
		}

		var confirming bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM orders
				WHERE debox_user_id = $1 AND status = 'confirming'
			)
		`, params.DeBoxUserID).Scan(&confirming); err != nil {
			return Order{}, fmt.Errorf("check confirming order: %w", err)
		}
		if confirming {
			return Order{}, ErrOrderConflict
		}

		if _, err := tx.Exec(ctx, `
			UPDATE orders
			SET status = 'expired',
			    verification_error = 'replaced by a newer payment order'
			WHERE debox_user_id = $1 AND status = 'pending'
		`, params.DeBoxUserID); err != nil {
			return Order{}, fmt.Errorf("replace pending order: %w", err)
		}

		order, err := collectOne[Order](ctx, tx, `
			INSERT INTO orders (
				debox_user_id, payer_address, plan_code, chain_key, chain_id,
				token_address, token_symbol, token_decimals, total_amount,
				recipient_address, expires_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING `+orderColumns,
			params.DeBoxUserID,
			params.PayerAddress,
			params.PlanCode,
			params.ChainKey,
			params.ChainID,
			params.TokenAddress,
			params.TokenSymbol,
			params.TokenDecimals,
			params.TotalAmount,
			params.RecipientAddress,
			time.Now().UTC().Add(20*time.Minute),
		)
		if err != nil {
			if isUniqueViolation(err) {
				return Order{}, ErrOrderConflict
			}
			return Order{}, fmt.Errorf("create order: %w", err)
		}
		return order, nil
	})
}

func (s *Store) GetOrder(ctx context.Context, orderID int64) (*Order, error) {
	order, err := collectOptional[Order](ctx, s.db, `
		SELECT `+orderColumns+` FROM orders WHERE id = $1
	`, orderID)
	if err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	return order, nil
}

func (s *Store) ClaimOrderTransaction(
	ctx context.Context,
	orderID int64,
	deboxUserID string,
	payerAddress string,
	txHash string,
) (Order, error) {
	order, err := collectOne[Order](ctx, s.db, `
		UPDATE orders
		SET status = 'confirming',
		    tx_hash = $1,
		    verified_at = NOW(),
		    verification_error = ''
		WHERE id = $2
		  AND debox_user_id = $3
		  AND LOWER(payer_address) = LOWER($4)
		  AND ((status = 'pending' AND expires_at > NOW()) OR status = 'confirming')
		  AND (tx_hash IS NULL OR tx_hash = '' OR LOWER(tx_hash) = LOWER($1))
		RETURNING `+orderColumns,
		txHash,
		orderID,
		deboxUserID,
		payerAddress,
	)
	if err == nil {
		return order, nil
	}
	if isUniqueViolation(err) {
		return Order{}, ErrOrderTransactionUsed
	}
	if !isNoRows(err) {
		return Order{}, fmt.Errorf("claim order transaction: %w", err)
	}

	existing, err := s.GetOrder(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if existing != nil &&
		existing.Status == "paid" &&
		existing.TxHash != nil &&
		strings.EqualFold(*existing.TxHash, txHash) {
		return *existing, nil
	}
	return Order{}, ErrOrderInvalid
}

type UpdateOrderVerificationParams struct {
	Status        string
	BlockNumber   *int64
	Confirmations int
	Error         string
}

func (s *Store) UpdateOrderVerification(
	ctx context.Context,
	orderID int64,
	params UpdateOrderVerificationParams,
) (Order, error) {
	order, err := collectOne[Order](ctx, s.db, `
		UPDATE orders
		SET status = $1,
		    tx_block_number = $2,
		    tx_confirmations = $3,
		    verified_at = NOW(),
		    verification_error = $4
		WHERE id = $5 AND status <> 'paid'
		RETURNING `+orderColumns,
		params.Status,
		params.BlockNumber,
		max(params.Confirmations, 0),
		truncate(params.Error, 500),
		orderID,
	)
	if err == nil {
		return order, nil
	}
	if !isNoRows(err) {
		return Order{}, fmt.Errorf("update order verification: %w", err)
	}
	existing, err := s.GetOrder(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if existing == nil {
		return Order{}, ErrNotFound
	}
	return *existing, nil
}

func (s *Store) ListConfirmingOrders(ctx context.Context, limit int) ([]Order, error) {
	orders, err := collectMany[Order](ctx, s.db, `
		SELECT `+orderColumns+`
		FROM orders
		WHERE status = 'confirming' AND tx_hash IS NOT NULL AND tx_hash <> ''
		ORDER BY verified_at ASC NULLS FIRST, id ASC
		LIMIT $1
	`, clamp(limit, 1, 200))
	if err != nil {
		return nil, fmt.Errorf("list confirming orders: %w", err)
	}
	return orders, nil
}

type FinalizedOrder struct {
	Order        Order         `json:"order"`
	Subscription *Subscription `json:"subscription"`
}

func (s *Store) FinalizePaidOrder(
	ctx context.Context,
	orderID int64,
	txHash string,
	blockNumber int64,
	confirmations int,
	subscriptionDays int,
) (FinalizedOrder, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (FinalizedOrder, error) {
		order, err := collectOne[Order](ctx, tx, `
			SELECT `+orderColumns+` FROM orders WHERE id = $1 FOR UPDATE
		`, orderID)
		if isNoRows(err) {
			return FinalizedOrder{}, ErrNotFound
		}
		if err != nil {
			return FinalizedOrder{}, fmt.Errorf("lock paid order: %w", err)
		}
		if order.Status == "paid" {
			if order.TxHash == nil || !strings.EqualFold(*order.TxHash, txHash) {
				return FinalizedOrder{}, ErrOrderInvalid
			}
			subscription, err := collectOptional[Subscription](ctx, tx, `
				SELECT `+subscriptionColumns+`
				FROM subscriptions
				WHERE debox_user_id = $1 AND status = 'active' AND expires_at > NOW()
				ORDER BY expires_at DESC LIMIT 1
			`, order.DeBoxUserID)
			if err != nil {
				return FinalizedOrder{}, fmt.Errorf("get finalized subscription: %w", err)
			}
			return FinalizedOrder{Order: order, Subscription: subscription}, nil
		}
		if order.Status != "confirming" ||
			order.TxHash == nil ||
			!strings.EqualFold(*order.TxHash, txHash) {
			return FinalizedOrder{}, ErrOrderInvalid
		}

		subscription, err := activateSubscription(
			ctx,
			tx,
			order.DeBoxUserID,
			order.PlanCode,
			subscriptionDays,
			true,
		)
		if err != nil {
			return FinalizedOrder{}, err
		}
		paidOrder, err := collectOne[Order](ctx, tx, `
			UPDATE orders
			SET status = 'paid',
			    tx_block_number = $1,
			    tx_confirmations = $2,
			    verified_at = NOW(),
			    verification_error = '',
			    completed_at = NOW()
			WHERE id = $3 AND status = 'confirming'
			RETURNING `+orderColumns,
			blockNumber,
			confirmations,
			orderID,
		)
		if isNoRows(err) {
			return FinalizedOrder{}, ErrOrderConflict
		}
		if err != nil {
			return FinalizedOrder{}, fmt.Errorf("finalize paid order: %w", err)
		}
		return FinalizedOrder{Order: paidOrder, Subscription: &subscription}, nil
	})
}
