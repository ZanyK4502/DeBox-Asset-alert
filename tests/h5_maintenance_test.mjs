import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const app = fs.readFileSync(path.join(root, "static", "app.js"), "utf8");
const i18n = fs.readFileSync(path.join(root, "static", "i18n.js"), "utf8");
const summaryEndpoint = "/api/subscription/summary-settings";
const maintenance = {
  status: 503,
  data: { code: "maintenance", detail: "服务升级中，暂不接受新订单或监控配置调整。" },
};
let scenarios = 0;

function sourceFunction(name) {
  const start = app.search(new RegExp(`^(?:async )?function ${name}\\(`, "m"));
  assert.ok(start >= 0, `Missing production function: ${name}`);
  const remaining = app.slice(start);
  const next = remaining.slice(1).search(/^(?:(?:async )?function |const )/m);
  assert.ok(next >= 0, `Missing boundary after production function: ${name}`);
  return remaining.slice(0, next + 1);
}

function summaryCard({
  chatType = "private",
  chatId = "user-fixture",
  enabled = false,
  time = "",
  timezone = "Asia/Shanghai",
  language = "zh",
  label = "",
} = {}) {
  const fields = {
    ".summary-target-enabled": { checked: enabled },
    ".summary-target-time": { value: time },
    ".summary-target-timezone": { value: timezone },
    ".summary-target-language": { value: language },
    ".summary-target-label": { value: label },
  };
  return {
    dataset: { chatType, chatId },
    querySelector(selector) {
      assert.ok(Object.hasOwn(fields, selector), `Unexpected summary field: ${selector}`);
      return fields[selector];
    },
  };
}

// The real UI includes an unconfigured private card alongside the saved group.
// Turning every checkbox off still submits defaults and changed legacy fields.
function allOffCards() {
  return [
    summaryCard(),
    summaryCard({
      chatType: "group", chatId: "group-fixture", time: "09:30",
      timezone: "UTC", language: "en", label: " Edited group label ",
    }),
  ];
}

function harness({ language = "zh", cards = [], responses = [] } = {}) {
  const calls = [];
  const toasts = [];
  const effects = { resets: 0, refreshes: 0 };
  const context = vm.createContext({
    window: {},
    state: { uiLanguage: language, deboxUserId: "user-fixture" },
    document: {
      querySelectorAll(selector) {
        assert.equal(selector, "[data-summary-target-card]");
        return cards;
      },
    },
    fetch: async (requestPath, options) => {
      calls.push({
        path: requestPath, method: options.method,
        credentials: options.credentials,
        body: options.body === undefined ? undefined : JSON.parse(options.body),
      });
      assert.ok(calls.length <= responses.length, "Unexpected additional request or retry");
      const response = responses[calls.length - 1];
      if (response instanceof Error) throw response;
      return {
        ok: response.status >= 200 && response.status < 300,
        status: response.status,
        json: async () => response.data,
      };
    },
    resetConnectionState: () => { effects.resets += 1; },
    refreshAccount: async () => { effects.refreshes += 1; },
    toast: (message) => { toasts.push(message); },
  });
  vm.runInContext(i18n, context, { filename: "static/i18n.js" });
  context.t = (key) => {
    const value = context.window.H5_I18N[context.state.uiLanguage][key];
    assert.equal(typeof value, "string", `Missing translation: ${key}`);
    return value;
  };
  const timezoneStart = app.indexOf("const SUMMARY_TIMEZONE_OPTIONS =");
  const timezoneEnd = app.indexOf("\nfunction chainLogoSrc(", timezoneStart);
  assert.ok(timezoneStart >= 0 && timezoneEnd > timezoneStart);
  vm.runInContext(app.slice(timezoneStart, timezoneEnd), context);
  for (const name of [
    "localizedApiError", "api", "normalizeSummaryTimezone",
    "selectedSummaryTargets", "persistSummarySettings",
  ]) {
    vm.runInContext(sourceFunction(name), context, { filename: `static/app.js:${name}` });
  }
  return { context, calls, toasts, effects };
}

async function apiFailure({ language, response, path: requestPath = "/api/watch-rules", key, message, code, resets = 0 }) {
  const h = harness({ language, responses: [response] });
  await assert.rejects(h.context.api(requestPath, { method: "POST", body: "{}" }), (error) => {
    assert.equal(error.message, key ? h.context.t(key) : message);
    assert.equal(error.status, response.status);
    assert.equal(error.code, code);
    if (code === undefined) assert.ok(!Object.hasOwn(error, "code"));
    return true;
  });
  assert.equal(h.calls.length, 1);
  assert.equal(h.calls[0].credentials, "same-origin");
  assert.equal(h.effects.resets, resets);
  scenarios += 1;
}

