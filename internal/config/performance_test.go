package config

import (
	"strings"
	"testing"
)

func clearPerformanceEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"DEBOX_SEND_PER_MINUTE", "MARKET_TASK_LOCK_CONNECTIONS", "MARKET_HYDRATION_CONCURRENCY", "MARKET_RPC_FALLBACK_URLS_JSON", "CHAIN_RPC_URL"} {
		t.Setenv(key, "")
	}
}
func TestPerformanceDefaultsAndLegacyFallbackIsolation(t *testing.T) {
	clearPerformanceEnv(t)
	p, err := loadPerformanceSettings()
	if err != nil || p.sendLimit != 100 || p.lockConnections != 8 || p.hydrationConcurrency != 2 {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	t.Setenv("CHAIN_RPC_URL", "https://old.example/bsc?key=secret")
	p, err = loadPerformanceSettings()
	if err != nil || len(p.fallbacks) != 1 || len(p.fallbacks["bsc"]) != 1 {
		t.Fatalf("legacy routing: %+v %v", p, err)
	}
	t.Setenv("MARKET_RPC_FALLBACK_URLS_JSON", `{"bsc":[],"base":["https://base.example/rpc"]}`)
	p, err = loadPerformanceSettings()
	if err != nil || len(p.fallbacks["bsc"]) != 0 || len(p.fallbacks["base"]) != 1 {
		t.Fatalf("explicit disable: %+v %v", p, err)
	}
}
func TestPerformanceRejectsUnsafeOrUnboundedConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DEBOX_SEND_PER_MINUTE", "6"}, {"MARKET_TASK_LOCK_CONNECTIONS", "0"}, {"MARKET_TASK_LOCK_CONNECTIONS", "17"},
		{"MARKET_HYDRATION_CONCURRENCY", "3"}, {"MARKET_RPC_FALLBACK_URLS_JSON", `{"bsc":["http://untrusted.example/secret"]}`},
		{"MARKET_RPC_FALLBACK_URLS_JSON", `{"bsc":["https://user:secret@example.com"]}`},
		{"MARKET_RPC_FALLBACK_URLS_JSON", `{"bsc":["https://a","https://b","https://c"]}`},
		{"MARKET_RPC_FALLBACK_URLS_JSON", `{"bnb":[],"bsc":[]}`},
		{"MARKET_RPC_FALLBACK_URLS_JSON", `{"unknown":[]}`},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			clearPerformanceEnv(t)
			t.Setenv(tc.key, tc.value)
			_, err := loadPerformanceSettings()
			if err == nil {
				t.Fatal("accepted invalid setting")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error leaks URL")
			}
		})
	}
}
