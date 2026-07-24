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
		"combination_rules",
		"combination_rule_members",
		"aggregation_windows",
		"aggregation_window_members",
		"rule_trigger_events",
		"aggregate_notifications",
	}
	destructive := regexp.MustCompile(`(?im)^\s*(drop|truncate|delete|update|rename)\b`)
	destructiveAlter := regexp.MustCompile(`(?im)^\s*alter\s+table\b.*\b(drop|rename)\b`)

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
				!strings.Contains(line, "add column if not exists") {
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
}
