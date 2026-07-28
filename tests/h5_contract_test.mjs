import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const html = fs.readFileSync(path.join(root, "static", "index.html"), "utf8");
const app = fs.readFileSync(path.join(root, "static", "app.js"), "utf8");
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
assert.ok(
  app.includes('currentPlan()?.market_pool_mode === "multiple"') &&
    app.includes('t("marketMultiPoolProfessionalOnly")'),
  "market wizard must disable multi-pool scope outside Professional",
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
for (const filterID of [
  "marketEventChainFilter",
  "marketEventTypeFilter",
  "marketEventPoolFilter",
  "marketEventAddressFilter",
]) {
  assert.ok(htmlIDs.has(filterID), `market event filter is missing: ${filterID}`);
}
for (const queryKey of ["chain_key", "event_type", "pool_id", "address"]) {
  assert.ok(
    app.includes(`query.set("${queryKey}"`),
    `market event request is missing query filter: ${queryKey}`,
  );
}

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
for (const key of translationKeys) {
  assert.ok(Object.hasOwn(translations.zh, key), `Chinese translation is missing: ${key}`);
  assert.ok(Object.hasOwn(translations.en, key), `English translation is missing: ${key}`);
}

const i18nScript = html.indexOf('<script src="/static/i18n.js"></script>');
const timeScript = html.indexOf('<script src="/static/time.js"></script>');
const appScript = html.indexOf('<script src="/static/app.js"></script>');
assert.ok(
  i18nScript >= 0 && timeScript > i18nScript && appScript > timeScript,
  "i18n.js and time.js must load before app.js",
);

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
