import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const app = fs.readFileSync(path.join(root, "static/app.js"), "utf8");
const i18n = fs.readFileSync(path.join(root, "static/i18n.js"), "utf8");
let scenarios = 0;

function sourceFunction(name) {
  const start = app.search(new RegExp(`^(?:async )?function ${name}\\(`, "m"));
  assert.ok(start >= 0, `Missing production function: ${name}`);
  const tail = app.slice(start);
  const next = tail.slice(1).search(/^(?:(?:async )?function |const )/m);
  assert.ok(next >= 0, `Missing function boundary: ${name}`);
  return tail.slice(0, next + 1);
}

const definition = { code: "market_price_above", allowed: true, default_threshold: "1", default_unit: "usd", units: ["usd"], default_window_minutes: 0 };
const recommendation = { rule_type: definition.code, sensitivity: "balanced", threshold: "2.1", threshold_unit: "usd", window_minutes: 0, cooldown_seconds: 300 };
const quality = (status = "cached", ageSeconds = 5) => ({ status, fetched_at: new Date(Date.now() - ageSeconds * 1000).toISOString() });
const quoteFailure = { status: 503, data: { code: "quote_unavailable", detail: "unavailable" } };

function harness({ language = "zh", responses = [] } = {}) {
  const nodes = new Map();
  const calls = [], toasts = [];
  const effects = { resets: 0, renders: 0, eventReads: 0 };
  function node(id) {
    if (!nodes.has(id)) nodes.set(id, {
      value: "", textContent: "", hidden: false, disabled: false, checked: false,
      options: [{ value: "usd" }], dataset: {},
      classList: { toggle() {} }, setAttribute() {},
    });
    return nodes.get(id);
  }
  const state = {
    uiLanguage: language, marketDetail: { project: { id: 7 } },
    marketRecommendations: [recommendation], marketRecommendationUpdatedAt: "",
    marketRecommendationQuoteQuality: quality(), marketRecommendationQuoteMaxAgeSeconds: 120,
    marketRecommendationError: "", marketRecommendationLoading: false,
    marketWizard: {
      selectedAsset: {}, poolResult: { groups: [] }, recommendations: [recommendation], recommendationUpdatedAt: "",
      recommendationQuoteQuality: quality(), recommendationQuoteMaxAgeSeconds: 120,
      recommendationError: "", recommendationLoading: false, busy: false,
    },
  };
  const context = vm.createContext({
    window: {}, state, $: node,
    MARKET_EVENT_ONLY_RULES: new Set(["market_new_pool", "market_four_meme_migration"]),
    MARKET_REPEAT_WHILE_ACTIVE_RULES: new Set(),
    MARKET_REPEAT_WHILE_ACTIVE_LABEL_KEYS: {}, MARKET_REPEAT_WHILE_ACTIVE_HELP_KEYS: {},
    marketRuleDefinition: () => definition,
    updateMarketThresholdHelp() {},
    marketDate: (value) => new Date(value).toISOString(),
    marketProjectIsExpiredFrozen: () => false,
    marketDetailRecommendationInput: () => [{ chain_key: "bsc", token_address: "fixture", pool_addresses: ["fixture-pool"] }],
    marketWizardRecommendationInput: () => [{ chain_key: "bsc", token_address: "fixture", pool_addresses: ["fixture-pool"] }],
    resetConnectionState: () => { effects.resets += 1; },
    loadMarketEvents: async () => { effects.eventReads += 1; },
    renderMarketDetail: () => { effects.renders += 1; },
    renderMarketWizardSummary() {}, toast: (value) => toasts.push(value),
    fetch: async (requestPath, options) => {
      calls.push({ path: requestPath, options });
      assert.ok(calls.length <= responses.length, "Unexpected request or background polling");
      const response = responses[calls.length - 1];
      if (response instanceof Error) throw response;
      return { ok: response.status >= 200 && response.status < 300, status: response.status, json: async () => response.data };
    },
  });
  vm.runInContext(i18n, context);
  context.t = (key, params = {}) => {
    const text = context.window.H5_I18N[language][key];
    assert.equal(typeof text, "string", `Missing translation ${key}`);
    return text.replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? ""));
  };
  for (const name of [
    "localizedApiError", "api", "marketQuoteObservationTime", "marketQuoteAvailable", "marketQuoteLabel",
    "marketMoney", "marketSnapshotTotal",
    "selectedMarketRecommendation", "marketRecommendationControls", "marketRecommendationState",
    "marketRecommendationCanApply", "formatRecommendationTime", "applyMarketRecommendationEditor",
    "marketWindowCapability", "marketWindowIssue", "marketRuleParameterIssue", "marketRuleEditorParametersValid", "updateMarketWindowControl",
    "loadMarketProjectExtras", "updateMarketRecommendationEditor", "refreshMarketRecommendations", "createMarketRule", "createMarketProject",
  ]) vm.runInContext(sourceFunction(name), context, { filename: `app.js:${name}` });
  for (const mode of ["detail", "wizard"]) {
    const controls = context.marketRecommendationControls(mode);
    controls.sensitivity.value = "balanced";
    controls.threshold.value = "saved-value";
    controls.unit.value = "usd";
    controls.window.value = "17";
    controls.cooldown.value = "321";
  }
  context.renderMarketRuleEditor = () => context.applyMarketRecommendationEditor("detail", definition, true);
  context.renderMarketWizardRuleEditor = () => context.applyMarketRecommendationEditor("wizard", definition, true);
  return { context, state, calls, nodes, toasts, effects, node };
}

