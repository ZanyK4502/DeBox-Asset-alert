package subscription

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestQuotaErrorPreservesResourceCapacityErrorIdentity(t *testing.T) {
	for _, sentinel := range []error{store.ErrResourceCapacityUnavailable, store.ErrResourceCapacityReached, store.ErrResourceCapacityState} {
		for _, restoring := range []bool{false, true} {
			for _, wrapped := range []error{fmt.Errorf("admission: %w", sentinel), errors.Join(sentinel, store.ErrRuleLimitReached)} {
				got := quotaError(wrapped, plans.Plan{RuleLimit: 3}, restoring)
				if got != wrapped || !errors.Is(got, sentinel) {
					t.Fatalf("capacity became a plan quota message: %v", got)
				}
			}
		}
	}
}
