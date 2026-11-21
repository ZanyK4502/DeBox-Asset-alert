package store

import (
	"context"
	"errors"
	"fmt"
)

var ErrSubscriptionSeatsReached = errors.New("subscription plan seats are fully reserved")

// The caller holds the resource and owner locks. Count before replacing the
// owner's pending order, so an existing seat can be renewed or reused. An
// unpaid order keeps its reservation until the usual expiry transaction marks
// it terminal; a confirming order retains its seat through settlement.
func (s *Store) checkSubscriptionSeat(ctx context.Context, tx DBTX, owner, planCode string) error {
	policy := s.resourceCapacity
	if policy == nil || !policy.SeatOnly && (!policy.Verified || resourceNetMonthlyCU(*policy) == 0) {
		return nil
	}
	var limit int
	switch planCode {
	case "standard":
		limit = policy.StandardSalesSeatLimit
	case "professional":
		limit = policy.ProfessionalSalesSeatLimit
	default:
		if policy.SeatOnly {
			return ErrOrderInvalid
		}
	}
	if limit == 0 {
		return nil
	}
	admission, ok := ctx.Value(resourceCapacityLockKey{}).(resourceCapacityAdmission)
	if !ok || admission.policy != policy || owner == "" || admission.owner != owner {
		return fmt.Errorf("%w: sales seat admission requires the owner's resource lock", ErrResourceCapacityState)
	}
	var occupied int64
	var ownerHasSeat bool
	err := tx.QueryRow(ctx, `
		WITH observed_at AS MATERIALIZED (SELECT clock_timestamp() AS at),
		seat_owners AS (
			SELECT debox_user_id
			FROM subscriptions CROSS JOIN observed_at
			WHERE plan_code = $1 AND status = 'active'
			  AND (is_permanent = 1 OR expires_at > observed_at.at)
			UNION
			SELECT debox_user_id
			FROM orders
			WHERE plan_code = $1 AND status IN ('pending', 'confirming')
		)
		SELECT COUNT(*), COALESCE(BOOL_OR(debox_user_id = $2), FALSE)
		FROM seat_owners
	`, planCode, owner).Scan(&occupied, &ownerHasSeat)
	if err != nil {
		return fmt.Errorf("%w: count occupied subscription seats: %w", ErrResourceCapacityState, err)
	}
	if !ownerHasSeat && occupied >= int64(limit) {
		return ErrSubscriptionSeatsReached
	}
	return nil
}
