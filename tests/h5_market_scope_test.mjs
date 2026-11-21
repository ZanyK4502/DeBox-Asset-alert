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
  assert.ok(start >= 0, `Missing production function ${name}`);
  const tail = app.slice(start), next = tail.slice(1).search(/^(?:(?:async )?function |const )/m);
  assert.ok(next >= 0, `Missing production boundary ${name}`);
  return tail.slice(0, next + 1);
}
function harness(language = "zh", overrides = {}) {
  const plan = { code: "professional", rule_limit: 10, interaction_rule_limit: 1, group_limit: 5,
    market_query: true, market_project_limit: 5, market_pool_limit: 5,
    market_pool_mode: "multiple", allowed_market_chain_keys: ["bsc"], ...overrides };
  const chains = [{ key: "bsc", name: "BNB Smart Chain" }, { key: "ethereum", name: "Ethereum" }, { key: "base", name: "Base" }];
  const state = { uiLanguage: language, deboxUserId: "fixture-user", chains,
    entitlement: { plan, market_project_count: 0 },
    marketCatalog: { plan, supported_chains: chains, monitoring_supported_chains: [chains[0]] },
    marketExpandedPoolChains: new Set(["bsc", "ethereum"]),
    marketWizard: { busy: false, selectedChains: new Set(["bsc", "ethereum"]), poolSelections: {},
      selectedAsset: { canonical_asset_id: "fixture", identity_source: "local", deployments: [
        { chain_key: "bsc", contract_address: "bsc-token" }, { chain_key: "ethereum", contract_address: "eth-token" }] },
      poolResult: { groups: [{ chain_key: "bsc" }, { chain_key: "ethereum" }] } } };
  const calls = [], toasts = [], nodes = new Map();
  const effects = { resets: 0, renders: 0 };
  const node = (id) => {
    if (!nodes.has(id)) nodes.set(id, { innerHTML: "", textContent: "", value: "custom", hidden: false,
      checked: false, disabled: false, dataset: {}, querySelectorAll: () => [], scrollIntoView() {},
      classList: { add() {}, toggle() {} }, setAttribute() {} });
    return nodes.get(id);
  };
  let respond = (requestPath) => ({ status: 200, data: requestPath === "/api/market/projects/7/rules" ? {} : { project: { id: 7, status: "active" } } });
  const context = vm.createContext({ window: {}, state, $: node, document: { querySelectorAll: () => [] },
    fetch: async (requestPath, options) => {
      calls.push({ path: requestPath, options });
      const response = respond(requestPath, options);
      return { ok: response.status >= 200 && response.status < 300, status: response.status, json: async () => response.data };
    }, toast: (message) => toasts.push(message), resetConnectionState: () => { effects.resets += 1; },
    renderMarketWizardPools: () => { effects.renders += 1; }, renderMarketWizard: () => {},
    renderMarketWizardRuleEditor: () => {}, renderMarketDetail: () => {},
    setMarketWizardBusy: (busy) => { state.marketWizard.busy = busy; }, setMarketVerifyLoading: () => {}, setMarketCreateLoading: () => {},
    marketRuleDefinition: () => ({ code: "market_price_above", allowed: true }),
    marketRecommendationCanApply: () => true, marketRuleEditorParametersValid: () => true,
    loadMarketContext: async () => {}, loadMarketProjectExtras: async () => {}, resetMarketWizard: () => {},
    freshMarketEventFilters: () => ({}), isMobileShell: () => false,
    chainLogoSrc: () => "", shortAddress: (value) => value, marketMoney: (value) => String(value ?? "-"),
    marketQuoteLabel: () => "quote time",
  });
  vm.runInContext(i18n, context); context.I18N = context.window.H5_I18N;
  for (const name of ["t", "quotaNumber", "localizedPlan", "localizedApiError", "api", "currentPlan", "marketChainName", "escapeHtml",
    "marketPoolDiscoveryAllowed", "marketScopePlan", "marketMonitoringChainKeys", "marketMonitoringChainAllowed", "marketMonitoringScopeText",
    "marketWizardMonitoringGroups", "marketWizardSelectedPoolCount", "marketWizardScopeError", "marketPoolSelection", "marketWizardPoolsReady",
    "updateWizardPoolSelection", "selectedWizardDeployments", "verifyAndDiscoverMarketPools", "marketWizardRecommendationInput",
    "createMarketProject", "marketPoolUpdateScopeError", "updateMarketPool", "renderMarketProjectPools", "renderMarketSelectedAsset", "renderWizardPoolCard"]) {
    vm.runInContext(sourceFunction(name), context, { filename: `app.js:${name}` });
  }
  return { context, state, plan, chains, node, calls, toasts, effects, respond: (fn) => { respond = fn; } };
}
function select(h, chain, address, checked = true, primary = false) {
  h.context.updateWizardPoolSelection({ checked, dataset: { chain, marketWizardPool: address } }, primary);
}