for (const language of ["zh", "en"]) {
  const h = harness({ language });
  const now = Date.now();
  assert.equal(h.context.marketQuoteAvailable(quality("live"), 120, now), true);
  assert.equal(h.context.marketQuoteAvailable(quality("cached"), 120, now), true);
  assert.equal(h.context.marketQuoteAvailable(quality("stale_fallback"), 120, now), false);
  assert.equal(h.context.marketQuoteAvailable(quality("live", 121), 120, now), false);
  assert.equal(h.context.marketQuoteAvailable(quality("live", -10), 120, now), false);
  assert.equal(h.context.marketQuoteAvailable({}, 120, now), false);
  assert.equal(h.context.marketQuoteAvailable(quality(), 0, now), false);
  assert.equal(h.context.marketQuoteAvailable(quality(), Infinity, now), false);
  assert.equal(h.context.marketQuoteAvailable(quality(), Number.MAX_VALUE, now), false);
  const malformed = { ...quality(), source_timestamp: "not-a-timestamp" };
  assert.equal(h.context.marketQuoteAvailable(malformed, 120, now), false);
  assert.equal(h.context.marketQuoteObservationTime(malformed), "");
  assert.equal(h.context.marketQuoteAvailable({ ...quality(), source_timestamp: "0001-01-01T00:00:00Z" }, 120, now), true);
  const source = { ...quality(), source_timestamp: new Date(now - 121000).toISOString() };
  assert.equal(h.context.marketQuoteAvailable(source, 120, now), false);
  assert.equal(h.context.marketQuoteObservationTime(source), source.source_timestamp);
  assert.ok(h.context.marketQuoteLabel(source, 120, now).includes(h.context.t("marketQuoteStale")));
  assert.ok(h.context.marketQuoteLabel(quality("stale_fallback"), 120, now).includes(h.context.t("marketQuoteFallback")));
  assert.equal(h.context.marketQuoteLabel({}, 120, now), h.context.t("marketQuoteUnknown"));
  scenarios += 1;

  const failed = harness({ language, responses: [quoteFailure] });
  await failed.context.loadMarketProjectExtras(7);
  assert.equal(failed.calls[0].path, "/api/market/projects/7/recommendations", "Only automatic existing-project loading uses stored historical samples");
  assert.equal(failed.calls[0].options.method || "GET", "GET");
  assert.equal(failed.effects.renders, 1);
  assert.equal(failed.effects.eventReads, 1);
  assert.equal(failed.state.marketRecommendationError, "quote_unavailable");
  assert.equal(failed.context.marketRecommendationCanApply("detail", definition), false);
  assert.equal(failed.node("marketThresholdInput").value, "saved-value");
  scenarios += 1;

  const unauthorized = harness({ language, responses: [{ status: 401, data: { code: "quote_unavailable" } }] });
  await assert.rejects(unauthorized.context.loadMarketProjectExtras(7), (err) => err.status === 401 && err.code === undefined);
  assert.equal(unauthorized.effects.resets, 1);
  assert.equal(unauthorized.effects.renders, 0);
  scenarios += 1;

  const refresh = harness({ language, responses: [quoteFailure] });
  refresh.node("marketRulePoolSelect").value = "saved-pool";
  assert.equal(await refresh.context.refreshMarketRecommendations("detail"), false);
  assert.equal(refresh.calls[0].path, "/api/market/recommendations/preview", "Detail manual refresh keeps the live-quote preview formula");
  assert.equal(refresh.calls[0].options.method, "POST");
  assert.equal(refresh.node("marketSensitivitySelect").value, "balanced");
  assert.equal(refresh.node("marketThresholdInput").value, "saved-value");
  assert.equal(refresh.node("marketWindowInput").value, "17");
  assert.equal(refresh.node("marketCooldownInput").value, "321");
  assert.equal(refresh.node("marketRulePoolSelect").value, "saved-pool");
  assert.equal(refresh.node("marketRecommendationStatus").textContent, refresh.context.t("marketRecommendationUnavailable"));
  scenarios += 1;
}

