package marketrules

import (
	"context"
	"fmt"
	"time"
)

func (service *Service) deferIncompleteRule(ctx context.Context, ruleID int64) error {
	if repository, ok := service.repository.(marketRuleEvaluationDeferrer); ok {
		if err := repository.DeferMarketRuleEvaluation(ctx, ruleID, time.Minute); err != nil {
			return fmt.Errorf("defer incomplete market rule %d: %w", ruleID, err)
		}
	}
	return nil
}
