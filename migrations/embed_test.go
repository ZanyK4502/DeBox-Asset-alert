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
	if got := names[len(names)-1]; got != "0041_holder_refresh_rolling_budget.sql" {
		t.Fatalf("latest migration = %q, want holder rolling budget migration", got)
	}

	requiredTables := []string{
		"resource_capacity_holds",
		"subscriptions",
		"watch_rules",
		"orders",
		"alert_events",
		"notification_groups",
		"user_preferences",
		"auth_challenges",
		"auth_sessions",
		"permanent_plan_allowlist",
		"combination_rules",
		"combination_rule_members",
		"aggregation_windows",
		"aggregation_window_members",
		"rule_trigger_events",
		"aggregate_notifications",
		"market_projects",
		"market_assets",
		"market_asset_deployments",
		"market_asset_identity_evidence",
		"market_project_deployments",
		"market_pools",
		"market_project_pools",
		"market_snapshots",
		"market_rules",
		"market_rule_deployments",
		"market_rule_pools",
		"market_rule_target_states",
		"market_events",
		"market_rule_events",
		"market_holders",
		"market_holder_snapshots",
		"market_address_labels",
		"market_chain_cursors",
		"market_chain_collection_states",
		"market_collection_idle_intervals",
		"market_collection_gaps",
		"market_collection_history_ranges",
		"market_collection_history_reviews",
		"nodit_webhook_subscriptions",
		"webhook_inbox",
		"market_scanned_blocks",
		"market_provider_health",
		"market_provider_usage",
		"market_stage_windows",
		"market_stage_window_events",
		"market_combination_rules",
		"market_combination_rule_projects",
		"market_combination_members",
		"market_combination_windows",
		"market_combination_window_members",
		"market_combination_trigger_events",
		"notification_detail_snapshots",
		"market_trade_scopes",
		"market_transaction_receipts",
		"market_transaction_events",
		"market_trade_parents",
		"market_trade_legs",
		"market_trade_ready_events",
		"market_four_meme_states",
		"market_trade_parent_summaries",
		"market_trade_pool_summaries",
		"market_scan_coverage",
		"market_holder_refresh_states",
		"market_holder_snapshot_refs",
		"market_holder_history_cleanup_cursor",
		"wallet_notification_outbox",
		"wallet_trigger_recovery_decisions",
		"wallet_notification_unknown_resolutions",
		"market_combination_source_decisions",
		"market_delivery_unknown_resolutions",
		"notification_recovery_boundaries",
		"notification_recovery_boundary_history",
		"notification_recovery_gaps",
		"daily_summary_dispatches",
		"daily_summary_unknown_resolutions",
		"daily_summary_legacy_reviews",
		"notification_send_admission",
		"notification_send_path_admission",
		"market_trade_rule_readiness",
		"market_trade_warmup_periods",
		"market_project_trade_scope_changes",
		"market_holder_refresh_scheduler",
		"wallet_balance_read_states",
		"wallet_balance_source_transitions",
	}
	destructive := regexp.MustCompile(
		`(?im)^\s*(drop\s+(table|column)|truncate|delete|rename)\b`,
	)
	dataUpdate := regexp.MustCompile(`(?im)^\s*update\b`)
	destructiveAlter := regexp.MustCompile(
		`(?im)^\s*alter\s+table\b.*\b(drop\s+(column|table)|rename)\b`,
	)
	alterStatement := regexp.MustCompile(`(?ims)^\s*alter\s+table\b.*?;`)

	combined := ""
	for _, name := range names {
		body, err := Files.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", name, err)
		}
		sql := strings.ToLower(string(body))
		removesEmptyComplimentaryTable := name == "0010_remove_complimentary_grants.sql" &&
			strings.Contains(sql, "if exists (select 1 from complimentary_grants)") &&
			strings.Contains(sql, "drop table complimentary_grants")
		repeatsSafeComplimentaryCleanup := name == "0011_multichain_market_domain.sql" &&
			strings.Contains(sql, "if exists (select 1 from complimentary_grants)") &&
			strings.Contains(sql, "drop table complimentary_grants")
		if (destructive.MatchString(sql) || destructiveAlter.MatchString(sql)) &&
			!removesEmptyComplimentaryTable &&
			!repeatsSafeComplimentaryCleanup {
			t.Fatalf("migration %q contains a destructive statement", name)
		}
		executableSQL := sql
		if name == "0040_market_collection_lifecycle.sql" {
			// Defining a lifecycle trigger does not execute its future updates.
			// Continue checking every top-level migration statement below.
			routines := regexp.MustCompile(`(?is)create\s+or\s+replace\s+function\b.*?\$\$.*?\$\$\s*;`)
			executableSQL = routines.ReplaceAllString(sql, "")
		}
		if dataUpdate.MatchString(executableSQL) &&
			name != "0011_multichain_market_domain.sql" &&
			name != "0012_multichain_market_collection.sql" &&
			name != "0014_repair_multichain_collection_links.sql" &&
			name != "0017_market_project_expiry_freeze.sql" &&
			name != "0018_independent_daily_summary_targets.sql" {
			t.Fatalf("migration %q contains an unreviewed data update", name)
		}
		for _, statement := range alterStatement.FindAllString(sql, -1) {
			if !strings.Contains(statement, "add column if not exists") &&
				!strings.Contains(statement, "drop constraint if exists") &&
				!strings.Contains(statement, "add constraint") {
				t.Fatalf(
					"migration %q has a non-additive ALTER TABLE: %s",
					name,
					strings.TrimSpace(statement),
				)
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

	multichainMigration, err := Files.ReadFile("0011_multichain_market_domain.sql")
	if err != nil {
		t.Fatalf("read multi-chain migration: %v", err)
	}
	multichainSQL := strings.ToLower(string(multichainMigration))
	for _, required := range []string{
		"insert into market_assets",
		"update market_projects",
		"insert into market_asset_deployments",
		"insert into market_project_deployments",
		"update market_project_pools",
		"insert into market_rule_deployments",
		"insert into market_rule_pools",
		"update market_snapshots",
		"update market_events",
		"update market_holders",
		"update market_holder_snapshots",
		"update market_address_labels",
	} {
		if !strings.Contains(multichainSQL, required) {
			t.Errorf("multi-chain migration is missing %q", required)
		}
	}

	for _, wallet := range []string{
		"0xcba3fce9d49ce5d7870443f324a8dd56a5788bfc",
		"0xe4f1f421d116ed75822c4527bfaf332566043b2d",
		"0x50d593be2c06d7b13c5deb3b9565b4b54ebda3a1",
		"0xdd7e931d86c1ae7d38453e2c261e048f323497c4",
		"0xcd44ffeb623bdc62a821a0301fad91e1c44c3643",
		"0xafd51cf3f190fda0615e93b409924c747c96addb",
		"0x5bca2ee4cc86f092083e8699a68bd4a05380a80a",
		"0x4f321ba96a75cd522511697bc5c3e0b3228df8b8",
		"0xf10112f10e6073e935cebca085a32abc8b856b5e",
	} {
		if !strings.Contains(combined, wallet) {
			t.Errorf("permanent allowlist is missing wallet %q", wallet)
		}
	}
}