{
  const h = harness({ responses: [{ status: 200, data: { recommendations: [recommendation], quote_quality: quality(), quote_max_age_seconds: 120 } }] });
  assert.equal(await h.context.refreshMarketRecommendations("wizard"), true);
  assert.equal(h.calls.length, 1);
  assert.equal(h.calls[0].path, "/api/market/recommendations/preview", "The new-project wizard keeps preview recommendations without stored history");
  assert.equal(h.calls[0].options.method, "POST");
  assert.ok(Array.isArray(JSON.parse(h.calls[0].options.body).deployments));
  scenarios += 1;
}

{
  const h = harness({ responses: [{ status: 500, data: { detail: "ordinary failure" } }] });
  await assert.rejects(h.context.loadMarketProjectExtras(7), /ordinary failure/);
  assert.equal(h.effects.renders, 0);
  scenarios += 1;
}

{
  const h = harness();
  h.state.marketRecommendationQuoteQuality = quality("cached", 121);
  h.context.applyMarketRecommendationEditor("detail", definition, true);
  assert.equal(h.node("marketThresholdInput").value, "saved-value");
  await h.context.createMarketRule({ preventDefault() {} });
  assert.equal(h.calls.length, 0, "expired recommendation submitted a rule");
  assert.equal(h.toasts.at(-1), h.context.t("marketRecommendationUnavailable"));
  h.state.marketWizard.recommendationQuoteQuality = quality("stale_fallback");
  await h.context.createMarketProject();
  assert.equal(h.calls.length, 0, "fallback recommendation created a project before validation");
  h.node("marketSensitivitySelect").value = "custom";
  h.context.applyMarketRecommendationEditor("detail", definition, true);
  assert.equal(h.context.marketRecommendationCanApply("detail", definition), true);
  assert.equal(h.node("marketThresholdInput").value, "saved-value");
  assert.equal(h.node("marketThresholdInput").disabled, false);
  scenarios += 1;
}

{
  const fetched = quality("cached");
  const h = harness({ responses: [{ status: 200, data: {
    recommendations: [recommendation], quote_quality: fetched, quote_max_age_seconds: 120,
    generated_at: "2099-01-01T00:00:00Z",
  } }] });
  assert.equal(await h.context.refreshMarketRecommendations("detail"), true);
  assert.equal(h.node("marketThresholdInput").value, "2.1");
  assert.ok(h.node("marketRecommendationStatus").textContent.includes(fetched.fetched_at));
  assert.ok(!h.node("marketRecommendationStatus").textContent.includes("2099"), "response generation time became quote observation time");
  assert.equal(h.calls.length, 1);
  scenarios += 1;
}

{
  const h = harness({ responses: [{ status: 200, data: { recommendations: [recommendation], generated_at: new Date().toISOString() } }] });
  assert.equal(await h.context.refreshMarketRecommendations("detail"), false);
  assert.equal(h.node("marketThresholdInput").value, "saved-value");
  scenarios += 1;
}

{
  const h = harness();
  for (const value of [null, undefined, "", " "]) assert.equal(h.context.marketMoney(value), "-");
  assert.equal(h.context.marketMoney("0"), "$0");
  assert.equal(h.context.marketSnapshotTotal([{ liquidity_usd: "0" }, { liquidity_usd: "2" }], "liquidity_usd"), 2);
  assert.equal(h.context.marketSnapshotTotal([{ liquidity_usd: "2" }, { liquidity_usd: null }], "liquidity_usd"), null);
  assert.equal(h.context.marketSnapshotTotal([], "liquidity_usd"), null);
  scenarios += 1;
}

console.log(`H5 quote quality OK: ${scenarios} scenarios`);
