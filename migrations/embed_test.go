package migrations

import (
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedMigrationsAreForwardOnlyAndComplete(t *testing.T) {
	t.Parallel()

	names, err := Names()
	if err != nil {
		t.Fatalf("Names(): %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations embedded")
	}
	if got := names[len(names)-1]; got != "0009_permanent_plan_allowlist.sql" {
		t.Fatalf("latest migration = %q, want permanent plan allowlist migration", got)
	}

	requiredTables := []string{
		"subscriptions",
		"watch_rules",
		"orders",
		"alert_events",
		"notification_groups",
		"user_preferences",
		"auth_challenges",
		"auth_sessions",
		"complimentary_grants",
		"permanent_plan_allowlist",
		"combination_rules",
		"combination_rule_members",
		"aggregation_windows",
		"aggregation_window_members",
		"rule_trigger_events",
		"aggregate_notifications",
		"market_projects",
		"market_pools",
		"market_project_pools",
		"market_snapshots",
		"market_rules",
		"market_events",
		"market_rule_events",
		"market_holders",
		"market_holder_snapshots",
		"market_address_labels",
		"market_chain_cursors",
		"nodit_webhook_subscriptions",
		"webhook_inbox",
		"market_scanned_blocks",
		"market_provider_health",
		"market_provider_usage",
		"market_stage_windows",
		"market_stage_window_events",
		"market_combination_rules",
		"market_combination_members",
		"market_combination_windows",
		"market_combination_window_members",
		"market_combination_trigger_events",
	}
	destructive := regexp.MustCompile(
		`(?im)^\s*(drop\s+(table|column)|truncate|delete|update|rename)\b`,
	)
	destructiveAlter := regexp.MustCompile(
		`(?im)^\s*alter\s+table\b.*\b(drop\s+(column|table)|rename)\b`,
	)

	combined := ""
	for _, name := range names {
		body, err := Files.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", name, err)
		}
		sql := strings.ToLower(string(body))
		if destructive.MatchString(sql) || destructiveAlter.MatchString(sql) {
			t.Fatalf("migration %q contains a destructive statement", name)
		}
		for _, line := range strings.Split(sql, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "alter table") &&
				!strings.Contains(line, "add column if not exists") &&
				!strings.Contains(line, "drop constraint if exists") &&
				!strings.Contains(line, "add constraint") {
				t.Fatalf("migration %q has a non-additive ALTER TABLE: %s", name, line)
			}
		}
		combined += "\n" + sql
	}

	for _, table := range requiredTables {
		if !strings.Contains(combined, "create table if not exists "+table) {
			t.Errorf("missing idempotent creation for table %q", table)
		}
	}
	if strings.Contains(combined, " double precision") ||
		strings.Contains(combined, " real ") {
		t.Fatal("financial market data must not use floating-point PostgreSQL types")
	}

	for _, wallet := range []string{
		"0xcba3fce9d49ce5d7870443f324a8dd56a5788bfc",
		"0xe4f1f421d116ed75822c4527bfaf332566043b2d",
		"0x50d593be2c06d7b13c5deb3b9565b4b54ebda3a1",
		"0xdd7e931d86c1ae7d38453e2c261e048f323497c4",
		"0xcd44ffeb623bdc62a821a0301fad91e1c44c3643",
	} {
		if !strings.Contains(combined, wallet) {
			t.Errorf("permanent allowlist is missing wallet %q", wallet)
		}
	}
}
