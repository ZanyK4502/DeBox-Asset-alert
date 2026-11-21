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

function harness(language) {
  const list = { innerHTML: "", querySelectorAll: () => [] };
  const nodes = { marketEventsList: list, loadMoreMarketEventsBtn: {} };
  const state = { marketDetail: { pools: [] }, marketEvents: [], marketEventsNextBeforeId: null };
  const context = vm.createContext({
    window: {}, state, $: (id) => nodes[id],
    chainExplorerTransaction: () => "", marketProjectIsExpiredFrozen: () => false,
    marketRuleDefinition: () => ({}), marketRuleName: () => "Rule",
    marketRuleDisplayName: () => "Rule", marketRuleDescription: () => "Condition",
    marketEventLabel: () => "Event", marketEventSummary: () => "Summary",
    marketRuleEventValue: (value) => String(value ?? "-"),
    marketChainName: () => "Chain", marketDate: () => "Time",
    detailCopyRow: () => "", detailSectionHtml: (_, content) => content,
    detailFactsHtml: (facts) => facts.map(([, value]) => value || "").join("|"),
    aggregateEventValue: () => "Value", aggregateChainName: () => "Chain", ruleLabel: () => "Rule",
  });
  vm.runInContext(i18n, context);
  context.t = (key) => {
    const text = context.window.H5_I18N[language][key];
    assert.equal(typeof text, "string", `Missing translation: ${key}`);
    return text;
  };
  for (const name of ["escapeHtml", "aggregateEventStatus", "aggregateEventDetailHtml", "marketRuleEventStatus", "marketRuleEventReason", "marketEventDetailHtml", "renderMarketEvents"]) {
    vm.runInContext(sourceFunction(name), context);
  }
  return { context, state, list };
}

for (const language of ["zh", "en"]) {
  const { context: h, state, list } = harness(language);
  for (const [status, key] of [
    ["unknown", "notificationUnknown"], ["skipped", "notificationSkipped"],
    ["sent", "notificationSent"], ["pending", "notificationPending"],
    ["failed", "notificationFailed"], ["staged", "notificationNotSent"],
  ]) {
    const event = { notification_status: status };
    assert.equal(h.aggregateEventStatus(event).text, h.t(key));
    assert.ok(h.aggregateEventDetailHtml(event).includes(h.t(key)), "Aggregate detail must use the same status");
    scenarios += 1;
  }

  for (const event of [
    { notification_status: "skipped", delivery_outcome: "unknown", notification_error: "cooldown_active" },
    { notification_status: "unknown", notification_error: "holder_excluded" },
    { notification_status: "sent", notification_successful: true, delivery_outcome: "unknown" },
  ]) {
    const status = h.marketRuleEventStatus(event);
    assert.equal(status.text, h.t("marketEventUnknown"));
    assert.equal(status.unknown, true);
    assert.equal(h.marketRuleEventReason(event), h.t("marketEventReasonUnknown"));
    state.marketEvents = [event];
    h.renderMarketEvents();
    for (const html of [list.innerHTML, h.marketEventDetailHtml(event)]) {
      assert.ok(html.includes(h.t("marketEventUnknown")));
      assert.ok(html.includes(h.t("marketEventReasonUnknown")));
      assert.ok(!html.includes(h.t("marketEventReasonSkipped")));
    }
    scenarios += 1;
  }

  const sent = { notification_status: "sent", notification_successful: true };
  assert.equal(h.marketRuleEventStatus(sent).text, h.t("marketEventNotified"));
  assert.equal(h.marketRuleEventStatus(sent).className, "notified");
  assert.equal(h.marketRuleEventReason(sent), "");
  assert.ok(h.marketEventDetailHtml(sent).includes(h.t("marketEventNotified")));
  scenarios += 1;
  for (const [status, reason] of [
    ["skipped", "marketEventReasonSkipped"], ["pending", "marketEventReasonPending"],
    ["failed", "marketEventReasonFailed"],
  ]) {
    assert.equal(h.marketRuleEventStatus({ notification_status: status }).text, h.t("marketEventNotNotified"));
    assert.equal(h.marketRuleEventReason({ notification_status: status }), h.t(reason));
    scenarios += 1;
  }
  assert.ok(!/retry|重试/i.test(h.t("marketEventReasonFailed")), "Failure text must not promise another attempt");
}

console.log(`H5 delivery status OK: ${scenarios} scenarios`);
