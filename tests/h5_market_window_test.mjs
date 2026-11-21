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
const volume = { code: "market_volume_above", window: { kind: "fixed", fixed_minutes: [5, 60, 360, 1440], default_minutes: 60 } };
const trades = { code: "market_trade_imbalance", window: { kind: "fixed", fixed_minutes: [5, 60, 1440], default_minutes: 60 } };
const custom = { code: "market_price_increase", window: { kind: "custom_snapshot", min_minutes: 1, max_minutes: 1440 }, max_baseline_lag_seconds: 30 };
const event = { code: "market_consecutive_large_buy", window: { kind: "custom_event", min_minutes: 1, max_minutes: 1440 } };
const rank = { code: "market_holder_rank_entered", window: { kind: "none" }, max_holder_rank: 100 };

function harness(language) {
  const nodes = new Map(), toasts = [];
  const node = (id) => {
    if (!nodes.has(id)) nodes.set(id, { value: "", hidden: false, disabled: false, innerHTML: "", textContent: "", querySelectorAll: () => [] });
    return nodes.get(id);
  };
  const state = { marketDetail: { project: { status: "active" }, rules: [] } };
  const context = vm.createContext({
    window: {}, state, $: node, toast: (value) => toasts.push(value),
    marketRuleDefinition: () => volume, marketRuleName: () => "Fixture", marketRuleDisplayName: () => "Fixture",
    marketProjectIsExpiredFrozen: () => false, marketPauseReason: () => "", marketDeliveryModeLabel: () => "Realtime",
    MARKET_UNIT_KEYS: {}, marketDate: (value) => String(value), marketChainName: (value) => String(value),
  });
  vm.runInContext(i18n, context);
  context.t = (key, params = {}) => {
    if (key === "usd") return "USD";
    const text = context.window.H5_I18N[language][key];
    assert.equal(typeof text, "string", `Missing translation ${key}`);
    return text.replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? ""));
  };
  for (const name of ["escapeHtml", "marketRuleIsActive", "marketRecommendationControls", "marketWindowCapability", "marketWindowIssue", "marketRuleParameterIssue", "marketRuleEditorParametersValid", "updateMarketWindowControl", "renderMarketRules", "marketHolderCoverageHTML"]) {
    vm.runInContext(sourceFunction(name), context);
  }
  return { context, state, node, toasts };
}

