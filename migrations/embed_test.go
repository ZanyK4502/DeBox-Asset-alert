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
	if got := names[len(names)-1]; got != "0014_repair_multichain_collection_links.sql" {
		t.Fatalf("latest migration = %q, want collection link repair migration", got)
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
		if dataUpdate.MatchString(sql) &&
			name != "0011_multichain_market_domain.sql" &&
			name != "0012_multichain_market_collection.sql" &&
			name != "0014_repair_multichain_collection_links.sql" {
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
	} {
		if !strings.Contains(combined, wallet) {
			t.Errorf("permanent allowlist is missing wallet %q", wallet)
		}
	}
}
