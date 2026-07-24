package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func TestPythonBaselineRouteContract(t *testing.T) {
	handler := New(testConfig(t))
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/health"},
		{http.MethodGet, "/api/bot/webhook-status"},
		{http.MethodGet, "/api/plans"},
		{http.MethodGet, "/api/chains"},
		{http.MethodPost, "/api/auth/challenge"},
		{http.MethodPost, "/api/auth/verify"},
		{http.MethodGet, "/api/auth/session"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/api/subscription/current"},
		{http.MethodPost, "/api/subscription/free-trial"},
		{http.MethodPost, "/api/subscription/complimentary"},
		{http.MethodPost, "/api/subscription/summary-settings"},
		{http.MethodGet, "/api/watch-rules"},
		{http.MethodPost, "/api/watch-rules"},
		{http.MethodDelete, "/api/watch-rules/paused"},
		{http.MethodDelete, "/api/watch-rules/1"},
		{http.MethodPost, "/api/watch-rules/1/free-monitor"},
		{http.MethodPost, "/api/watch-rules/1/restore"},
		{http.MethodPatch, "/api/watch-rules/1/notification-language"},
		{http.MethodGet, "/api/combination-rules"},
		{http.MethodPost, "/api/combination-rules"},
		{http.MethodDelete, "/api/combination-rules/1"},
		{http.MethodPost, "/api/combination-rules/1/restore"},
		{http.MethodPatch, "/api/combination-rules/1/notification-language"},
		{http.MethodGet, "/api/aggregate-events"},
		{http.MethodGet, "/api/chain/balance"},
		{http.MethodGet, "/api/debox/user"},
		{http.MethodGet, "/api/debox/token"},
		{http.MethodGet, "/api/notification-groups"},
		{http.MethodPost, "/api/notification-groups"},
		{http.MethodDelete, "/api/notification-groups/1"},
		{http.MethodGet, "/api/payment/config"},
		{http.MethodPost, "/api/payment/prepare"},
		{http.MethodPost, "/api/payment/verify"},
		{http.MethodPost, "/bot/webhook"},
		{http.MethodGet, "/"},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code == http.StatusNotFound || recorder.Code == http.StatusMethodNotAllowed {
				t.Fatalf("baseline route is unavailable: status=%d body=%s", recorder.Code, recorder.Body)
			}
		})
	}
}

func TestPythonBaselinePublicCatalogContract(t *testing.T) {
	handler := New(testConfig(t))

	var planPayload struct {
		Plans     []plans.Plan     `json:"plans"`
		RuleTypes []plans.RuleType `json:"rule_types"`
	}
	getJSON(t, handler, "/api/plans", &planPayload)

	dailyLimit := 5
	ruleTypes := expectedRuleTypes()
	expectedPlans := []plans.Plan{
		{
			Code: "free", Name: "免费版", Price: "0", Asset: "USDT", Days: 0,
			WalletLimit: 1, RuleLimit: 1, GroupLimit: 0, DailyAlertLimit: &dailyLimit,
			AllowedRuleTypes: []string{
				"balance_change", "incoming", "outgoing",
				"balance_threshold", "balance_threshold_high",
			},
			AllowedRules: ruleTypes[:5], PrivateNotification: true,
			Description: "1 个钱包、1 条基础规则，每日最多 5 次提醒，仅支持私聊通知。",
		},
		{
			Code: "standard", Name: "标准版", Price: "10", Asset: "USDT", Days: 30,
			WalletLimit: 3, RuleLimit: 10, GroupLimit: 0,
			AllowedRuleTypes: []string{
				"balance_change", "incoming", "outgoing",
				"balance_threshold", "balance_threshold_high", "approval_change",
			},
			AllowedRules: ruleTypes[:6], PrivateNotification: true, DailySummary: true,
			SummaryTargets: []string{"private"},
			Description:    "适合个人监控：3 个钱包、10 条规则，支持资产变化、Approve 监控、私聊通知和每日摘要。",
		},
		{
			Code: "professional", Name: "专业版", Price: "25", Asset: "USDT", Days: 30,
			WalletLimit: 20, RuleLimit: 100, GroupLimit: 3,
			AllowedRuleTypes: []string{
				"balance_change", "incoming", "outgoing",
				"balance_threshold", "balance_threshold_high",
				"approval_change", "address_interaction",
			},
			AllowedRules: ruleTypes, PrivateNotification: true, GroupNotification: true,
			DailySummary: true, SummaryTargets: []string{"private", "group"},
			Description: "适合项目方和社群：20 个钱包、100 条规则，支持群通知、指定地址交互提醒和群每日摘要。",
		},
	}
	if !reflect.DeepEqual(planPayload.RuleTypes, ruleTypes) {
		t.Fatalf("rule type contract changed:\ngot:  %#v\nwant: %#v", planPayload.RuleTypes, ruleTypes)
	}
	if !reflect.DeepEqual(planPayload.Plans, expectedPlans) {
		t.Fatalf("plan contract changed:\ngot:  %#v\nwant: %#v", planPayload.Plans, expectedPlans)
	}

	var chains []chain.Profile
	getJSON(t, handler, "/api/chains", &chains)
	expectedChains := []chain.Profile{
		{Key: "bsc", ChainID: 56, ChainIDHex: "0x38", Name: "BNB Chain", NativeSymbol: "BNB"},
		{Key: "ethereum", ChainID: 1, ChainIDHex: "0x1", Name: "Ethereum", NativeSymbol: "ETH"},
		{Key: "base", ChainID: 8453, ChainIDHex: "0x2105", Name: "Base", NativeSymbol: "ETH"},
		{Key: "polygon", ChainID: 137, ChainIDHex: "0x89", Name: "Polygon", NativeSymbol: "POL"},
		{Key: "arbitrum", ChainID: 42161, ChainIDHex: "0xa4b1", Name: "Arbitrum", NativeSymbol: "ETH"},
		{Key: "optimism", ChainID: 10, ChainIDHex: "0xa", Name: "Optimism", NativeSymbol: "ETH"},
	}
	if !reflect.DeepEqual(chains, expectedChains) {
		t.Fatalf("chain contract changed:\ngot:  %#v\nwant: %#v", chains, expectedChains)
	}
}

func expectedRuleTypes() []plans.RuleType {
	return []plans.RuleType{
		{Code: "balance_change", Label: "余额变化", Description: "余额发生任意变化时推送通知。"},
		{Code: "incoming", Label: "转入提醒", Description: "余额增加并达到阈值时推送通知。"},
		{Code: "outgoing", Label: "转出提醒", Description: "余额减少并达到阈值时推送通知。"},
		{Code: "balance_threshold", Label: "低余额阈值", Description: "余额首次达到或低于阈值时提醒一次；持续低于不重复，恢复后再次跌破会重新提醒。"},
		{Code: "balance_threshold_high", Label: "高余额阈值", Description: "余额首次达到或高于阈值时提醒一次；持续高于不重复，回落后再次突破会重新提醒。"},
		{Code: "approval_change", Label: "授权 / Approve 监控", Description: "钱包对指定合约的代币授权额度发生变化时推送通知。"},
		{Code: "address_interaction", Label: "指定地址交互提醒", Description: "钱包与指定地址或合约发生交互时推送通知。"},
	}
}

func getJSON(t *testing.T, handler http.Handler, path string, target any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", path, recorder.Code, recorder.Body)
	}
	if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
		t.Fatalf("decode GET %s: %v", path, err)
	}
}
