package summary

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// This annotates only a newly prepared report. Existing snapshots, totals and
// formulas are never rewritten or recomputed while delivering a retry.
func buildDailySummaryReport(subscription store.Subscription, from, to time.Time, content dailySummaryContent) string {
	coverage := content.RecoveryCoverage
	text := buildSummaryText(subscription, from, to, content.Statistics, content.AddressEvents, content.MarketEvents, content.MarketSummaries, coverage != nil && coverage.Status == "incomplete")
	english := normalizeLanguage(subscription.DailySummaryLanguage) == "en"
	if coverage != nil && coverage.Status == "incomplete" {
		names := []string{}
		gapNotes := []string{}
		for _, chain := range coverage.Chains {
			if chain.Status != "complete" {
				names = append(names, marketChainName(chain.ChainKey))
			}
			for _, gap := range chain.Gaps[:min(len(chain.Gaps), 3)] {
				rangeText := dailySummaryLabel("起始时间未知", "start time unknown", english)
				if gap.ThroughAt.IsZero() {
					rangeText = dailySummaryLabel("时间未知", "time unknown", english)
				} else if gap.FromAt != nil && !gap.FromAt.IsZero() {
					rangeText = periodLabel(*gap.FromAt, gap.ThroughAt, subscription.DailySummaryTimezone, english)
				} else {
					location, zone := loadLocation(subscription.DailySummaryTimezone)
					rangeText += " → " + gap.ThroughAt.In(location).Format("2006-01-02 15:04") + " (" + zone + ")"
				}
				gapNotes = append(gapNotes, html.EscapeString(fmt.Sprintf("%s · %s · %s %d–%d", marketChainName(chain.ChainKey), rangeText, dailySummaryLabel("区块", "blocks", english), gap.FromBlock, gap.ThroughBlock)))
			}
			if len(chain.Gaps) > 3 {
				gapNotes = append(gapNotes, html.EscapeString(fmt.Sprintf(dailySummaryLabel("另 %d 段缺口保留在本报告记录中。", "%d additional gaps are retained in this report's record.", english), len(chain.Gaps)-3)))
			}
		}
		scope := strings.Join(names, ", ")
		if scope == "" {
			scope = dailySummaryLabel("当前项目", "the current project", english)
		}
		warning := fmt.Sprintf("采集覆盖说明：%s 已配置的市场采集范围在本期存在已记录的历史缺口或覆盖证据不足。本报仅汇总已采集并保留的记录；零条记录不代表链上没有活动。", html.EscapeString(scope))
		if english {
			warning = fmt.Sprintf("Collection coverage: the configured market collection scope on %s has recorded historical gaps or insufficient coverage evidence in this period. This report includes only collected, retained records; zero recorded events does not prove there was no on-chain activity.", html.EscapeString(scope))
		}
		if len(gapNotes) > 0 {
			warning = notificationfmt.JoinBlocks(warning, strings.Join(gapNotes, "<br/>"))
		}
		text = notificationfmt.JoinBlocks(warning, text)
	}
	location, _ := loadLocation(subscription.DailySummaryTimezone)
	if from.Before(to.In(location).AddDate(0, 0, -1)) {
		note := "合并日报：本期从上次确认成功的截止点合并统计，实际起止时间见统计周期。"
		if english {
			note = "Combined report: this period starts at the last confirmed successful cutoff; see Period for the actual range."
		}
		text = notificationfmt.JoinBlocks(note, text)
	}
	return text
}
