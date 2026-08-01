package notificationdetail

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

var transactionExplorerBases = map[string]string{
	"bsc":      "https://bscscan.com/tx/",
	"ethereum": "https://etherscan.io/tx/",
	"base":     "https://basescan.org/tx/",
	"polygon":  "https://polygonscan.com/tx/",
	"arbitrum": "https://arbiscan.io/tx/",
	"optimism": "https://optimistic.etherscan.io/tx/",
}

func detailValuesAndLinks(
	object map[string]any,
	language string,
	group bool,
) ([]CopyValue, []Link) {
	values := make([]CopyValue, 0)
	links := make([]Link, 0)
	seenValues := map[string]struct{}{}
	seenLinks := map[string]struct{}{}
	defaultChain := firstDetailChainKey(object)
	collectDetailValues(
		object,
		"",
		defaultChain,
		language,
		group,
		seenValues,
		seenLinks,
		&values,
		&links,
	)
	return values, links
}

func collectDetailValues(
	value any,
	path string,
	inheritedChain string,
	language string,
	group bool,
	seenValues map[string]struct{},
	seenLinks map[string]struct{},
	values *[]CopyValue,
	links *[]Link,
) {
	switch typed := value.(type) {
	case map[string]any:
		chainKey := inheritedChain
		if value, ok := typed["chain_key"].(string); ok && strings.TrimSpace(value) != "" {
			chainKey = chain.NormalizeChainKey(value, "")
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := typed[key]
			itemPath := key
			if path != "" {
				itemPath = path + "." + key
			}
			if text, ok := item.(string); ok {
				if hash, err := chain.ValidateTransactionHash(text); isTransactionHashKey(key) && err == nil {
					addCopyValue(
						"transaction_hash", copyLabel("transaction_hash", language),
						hash, itemPath, seenValues, values,
					)
					if explorerURL := transactionExplorerURL(chainKey, hash); explorerURL != "" {
						if _, exists := seenLinks[explorerURL]; !exists {
							seenLinks[explorerURL] = struct{}{}
							*links = append(*links, Link{
								Kind: "transaction", Label: transactionLinkLabel(language),
								URL: explorerURL, Value: hash, ChainKey: chainKey,
							})
						}
					}
					continue
				}
				if address, err := chain.ValidateAddress(text); err == nil {
					kind := addressKind(key)
					if !group || groupPublicAddressKind(kind) {
						addCopyValue(
							kind, copyLabel(kind, language), address, itemPath,
							seenValues, values,
						)
					}
				}
			}
			collectDetailValues(
				item, itemPath, chainKey, language, group,
				seenValues, seenLinks, values, links,
			)
		}
	case []any:
		for index, item := range typed {
			itemPath := fmt.Sprintf("%s[%d]", path, index)
			collectDetailValues(
				item, itemPath, inheritedChain, language, group,
				seenValues, seenLinks, values, links,
			)
		}
	}
}

func isTransactionHashKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "transaction_hash", "tx_hash":
		return true
	default:
		return false
	}
}

func addCopyValue(
	kind string,
	label string,
	value string,
	path string,
	seen map[string]struct{},
	values *[]CopyValue,
) {
	key := kind + "\x00" + value
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*values = append(*values, CopyValue{
		Kind: kind, Label: label, Value: value, Path: path,
	})
}

func addressKind(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(key, "wallet"):
		return "wallet_address"
	case strings.Contains(key, "holder"):
		return "holder_address"
	case strings.Contains(key, "target"):
		return "target_address"
	case strings.Contains(key, "pool"):
		return "pool_address"
	case strings.Contains(key, "factory"):
		return "factory_address"
	case strings.Contains(key, "token"):
		return "token_address"
	case strings.Contains(key, "contract") || key == "spender":
		return "contract_address"
	default:
		return "address"
	}
}

func groupPublicAddressKind(kind string) bool {
	switch kind {
	case "token_address", "pool_address", "factory_address", "contract_address":
		return true
	default:
		return false
	}
}

