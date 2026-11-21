package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func validateMarketRuleRuntimeParameters(code string, window *int32, threshold string) error {
	if issues := marketwindow.RuntimeIssues(code, window, threshold); len(issues) > 0 {
		return fmt.Errorf("%w: %s", marketwindow.ErrUnsupported, issues[0].Message)
	}
	return nil
}

// Only internal, constant aliases are passed here. The registry supplies all
// SQL literals; request input is never interpolated into this predicate.
func marketRuleSupportedParametersSQL(alias string) string {
	if alias != "mr" {
		panic("unsupported internal market rule alias")
	}
	var cases []string
	for _, code := range marketwindow.KnownRules() {
		capability := marketwindow.ForRule(code)
		predicate := "TRUE"
		switch capability.Kind {
		case marketwindow.KindFixed:
			periods := make([]string, len(capability.FixedMinutes))
			for index, minutes := range capability.FixedMinutes {
				periods[index] = strconv.Itoa(int(minutes))
			}
			predicate = "mr.window_minutes IN (" + strings.Join(periods, ",") + ")"
		case marketwindow.KindCustomSnapshot, marketwindow.KindCustomEvent:
			predicate = fmt.Sprintf("mr.window_minutes BETWEEN %d AND %d", capability.MinMinutes, capability.MaxMinutes)
		}
		if code == plans.MarketHolderRankEntered || code == plans.MarketHolderRankExited {
			predicate += fmt.Sprintf(" AND mr.threshold_value BETWEEN 1 AND %d AND mr.threshold_value=TRUNC(mr.threshold_value)", marketwindow.MaxHolderRank)
		}
		cases = append(cases, "WHEN '"+code+"' THEN ("+predicate+")")
	}
	// COALESCE makes missing required periods invalid, including in NOT(audit).
	return "COALESCE((CASE mr.rule_type " + strings.Join(cases, " ") + " ELSE FALSE END),FALSE)"
}

type MarketRuleParameterImpact struct {
	Rule   MarketRule           `json:"rule"`
	Issues []marketwindow.Issue `json:"issues"`
}

type MarketRuleParameterAudit struct {
	Rules     []MarketRuleParameterImpact `json:"rules"`
	Truncated bool                        `json:"truncated"`
}

// AuditMarketRuleParameters is read-only. It includes paused and disabled
// rules, preserves every original parameter, and never rewrites runtime state.
func (s *Store) AuditMarketRuleParameters(ctx context.Context, limit int) (MarketRuleParameterAudit, error) {
	limit = clamp(limit, 1, 10000)
	rules, err := collectMany[MarketRule](ctx, s.db, `SELECT `+marketRuleColumns+`
	 FROM market_rules mr WHERE NOT (`+marketRuleSupportedParametersSQL("mr")+`) ORDER BY id LIMIT $1`, limit+1)
	if err != nil {
		return MarketRuleParameterAudit{}, fmt.Errorf("audit saved market rule parameters: %w", err)
	}
	result := MarketRuleParameterAudit{Rules: make([]MarketRuleParameterImpact, 0, min(len(rules), limit)), Truncated: len(rules) > limit}
	if len(rules) > limit {
		rules = rules[:limit]
	}
	for _, rule := range rules {
		result.Rules = append(result.Rules, MarketRuleParameterImpact{Rule: rule, Issues: marketwindow.RuntimeIssues(rule.RuleType, rule.WindowMinutes, rule.ThresholdValue)})
	}
	return result, nil
}
