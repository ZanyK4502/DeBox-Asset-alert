import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const html = fs.readFileSync(path.join(root, "static", "index.html"), "utf8");
const app = fs.readFileSync(path.join(root, "static", "app.js"), "utf8");
const styles = fs.readFileSync(path.join(root, "static", "styles.css"), "utf8");
const notificationDetailSource = fs.readFileSync(
  path.join(root, "static", "notification-detail.js"),
  "utf8",
);
const i18nSource = fs.readFileSync(path.join(root, "static", "i18n.js"), "utf8");
const timeSource = fs.readFileSync(path.join(root, "static", "time.js"), "utf8");

const allHTMLIDs = [...html.matchAll(/\bid="([^"]+)"/g)].map((match) => match[1]);
const htmlIDs = new Set(allHTMLIDs);
assert.equal(htmlIDs.size, allHTMLIDs.length, "H5 contains duplicate HTML ids");
const referencedIDs = new Set([...app.matchAll(/\$\("([^"]+)"\)/g)].map((match) => match[1]));
for (const id of referencedIDs) {
  assert.ok(htmlIDs.has(id), `app.js references missing HTML id: ${id}`);
}

const requiredAPIs = [
  "/api/plans",
  "/api/chains",
  "/api/auth/challenge",
  "/api/auth/verify",
  "/api/auth/session",
  "/api/auth/logout",
  "/api/subscription/current",
  "/api/payment/config",
  "/api/payment/prepare",
  "/api/payment/verify",
  "/api/debox/token",
  "/api/chain/balance",
  "/api/watch-rules",
  "/api/aggregate-events",
  "/api/notification-details/",
  "/api/subscription/summary-settings",
  "/api/notification-groups",
  "/api/market/catalog",
  "/api/market/assets/search",
  "/api/market/assets/manual-resolve",
  "/api/market/assets/verify-cross-chain",
  "/api/market/pools/discover",
  "/api/market/recommendations/preview",
  "/api/market/projects",
  "/api/market/rules/",
  "/api/market/combinations",
  "/events?",
  "/api/market/labels/",
];
for (const endpoint of requiredAPIs) {
  assert.ok(app.includes(endpoint), `H5 no longer references required API: ${endpoint}`);
}
assert.ok(
  !app.includes("/api/subscription/complimentary"),
  "H5 still references the removed complimentary subscription API",
);
for (const step of [1, 2, 3, 4]) {
  assert.ok(
    htmlIDs.has(`marketWizardStep${step}`),
    `four-step market wizard is missing step ${step}`,
  );
}
assert.match(
  html,
  /<section class="overview" data-mobile-screen="account"[\s\S]*?id="profileBox"[\s\S]*?id="subscriptionBox"[\s\S]*?<\/section>/,
  "profile and subscription status must appear in the mobile Account view",
);
assert.match(
  html,
  /<section id="summary"[^>]*data-mobile-screen="overview"/,
  "daily summary must appear in the mobile Overview view",
);
assert.match(
  html,
  /<section id="groups"[^>]*data-mobile-screen="overview"/,
  "group notifications must appear in the mobile Overview view",
);
for (const id of [
  "marketProjectsSection",
  "activeRulesSection",
  "pausedRulesWrap",
  "aggregateEventsSection",
]) {
  assert.match(
    html,
    new RegExp(`<section id="${id}"[^>]*data-mobile-screen="monitoring"`),
    `${id} must appear in the mobile Monitoring view`,
  );
}
assert.match(
  html,
  /<section id="market"[^>]*data-mobile-screen="market"/,
  "token monitoring creation must remain in the mobile Market view",
);
assert.match(
  html,
  /<section id="rules"[^>]*data-mobile-screen="address"/,
  "address monitoring creation must remain in the mobile Address view",
);
assert.ok(
  !html.includes('data-i18n="heroTitle"'),
  "the removed mobile hero message must not remain visible",
);
assert.deepEqual(
  [...html.matchAll(/\bdata-mobile-tab="([^"]+)"/g)].map((match) => match[1]),
  ["account", "market", "address", "monitoring", "overview"],
  "mobile navigation must be ordered Account, Market, Address, Monitoring, Overview",
);
assert.ok(
  app.includes('const MOBILE_VIEWS = new Set(["overview", "monitoring", "market", "address", "account"])'),
  "mobile view state must recognize the Monitoring view",
);
assert.ok(
  app.includes('setMobileView("monitoring", { restoreScroll: false, target: $("activeRulesSection") })') &&
    app.includes('setMobileView("monitoring", { restoreScroll: false, target: $("marketProjectDetail") })'),
  "new address and token monitoring must reveal their result in the Monitoring view",
);
assert.ok(
  app.includes('function storedMobileView() {\n  return "account";\n}'),
  "fresh mobile sessions must open the Account view",
);
assert.ok(
  app.includes('deployment_scope: "all"') &&
    app.includes('cooldown_scope: "chain"'),
  "market wizard must create the first rule with explicit multi-chain scopes",
);
assert.ok(
  app.includes("existingMarketProjectForCandidate(candidate)") &&
    app.includes('t("marketAlreadyCreatedDeleteFirst")'),
  "market wizard must block tokens that already have active or archived monitoring",
);
assert.ok(
  i18nSource.includes("已创建，请先删除此代币相关监控项目。"),
  "manual duplicate-token guidance is missing",
);
assert.ok(
  app.includes('t("marketProjectMetric"') &&
    app.includes("state.entitlement.market_project_count") &&
    app.includes("plan.market_project_limit"),
  "subscription status must display token usage and quota",
);
assert.deepEqual(
  [...html.matchAll(/\bdata-billing-cycle="([^"]+)"/g)].map((match) => match[1]),
  ["monthly", "quarterly", "annual"],
  "subscription checkout must keep all three billing cycles",
);
assert.ok(
  htmlIDs.has("purchaseSummary") &&
    app.includes('t("purchaseSummary"') &&
    app.includes("selectedBillingOption(plan)"),
  "subscription checkout must summarize the selected plan and billing cycle",
);
assert.ok(
  app.includes('currentPlan()?.market_pool_mode === "multiple"') &&
    app.includes('t("marketMultiPoolProfessionalOnly")'),
  "market wizard must disable multi-pool scope outside Professional",
);
assert.ok(
  app.includes("function marketPoolDiscoveryAllowed()") &&
    app.includes('currentPlan()?.market_query === true') &&
    app.includes('t("marketPoolDiscoveryPaidOnly")'),
  "free users must be blocked from identity verification and pool discovery",
);
assert.ok(
  i18nSource.includes("同币验证和交易池查询仅支持标准版和专业版") &&
    i18nSource.includes("Identity verification and pool discovery require Standard or Professional"),
  "paid-only market discovery guidance must exist in both languages",
);
assert.ok(
  styles.includes(".market-detail-goals::-webkit-scrollbar-thumb") &&
    styles.includes("#marketRulesList .list-item-actions button.compact") &&
    styles.includes("#marketCombinationsList .list-item-actions button.compact") &&
    styles.includes(".usage-help-fab"),
  "mobile market scrollbars, compact rule cards, and the help button must keep targeted styling",
);
for (const refreshID of [
  "marketWizardRecommendationRefreshBtn",
  "marketRecommendationRefreshBtn",
]) {
  assert.ok(htmlIDs.has(refreshID), `market recommendation refresh control is missing: ${refreshID}`);
}
assert.ok(
  app.includes("MARKET_EVENT_ONLY_RULES") &&
    app.includes("controls.threshold.disabled = lockRecommended") &&
    app.includes('controls.sensitivity.value === "custom"'),
  "market recommendation presets must lock generated values and preserve custom editing",
);
for (const tab of ["overview", "rules", "pools", "holders", "events"]) {
  assert.ok(
    html.includes(`data-market-detail-tab="${tab}"`) &&
      html.includes(`data-market-detail-panel="${tab}"`),
    `market project detail is missing the ${tab} tab or panel`,
  );
}
assert.ok(
  htmlIDs.has("marketLabelChainSelect") &&
    htmlIDs.has("marketHolderChainFilter") &&
    !htmlIDs.has("marketLabelTypeSelect"),
  "holder labels need a dedicated chain selector, a separate ranking filter, and no type field",
);
assert.match(
  html,
  /id="marketLabelInput"[^>]*\brequired\b/,
  "holder label input must be required",
);
assert.match(
  html,
  /id="marketCombinationNoteInput"[^>]*\brequired\b/,
  "market combination note input must be required",
);
assert.ok(
  !app.includes("data-clear-market-label") &&
    !app.includes("clearMarketLabel("),
  "holder label actions must not include the duplicate remove-label action",
);
assert.ok(
  app.includes("event.combination_notes") &&
    app.includes('t("marketEventCombinationNote")'),
  "market event history must render combination notes",
);
assert.ok(
  app.includes("data-permanently-delete-market-combination") &&
    app.includes("/api/market/combinations/${combinationId}/permanent"),
  "paused market combinations must support permanent deletion",
);
for (const filterID of [
  "marketEventChainFilter",
  "marketEventTypeFilter",
  "marketEventPoolFilter",
  "marketEventAddressFilter",
]) {
  assert.ok(htmlIDs.has(filterID), `market event filter is missing: ${filterID}`);
}
for (const queryKey of ["chain_key", "rule_type", "pool_id", "address"]) {
  assert.ok(
    app.includes(`query.set("${queryKey}"`),
    `market event request is missing query filter: ${queryKey}`,
  );
}

for (const id of [
  "notificationDetailPage",
  "notificationDetailStatus",
  "notificationDetailContent",
  "closeNotificationDetailPageBtn",
  "eventDetailBackdrop",
  "eventDetailDrawer",
  "eventDetailDrawerContent",
]) {
  assert.ok(htmlIDs.has(id), `notification detail H5 control is missing: ${id}`);
}
assert.ok(
  app.includes('new URLSearchParams(window.location.search).get("notification_id")') &&
    app.includes("NOTIFICATION_ID_PATTERN") &&
    app.includes("loadNotificationDetail"),
  "notification links must open one exact hidden H5 detail route",
);
assert.ok(
  app.includes('openEventDetailDrawer("aggregate"') &&
    app.includes('openEventDetailDrawer("market"'),
  "address and market history must open focused event drawers",
);
assert.ok(
  !app.includes("aggregateScrollFrame") && !htmlIDs.has("aggregateScrollToggleBtn"),
  "address history must not auto-scroll while the user is reading",
);

const context = { window: {} };
vm.runInNewContext(i18nSource, context, { filename: "static/i18n.js" });
const translations = context.window.H5_I18N;
assert.ok(translations?.zh && translations?.en, "Chinese and English H5 dictionaries are required");
for (const key of [
  "complimentaryActivate",
  "complimentaryAvailable",
  "complimentaryActive",
  "complimentaryConfirm",
  "complimentaryActivated",
]) {
  assert.ok(!Object.hasOwn(translations.zh, key), `Chinese legacy translation remains: ${key}`);
  assert.ok(!Object.hasOwn(translations.en, key), `English legacy translation remains: ${key}`);
}

const translationKeys = new Set();
for (const match of html.matchAll(/\bdata-i18n(?:-placeholder|-aria-label|-label)?="([^"]+)"/g)) {
  translationKeys.add(match[1]);
}
for (const match of app.matchAll(/\bt\(\s*["']([^"']+)["']/g)) {
  translationKeys.add(match[1]);
}
for (const match of notificationDetailSource.matchAll(/\bt\(\s*["']([^"']+)["']/g)) {
  translationKeys.add(match[1]);
}
for (const key of translationKeys) {
  assert.ok(Object.hasOwn(translations.zh, key), `Chinese translation is missing: ${key}`);
  assert.ok(Object.hasOwn(translations.en, key), `English translation is missing: ${key}`);
}

const i18nScript = html.search(/<script src="\/static\/i18n\.js(?:\?[^"]*)?"><\/script>/);
const timeScript = html.search(/<script src="\/static\/time\.js(?:\?[^\"]*)?"><\/script>/);
const notificationDetailScript = html.search(
  /<script src="\/static\/notification-detail\.js(?:\?[^\"]*)?"><\/script>/,
);
const appScript = html.search(/<script src="\/static\/app\.js(?:\?[^"]*)?"><\/script>/);
assert.ok(
  i18nScript >= 0 && timeScript > i18nScript &&
    notificationDetailScript > timeScript && appScript > notificationDetailScript,
  "i18n.js, time.js, and notification-detail.js must load before app.js",
);

const detailDocument = {
  createElement(tagName) {
    assert.equal(tagName, "textarea", "notification text decoding must use an inert textarea");
    return {
      value: "",
      textContent: "",
      set innerHTML(value) {
        this.value = String(value)
          .replaceAll("&amp;", "&")
          .replaceAll("&lt;", "<")
          .replaceAll("&gt;", ">")
          .replaceAll("&quot;", '"')
          .replaceAll("&#039;", "'");
        this.textContent = this.value;
      },
    };
  },
};
const detailContext = { window: {}, document: detailDocument, Date, Intl };
vm.runInNewContext(notificationDetailSource, detailContext, {
  filename: "static/notification-detail.js",
});
const notificationRenderer = detailContext.window.H5_NOTIFICATION_DETAIL;
assert.equal(typeof notificationRenderer?.render, "function", "notification detail renderer is required");
assert.equal(
  notificationRenderer.plainNotificationText("<b>提醒</b><br>&amp; complete"),
  "提醒\n& complete",
  "notification HTML must become safe, readable plain text",
);

const detailT = (key, values = {}) => String(translations.zh[key] ?? key)
  .replace(/\{([a-zA-Z0-9_]+)\}/g, (_, name) => String(values[name] ?? ""));
const detailOptions = {
  t: detailT,
  language: "zh",
  ruleLabel: (code) => translations.rules.zh[code] || code,
  chainName: (key) => ({ bsc: "BNB Chain", base: "Base" })[key] || key,
  eventLabel: (type) => ({ buy: "买入", sell: "卖出" })[type] || type,
};
const detailBase = {
  notification_id: "nd_0123456789abcdef0123456789abcdef01234567",
  access_scope: "private",
  language: "zh",
  label: "项目金库",
  rule: { type: "incoming", name: "转入提醒", threshold: "100" },
  actual_value: "188",
  notification_text: "<b>重要提醒</b><br>实际值 188",
  copy_values: [{ label: "交易哈希", value: "0xabc" }],
  links: [{ kind: "manage_rule", label: "管理规则", url: "/#rules" }],
  created_at: "2026-07-31T10:00:00Z",
  expires_at: "2026-08-30T10:00:00Z",
};
const detailFixtures = {
  address_realtime: {
    rule: { chain_key: "bsc" },
    alert_event: { previous_value: "90", current_value: "188", created_at: "2026-07-31T10:00:00Z" },
    token_symbol: "USDT",
  },
  address_stage: {
    stage: { total_trigger_count: 3, trigger_count_threshold: 3, window_starts_at: "2026-07-31T09:00:00Z", window_ends_at: "2026-07-31T10:00:00Z", events: [{ current_value: "188", occurred_at: "2026-07-31T10:00:00Z" }] },
  },
  address_combination: {
    combination: { window_starts_at: "2026-07-31T09:00:00Z", window_ends_at: "2026-07-31T10:00:00Z", member_progress: [{ rule_type: "incoming", trigger_count: 2, required_trigger_count: 2, events: [] }] },
  },
  market_realtime: {
    delivery: { project: { token_name: "DeBox", token_symbol: "BOX", chain_key: "bsc" }, event: { event_type: "buy", chain_key: "bsc", price_usd: "0.02", occurred_at: "2026-07-31T10:00:00Z" }, current_value: "25", pool: { protocol: "PancakeSwap", protocol_version: "v3" } },
  },
  market_stage: {
    delivery: { project: { token_name: "DeBox", token_symbol: "BOX", chain_key: "bsc" }, rule: { trigger_count_threshold: 3 }, trigger_count: 3, stage_events: [{ event: { event_type: "buy", chain_key: "bsc", occurred_at: "2026-07-31T10:00:00Z" } }], starts_at: "2026-07-31T09:00:00Z", ends_at: "2026-07-31T10:00:00Z" },
  },
  market_combination: {
    delivery: { starts_at: "2026-07-31T09:00:00Z", ends_at: "2026-07-31T10:00:00Z", combination_members: [{ rule_type: "market_large_buy", trigger_count: 2, required_trigger_count: 2, watch_events: [], market_events: [] }] },
  },
  daily_summary: {
    statistics: { event_count: 2, market_event_count: 3, address_risk_event_count: 1, market_anomaly_count: 1, failed_notification_count: 0, market_failed_notification_count: 0 },
    period_start: "2026-07-30T10:00:00Z",
    period_end: "2026-07-31T10:00:00Z",
    address_events: [],
    market_events: [],
    market_project_chain_summaries: [{ token_name: "DeBox", token_symbol: "BOX", chain_key: "bsc", start_price_usd: "0.018", end_price_usd: "0.02", trade_volume_usd: "180000", buy_count: 30, sell_count: 18, large_trade_count: 2 }],
  },
};
for (const [notificationKind, data] of Object.entries(detailFixtures)) {
  const rendered = notificationRenderer.render(
    { ...detailBase, notification_kind: notificationKind, data },
    detailOptions,
  );
  assert.match(rendered, /notification-detail-hero/, `${notificationKind} must render a detail hero`);
  assert.ok(!rendered.includes("<script"), `${notificationKind} rendered executable notification HTML`);
  assert.ok(!rendered.includes("undefined"), `${notificationKind} rendered an undefined value`);
}

const combinationRegression = notificationRenderer.render({
  ...detailBase,
  notification_kind: "market_combination",
  rule: {
    type: "market_combination",
    name: translations.zh.marketCombinationDetail,
    threshold: "market_large_buy>=2@10000;market_liquidity_drop>=1@15%",
  },
  actual_value: "market_large_buy=2;market_liquidity_drop=1",
  data: {
    delivery: {
      combination_members: [
        { rule_type: "market_large_buy", trigger_count: 2, required_trigger_count: 2, market_rule: { rule_type: "market_large_buy", threshold_value: "10000", threshold_unit: "usd" } },
        { rule_type: "market_liquidity_drop", trigger_count: 1, required_trigger_count: 1, market_rule: { rule_type: "market_liquidity_drop", threshold_value: "15", threshold_unit: "percent" } },
      ],
    },
  },
}, detailOptions);
assert.ok(
  !combinationRegression.includes("market_large_buy=2") &&
    !combinationRegression.includes("market_liquidity_drop=1"),
  "combination details must not expose internal progress codes",
);
assert.ok(
  combinationRegression.includes("$10,000") && combinationRegression.includes("15%"),
  "market combination member conditions must use readable units",
);

const dailyRegression = notificationRenderer.render(
  { ...detailBase, notification_kind: "daily_summary", data: detailFixtures.daily_summary },
  detailOptions,
);
for (const label of [translations.zh.notificationFailed, translations.zh.detailMarketNotificationFailures]) {
  const labelIndex = dailyRegression.indexOf(`<span>${label}</span>`);
  const cardIndex = dailyRegression.lastIndexOf("notification-detail-metric", labelIndex);
  assert.ok(labelIndex > cardIndex && !dailyRegression.slice(cardIndex, labelIndex).includes("warning"),
    `zero-value failure metric must not be marked as warning: ${label}`);
}

const timeContext = { window: { Date, Intl } };
vm.runInNewContext(timeSource, timeContext, { filename: "static/time.js" });
const formatExpiryDate = timeContext.window.H5_TIME?.formatExpiryDate;
assert.equal(typeof formatExpiryDate, "function", "H5 expiry formatter is required");
assert.equal(
  formatExpiryDate("2026-08-20T12:00:00Z", "zh", "Asia/Shanghai"),
  "2026-08-20 20:00:00（GMT+8）",
  "H5 expiry must use the requested local time zone",
);

const fallbackContext = { window: { Date } };
vm.runInNewContext(timeSource, fallbackContext, { filename: "static/time.js" });
assert.equal(
  fallbackContext.window.H5_TIME.formatExpiryDate("2026-08-20T12:00:00Z", "en"),
  "2026-08-20 12:00:00 (UTC)",
  "H5 expiry must fall back to UTC when time-zone formatting is unavailable",
);

console.log(
  `H5 contract OK: ${referencedIDs.size} DOM references, ${requiredAPIs.length} APIs, ${translationKeys.size} translation keys`,
);