for (const language of ["zh", "en"]) {
  await apiFailure({ language, response: maintenance, key: "maintenanceNotice", code: "maintenance" });
}
await apiFailure({
  language: "en", response: { ...maintenance, status: 401 },
  key: "sessionExpired", resets: 1,
});
await apiFailure({
  language: "en", response: { ...maintenance, status: 401 },
  path: "/api/auth/session", key: "sessionExpired",
});
await apiFailure({ language: "zh", response: { status: 400, data: { detail: "原有业务错误" } }, message: "原有业务错误" });
await apiFailure({ language: "en", response: { status: 400, data: { message: "Original error" } }, message: "Original error" });
await apiFailure({ language: "en", response: { status: 503, data: { detail: "原有中文错误" } }, key: "requestFailed" });
{
  const data = { code: "maintenance", value: 42 };
  const h = harness({ responses: [{ status: 200, data }] });
  assert.deepEqual(await h.context.api("/api/test"), data);
  assert.equal(h.calls.length, 1);
  assert.equal(h.effects.resets, 0);
  scenarios += 1;
}

for (const language of ["zh", "en"]) {
  const h = harness({ language, cards: allOffCards(), responses: [maintenance, { status: 200, data: {} }] });
  assert.equal(await h.context.persistSummarySettings(), true);
  assert.deepEqual(h.calls.map((call) => call.path), [summaryEndpoint, summaryEndpoint]);
  assert.deepEqual(h.calls.map((call) => call.method), ["POST", "POST"]);
  assert.deepEqual(h.calls[0].body, {
    enabled: false, push_time: "20:00", timezone: "Asia/Shanghai",
    targets: [
      { chat_type: "private", chat_id: "user-fixture", enabled: false, push_time: "20:00", timezone: "Asia/Shanghai", language: "zh", label: "" },
      { chat_type: "group", chat_id: "group-fixture", enabled: false, push_time: "09:30", timezone: "UTC", language: "en", label: "Edited group label" },
    ],
    label: "", language: "zh",
  });
  assert.deepEqual(h.calls[1].body, { enabled: false });
  assert.equal(h.effects.refreshes, 1);
  assert.equal(h.effects.resets, 0);
  assert.deepEqual(h.toasts, [h.context.t("summaryStoppedDuringMaintenance")]);
  scenarios += 1;
}

for (const cards of [[summaryCard({ enabled: true })], [summaryCard(), summaryCard({ chatType: "group", enabled: true })]]) {
  const h = harness({ cards, responses: [maintenance] });
  await assert.rejects(h.context.persistSummarySettings(), (error) => error.code === "maintenance");
  assert.equal(h.calls.length, 1);
  assert.equal(h.effects.refreshes, 0);
  assert.deepEqual(h.toasts, []);
  scenarios += 1;
}

for (const response of [
  { status: 400, data: { detail: "普通业务错误" } },
  { status: 503, data: { detail: "普通服务错误" } },
  { ...maintenance, status: 401 },
  new Error("Network unavailable"),
]) {
  const h = harness({ cards: allOffCards(), responses: [response] });
  await assert.rejects(h.context.persistSummarySettings());
  assert.equal(h.calls.length, 1);
  assert.equal(h.effects.refreshes, 0);
  assert.deepEqual(h.toasts, []);
  scenarios += 1;
}

for (const cards of [allOffCards(), [summaryCard({ enabled: true })]]) {
  const h = harness({ cards, responses: [{ status: 200, data: {} }] });
  assert.equal(await h.context.persistSummarySettings(), true);
  assert.equal(h.calls.length, 1);
  assert.ok(Object.hasOwn(h.calls[0].body, "targets"), "Normal submission must keep its complete payload");
  assert.equal(h.effects.refreshes, 1);
  assert.deepEqual(h.toasts, [h.context.t("summarySaved")]);
  scenarios += 1;
}

for (const response of [
  { status: 400, data: { detail: "停止失败" } },
  maintenance,
]) {
  const h = harness({ cards: allOffCards(), responses: [maintenance, response] });
  await assert.rejects(h.context.persistSummarySettings(), (error) => {
    assert.equal(error.status, response.status);
    assert.equal(error.message, response === maintenance ? h.context.t("maintenanceNotice") : response.data.detail);
    assert.equal(error.code, response === maintenance ? "maintenance" : undefined);
    return true;
  });
  assert.equal(h.calls.length, 2, "A failed minimal stop must never cause a third request");
  assert.deepEqual(h.calls[1].body, { enabled: false });
  assert.equal(h.effects.refreshes, 0);
  assert.deepEqual(h.toasts, []);
  scenarios += 1;
}

{
  const h = harness();
  assert.equal(await h.context.persistSummarySettings(), false);
  assert.equal(h.calls.length, 0);
  assert.deepEqual(h.toasts, [h.context.t("summaryTargetRequired")]);
  scenarios += 1;
}

console.log(`H5 maintenance OK: ${scenarios} scenarios`);