for (const language of ["zh", "en"]) {
  const h = harness(language);
  for (const mode of ["detail", "wizard"]) {
    const controls = h.context.marketRecommendationControls(mode);
    controls.sensitivity.value = "custom";
    controls.threshold.value = "42";
    controls.window.value = "17";
    h.context.updateMarketWindowControl(mode, volume, true);
    assert.equal(controls.window.value, "17", "Unsupported old window must not be silently replaced");
    assert.equal(controls.threshold.value, "42");
    assert.equal(controls.window.hidden, true);
    assert.equal(controls.windowSelect.hidden, false);
    assert.equal(controls.windowSelect.disabled, false);
    assert.match(controls.windowSelect.innerHTML, /value="17" disabled/);
    assert.ok(controls.windowHelp.textContent.includes("17"));
    assert.equal(h.context.marketRuleEditorParametersValid(mode, volume), false);
    controls.windowSelect.value = "360";
    controls.windowSelect.onchange();
    assert.equal(controls.window.value, "360");
    assert.equal(h.context.marketRuleEditorParametersValid(mode, volume), true);
    assert.notEqual(h.context.marketWindowIssue(trades, "360"), "");
    assert.equal(h.context.marketWindowIssue(trades, "60"), "");
    scenarios += 1;

    controls.window.value = "17";
    h.context.updateMarketWindowControl(mode, custom, true);
    assert.equal(controls.window.hidden, false);
    assert.equal(controls.window.max, "1440");
    assert.equal(controls.window.step, "1");
    assert.equal(controls.window.value, "17");
    assert.ok(controls.windowHelp.textContent.includes("30"));
    for (const value of ["1", "17", "1440"]) {
      assert.equal(h.context.marketWindowIssue(custom, value), "");
      assert.equal(h.context.marketWindowIssue(event, value), "");
    }
    for (const value of ["", "0", "1.5", "1441", "NaN", "Infinity"]) assert.notEqual(h.context.marketWindowIssue(custom, value), "");
    scenarios += 1;

    controls.window.value = "-999";
    controls.threshold.value = "100";
    h.context.updateMarketWindowControl(mode, rank, true);
    assert.equal(controls.windowWrap.hidden, true);
    assert.equal(controls.window.value, "-999", "Unused legacy fields are preserved");
    assert.equal(controls.threshold.max, "100");
    assert.equal(controls.threshold.step, "1");
    assert.equal(h.context.marketRuleEditorParametersValid(mode, rank), true);
    for (const value of ["0", "101", "1.5", "NaN", ""]) assert.notEqual(h.context.marketRuleParameterIssue(rank, "-999", value), "");
    controls.sensitivity.value = "balanced";
    controls.window.value = "60";
    h.context.updateMarketWindowControl(mode, volume, true);
    assert.equal(controls.windowSelect.disabled, true, "Recommended periods remain locked");
    scenarios += 1;
  }

  const saved = { id: 7, rule_type: volume.code, threshold_value: "42", threshold_unit: "usd", window_minutes: 17, enabled: 0, run_status: "paused", runtime_issues: [{ code: "unsupported_statistical_period" }] };
  h.state.marketDetail.rules = [saved];
  h.context.renderMarketRules();
  const html = h.node("marketRulesList").innerHTML;
  assert.ok(html.includes("17") && html.includes("42"));
  assert.match(html, /data-restore-market-rule="7" disabled/);
  assert.ok(html.includes(h.context.t("marketRuleStatusUnsupported")));
  assert.equal(saved.window_minutes, 17);
  assert.equal(saved.threshold_value, "42");
  assert.equal(h.context.marketRuleIsActive({ ...saved, enabled: 1, run_status: "active" }), false);
  assert.equal(h.context.marketRuleIsActive({ ...saved, enabled: 1, run_status: "active", runtime_issues: [] }), true);
  scenarios += 1;

  h.context.marketRuleDefinition = () => rank;
  const oldRank = { ...saved, rule_type: rank.code, threshold_value: "101", window_minutes: null, runtime_issues: [{ code: "unsupported_holder_rank" }] };
  h.state.marketDetail.rules = [oldRank];
  h.context.renderMarketRules();
  assert.ok(h.node("marketRulesList").innerHTML.includes("101"));
  assert.match(h.node("marketRulesList").innerHTML, /data-restore-market-rule="7" disabled/);
  assert.equal(oldRank.threshold_value, "101", "Old rank limits must not be clamped");
  scenarios += 1;

  const coverage = h.context.marketHolderCoverageHTML([
    { chain_key: "bsc", status: "complete", covered_top_n: 100, last_success_at: "2026-11-14T00:00:00Z" },
    { chain_key: "eth", status: "source_coverage_unknown", covered_top_n: 20, last_success_at: "2026-11-13T00:00:00Z" },
  ]);
  assert.ok(coverage.includes("2026-11-14") && coverage.includes("2026-11-13"));
  assert.ok(coverage.includes(h.context.t("marketHolderCoverageReady")));
  assert.ok(coverage.includes(h.context.t("marketHolderCoverageUnknown")));
  assert.ok(coverage.includes("2000") || coverage.includes("2,000"));
  const filtered = h.context.marketHolderCoverageHTML([{ chain_key: "eth", status: "source_coverage_unknown" }], "bsc");
  assert.ok(!filtered.includes("eth"));
  assert.ok(filtered.includes(h.context.t("marketHolderCoverageUnknown")));
  scenarios += 1;
}

console.log(`H5 market window/rank coverage OK: ${scenarios} scenarios`);