func copyLabel(kind, language string) string {
	english := language == "en"
	labels := map[string][2]string{
		"transaction_hash": {"交易哈希", "Transaction hash"},
		"wallet_address":   {"钱包地址", "Wallet address"},
		"holder_address":   {"持仓地址", "Holder address"},
		"target_address":   {"目标地址", "Target address"},
		"pool_address":     {"交易池地址", "Pool address"},
		"factory_address":  {"工厂合约", "Factory contract"},
		"token_address":    {"代币合约", "Token contract"},
		"contract_address": {"合约地址", "Contract address"},
		"address":          {"地址", "Address"},
	}
	value := labels[kind]
	if english {
		return value[1]
	}
	return value[0]
}

func transactionLinkLabel(language string) string {
	if language == "en" {
		return "View transaction"
	}
	return "查看交易"
}

func transactionExplorerURL(chainKey, transactionHash string) string {
	if strings.TrimSpace(chainKey) == "" {
		return ""
	}
	hash, err := chain.ValidateTransactionHash(transactionHash)
	if err != nil {
		return ""
	}
	base := transactionExplorerBases[chain.NormalizeChainKey(chainKey, "")]
	if base == "" {
		return ""
	}
	return base + hash
}

func firstDetailChainKey(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if chainKey, ok := typed["chain_key"].(string); ok && strings.TrimSpace(chainKey) != "" {
			return chain.NormalizeChainKey(chainKey, "")
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if result := firstDetailChainKey(typed[key]); result != "" {
				return result
			}
		}
	case []any:
		for _, item := range typed {
			if result := firstDetailChainKey(item); result != "" {
				return result
			}
		}
	}
	return ""
}

func managementLink(
	publicAppURL string,
	notificationKind string,
	ruleID *int64,
	data map[string]any,
	language string,
) *Link {
	query := url.Values{}
	fragment := ""
	kind := "manage_rule"
	if ruleID != nil && *ruleID > 0 {
		query.Set("rule_id", strconv.FormatInt(*ruleID, 10))
	}
	switch notificationKind {
	case store.NotificationKindAddressRealtime, store.NotificationKindAddressStage:
		if ruleID == nil {
			return nil
		}
		query.Set("rule_type", "address")
		fragment = "activeRulesSection"
	case store.NotificationKindAddressCombination:
		if ruleID == nil {
			return nil
		}
		query.Del("rule_id")
		query.Set("combination_id", strconv.FormatInt(*ruleID, 10))
		query.Set("rule_type", "address_combination")
		fragment = "activeRulesSection"
	case store.NotificationKindMarketRealtime, store.NotificationKindMarketStage:
		if ruleID == nil {
			return nil
		}
		query.Set("rule_type", "market")
		if projectID := marketProjectID(data); projectID > 0 {
			query.Set("project_id", strconv.FormatInt(projectID, 10))
		}
		fragment = "marketProjectsSection"
	case store.NotificationKindMarketCombination:
		if ruleID == nil {
			return nil
		}
		query.Del("rule_id")
		query.Set("combination_id", strconv.FormatInt(*ruleID, 10))
		query.Set("rule_type", "market_combination")
		fragment = "marketProjectsSection"
	case store.NotificationKindDailySummary:
		query = url.Values{}
		fragment = "summary"
		kind = "manage_summary"
	default:
		return nil
	}
	label := "管理规则"
	if kind == "manage_summary" {
		label = "管理日报"
	}
	if language == "en" {
		label = "Manage rule"
		if kind == "manage_summary" {
			label = "Manage daily summary"
		}
	}
	return &Link{
		Kind:  kind,
		Label: label,
		URL:   buildAppURL(publicAppURL, query, fragment),
	}
}

func buildAppURL(publicAppURL string, query url.Values, fragment string) string {
	base := strings.TrimSpace(publicAppURL)
	var target *url.URL
	if base == "" {
		target = &url.URL{Path: "/"}
	} else if parsed, err := url.Parse(base); err == nil {
		target = parsed
	} else {
		target = &url.URL{Path: "/"}
	}
	target.RawQuery = query.Encode()
	target.Fragment = fragment
	return target.String()
}

func marketProjectID(data map[string]any) int64 {
	delivery, _ := data["delivery"].(map[string]any)
	project, _ := delivery["project"].(map[string]any)
	switch value := project["id"].(type) {
	case json.Number:
		result, _ := value.Int64()
		return result
	case float64:
		return int64(value)
	case string:
		result, _ := strconv.ParseInt(value, 10, 64)
		return result
	default:
		return 0
	}
}