for (const language of ["zh", "en"]) {
  {
    const h = harness(language);
    assert.deepEqual(Array.from(h.context.marketMonitoringChainKeys()), ["bsc"]);
    assert.equal(h.state.chains.length, 3);
    assert.equal(h.state.marketCatalog.supported_chains.length, 3, "Monitoring restrictions must not rewrite query chains");
    assert.match(h.context.marketMonitoringScopeText(), /BNB Smart Chain/);
    assert.match(h.context.marketMonitoringScopeText(), language === "zh" ? /最多 5 个项目，每个项目合计最多 5/ : /5 projects and 5 selected pools in total per project/);
    assert.equal(h.context.marketPoolDiscoveryAllowed(), true);
    h.context.renderMarketSelectedAsset();
    assert.ok(h.node("marketWizardDeployments").innerHTML.includes("eth-token"));
    assert.ok(h.node("marketWizardDeployments").innerHTML.includes(h.context.t("marketQueryChainOnly")));
    assert.equal(h.context.selectedWizardDeployments().length, 2, "Queries retain all explicitly chosen chains");
    scenarios += 1;
  }
  {
    const h = harness(language);
    h.respond((requestPath) => ({ status: 200, data: requestPath.includes("/discover") ? { groups: [
      { chain_key: "bsc", full_monitoring_pools: [{ pair: { pairAddress: "pool-a" } }] },
      { chain_key: "ethereum", full_monitoring_pools: [{ pair: { pairAddress: "eth-pool" } }] }] } : {} }));
    await h.context.verifyAndDiscoverMarketPools();
    const queried = JSON.parse(h.calls.find((call) => call.path.endsWith("/discover")).options.body).deployments;
    assert.deepEqual(queried.map((item) => item.chain_key), ["bsc", "ethereum"]);
    assert.equal(h.context.marketPoolSelection("ethereum").selected.size, 0);
    assert.equal(h.context.marketPoolSelection("bsc").selected.size, 1);
    assert.equal(h.context.marketWizardPoolsReady(), true);
    assert.deepEqual(Array.from(h.context.marketWizardRecommendationInput(), (item) => item.chain_key), ["bsc"]);
    h.respond((requestPath) => ({ status: 200, data: requestPath.endsWith("/rules") ? {} : { project: { id: 7, status: "active" } } }));
    h.calls.length = 0;
    await h.context.createMarketProject();
    const payload = JSON.parse(h.calls[0].options.body);
    assert.deepEqual(payload.deployments.map((item) => item.chain_key), ["bsc"]);
    assert.deepEqual(payload.deployments[0].pool_addresses, ["pool-a"]);
    assert.equal(h.state.chains.length, 3);
    scenarios += 1;
  }
  {
    const h = harness(language);
    for (const pool of ["a", "b", "c", "d", "e"]) select(h, "bsc", pool);
    const before = Array.from(h.context.marketPoolSelection("bsc").selected);
    select(h, "bsc", "f", true, true);
    assert.deepEqual(Array.from(h.context.marketPoolSelection("bsc").selected), before);
    assert.equal(h.context.marketPoolSelection("bsc").primary, "a");
    assert.equal(h.toasts.at(-1), h.context.t("marketMonitoringPoolLimit", { limit: 5 }));
    select(h, "ethereum", "eth");
    assert.equal(h.context.marketPoolSelection("ethereum").selected.size, 0);
    assert.equal(h.state.marketWizard.selectedChains.has("ethereum"), true, "Read-only query choice must stay selected");
    select(h, "bsc", "a", false); select(h, "bsc", "f", true, true);
    assert.equal(h.context.marketPoolSelection("bsc").primary, "f");
    assert.deepEqual(Array.from(h.context.marketPoolSelection("bsc").selected), ["b", "c", "d", "e", "f"]);
    scenarios += 1;
  }
  {
    const h = harness(language, { code: "standard", rule_limit: 3, interaction_rule_limit: 0, group_limit: 0, market_project_limit: 1, market_pool_limit: 1, market_pool_mode: "main" });
    select(h, "bsc", "a"); select(h, "bsc", "b", true, true);
    assert.deepEqual(Array.from(h.context.marketPoolSelection("bsc").selected), ["b"]);
    assert.equal(h.context.marketPoolSelection("bsc").primary, "b");
    assert.equal(h.context.marketWizardPoolsReady(), true);
    scenarios += 1;
  }
  {
    const h = harness(language, { market_pool_limit: 3 });
    for (const pool of ["a", "b", "c"]) select(h, "bsc", pool);
    assert.equal(h.context.marketPoolSelection("bsc").selected.size, 3, "UI must use server limit rather than hardcode 2");
    h.state.marketCatalog.monitoring_supported_chains = [];
    assert.equal(h.context.marketWizardPoolsReady(), false);
    assert.equal(h.context.marketWizardScopeError(), h.context.t("marketMonitoringChainNotAllowed"));
    assert.equal(h.context.marketPoolDiscoveryAllowed(), true);
    delete h.state.marketCatalog.monitoring_supported_chains;
    assert.equal(h.context.marketWizardScopeError(), h.context.t("marketMonitoringScopeUnavailable"));
    assert.equal(h.state.chains.length, 3);
    scenarios += 1;
  }
  {
    const h = harness(language, { market_pool_limit: 2, allowed_market_chain_keys: ["bsc", "ethereum"] });
    h.state.marketCatalog.monitoring_supported_chains = h.chains.slice(0, 2);
    select(h, "bsc", "a"); select(h, "ethereum", "eth-a");
    select(h, "ethereum", "eth-b");
    assert.equal(h.context.marketWizardSelectedPoolCount(), 2, "A future second allowed chain must not multiply the per-project pool limit");
    assert.deepEqual(Array.from(h.context.marketPoolSelection("ethereum").selected), ["eth-a"]);
    assert.equal(h.context.marketWizardPoolsReady(), true);
    scenarios += 1;
  }
  {
    const h = harness(language);
    select(h, "bsc", "a"); select(h, "bsc", "b");
    h.plan.market_pool_limit = 1; // A refreshed server limit must not trim an existing draft.
    select(h, "bsc", "c");
    assert.deepEqual(Array.from(h.context.marketPoolSelection("bsc").selected), ["a", "b"]);
    assert.equal(h.context.marketWizardPoolsReady(), false);
    select(h, "bsc", "b", false);
    assert.equal(h.context.marketWizardPoolsReady(), true);
    scenarios += 1;
  }
  {
    const h = harness(language);
    h.state.marketDetail = { project: { id: 7, status: "active" }, pools: [
      { id: 1, chain_key: "bsc", selected: 1, is_primary: 1, supports_event_parsing: 1 },
      { id: 2, chain_key: "bsc", selected: 1, supports_event_parsing: 1 },
      { id: 3, chain_key: "bsc", selected: 1, supports_event_parsing: 1 },
      { id: 4, chain_key: "bsc", selected: 1, supports_event_parsing: 1 },
      { id: 5, chain_key: "bsc", selected: 1, supports_event_parsing: 1 },
      { id: 6, chain_key: "bsc", selected: 0, supports_event_parsing: 1 },
      { id: 7, chain_key: "ethereum", selected: 1, supports_event_parsing: 1 }] };
    const original = JSON.stringify(h.state.marketDetail);
    await h.context.updateMarketPool(6, true, false);
    await h.context.updateMarketPool(7, true, true);
    assert.equal(h.calls.length, 0);
    assert.equal(JSON.stringify(h.state.marketDetail), original, "UI must not trim legacy selected pools");
    h.context.renderMarketProjectPools();
    assert.ok(h.node("marketProjectPools").innerHTML.includes("Ethereum"));
    assert.ok(h.node("marketProjectPools").innerHTML.includes('data-market-toggle-pool="7"'), "Stopping an old unsupported pool must remain available");
    h.respond(() => ({ status: 200, data: JSON.parse(original) }));
    await h.context.updateMarketPool(7, false, false);
    assert.equal(h.calls.length, 1);
    assert.equal(JSON.parse(h.calls[0].options.body).selected, false);
    scenarios += 1;
  }
  {
    const h = harness(language);
    select(h, "bsc", "a");
    h.state.entitlement.market_project_count = 5;
    await h.context.createMarketProject();
    assert.equal(h.calls.length, 0);
    assert.equal(h.toasts.at(-1), h.context.t("marketMonitoringProjectLimit", { limit: 5 }));
    assert.equal(h.context.marketPoolSelection("bsc").selected.size, 1);
    scenarios += 1;
  }
  {
    const h = harness(language);
    for (const pool of ["a", "b", "c", "d", "e"]) select(h, "bsc", pool);
    h.state.entitlement.market_project_count = 4;
    await h.context.createMarketProject();
    const creation = h.calls.find((call) => call.path === "/api/market/projects");
    assert.ok(creation, "The fifth project must be permitted by the displayed server project quota");
    assert.deepEqual(JSON.parse(creation.options.body).deployments[0].pool_addresses, ["a", "b", "c", "d", "e"]);
    assert.equal(h.plan.rule_limit, 10, "More projects/pools must not raise the shared-rule entitlement");
    assert.equal(h.plan.interaction_rule_limit, 1);
    scenarios += 1;
  }
  {
    const h = harness(language);
    const project = { project: { id: 7, status: "active" }, pools: Array.from({ length: 6 }, (_, index) => ({
      id: index + 1, chain_key: "bsc", selected: index < 4 ? 1 : 0, is_primary: index === 0 ? 1 : 0, supports_event_parsing: 1,
    })) };
    h.state.marketDetail = project;
    h.respond(() => ({ status: 200, data: { ...project, pools: project.pools.map((pool) => pool.id === 5 ? { ...pool, selected: 1 } : pool) } }));
    await h.context.updateMarketPool(5, true, false);
    assert.equal(h.calls.length, 1, "The fifth selected pool must remain available");
    assert.equal(JSON.parse(h.calls[0].options.body).market_pool_id, 5);
    await h.context.updateMarketPool(6, true, false);
    assert.equal(h.calls.length, 1, "The sixth selected pool must be rejected without a server write");
    assert.equal(h.toasts.at(-1), h.context.t("marketMonitoringPoolLimit", { limit: 5 }));
    scenarios += 1;
  }
  {
    const h = harness(language, { code: "standard", rule_limit: 3, interaction_rule_limit: 0, group_limit: 0, market_project_limit: 1, market_pool_limit: 1, market_pool_mode: "main" });
    select(h, "bsc", "a");
    h.state.entitlement.market_project_count = 1;
    await h.context.createMarketProject();
    assert.equal(h.calls.length, 0, "Standard must keep its one-project limit");
    assert.equal(h.toasts.at(-1), h.context.t("marketMonitoringProjectLimit", { limit: 1 }));
    scenarios += 1;
  }
  for (const [code, key, status] of [["market_chain_not_allowed", "marketMonitoringChainNotAllowed", 403],
    ["market_pool_limit_reached", "marketMonitoringPoolLimitExceeded", 409], ["market_pool_scope_not_allowed", "marketMonitoringPoolScopeDenied", 400]]) {
    const h = harness(language);
    h.respond(() => ({ status, data: { code, detail: "private scope internals" } }));
    await assert.rejects(h.context.api("/api/market/projects/7/restore", { method: "POST" }), (error) => {
      assert.equal(error.code, code); assert.equal(error.message, h.context.t(key)); return true;
    });
    assert.equal(h.calls.length, 1); assert.equal(h.effects.resets, 0);
    h.calls.length = 0;
    h.respond(() => ({ status: 401, data: { code } }));
    await assert.rejects(h.context.api("/api/market/projects/7/restore"), (error) => {
      assert.equal(error.message, h.context.t("sessionExpired")); assert.ok(!Object.hasOwn(error, "code")); return true;
    });
    assert.equal(h.effects.resets, 1);
    scenarios += 1;
  }
}
console.log(`H5 market monitoring scope OK: ${scenarios} scenarios`);
