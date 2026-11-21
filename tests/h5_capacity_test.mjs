import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const app = fs.readFileSync(path.join(root, "static", "app.js"), "utf8");
const i18n = fs.readFileSync(path.join(root, "static", "i18n.js"), "utf8");
let scenarios = 0;

function sourceFunction(name) {
  const start = app.search(new RegExp(`^(?:async )?function ${name}\\(`, "m"));
  assert.ok(start >= 0, `Missing production function: ${name}`);
  const remaining = app.slice(start);
  const next = remaining.slice(1).search(/^(?:(?:async )?function |const )/m);
  assert.ok(next >= 0, `Missing boundary after production function: ${name}`);
  return remaining.slice(0, next + 1);
}

function harness(language, status, data) {
  const calls = [];
  const effects = { resets: 0 };
  const context = vm.createContext({
    window: {}, state: { uiLanguage: language },
    fetch: async (requestPath, options) => {
      calls.push({ path: requestPath, options });
      assert.equal(calls.length, 1, "Capacity rejection must not automatically retry");
      return { ok: status >= 200 && status < 300, status, json: async () => data };
    },
    resetConnectionState: () => { effects.resets += 1; },
  });
  vm.runInContext(i18n, context, { filename: "static/i18n.js" });
  context.t = (key) => {
    const value = context.window.H5_I18N[language][key];
    assert.equal(typeof value, "string", `Missing translation: ${language}/${key}`);
    return value;
  };
  for (const name of ["localizedApiError", "api"]) {
    vm.runInContext(sourceFunction(name), context, { filename: `static/app.js:${name}` });
  }
  return { context, calls, effects };
}

const capacityMessages = { capacity_not_ready: "capacityNotReadyNotice", capacity_full: "capacityFullNotice" };
for (const language of ["zh", "en"]) {
  for (const [code, key] of Object.entries(capacityMessages)) {
    for (const status of [409, 503]) {
      const h = harness(language, status, { code, detail: "Internal allocation details must not be shown" });
      const options = { method: "POST", body: JSON.stringify({ plan: "professional" }) };
      await assert.rejects(h.context.api("/api/subscription/orders", options), (error) => {
        assert.equal(error.message, h.context.t(key));
        assert.equal(error.status, status);
        assert.equal(error.code, code);
        assert.match(error.message, language === "zh" ? /已有监控继续运行/ : /Existing monitoring continues/);
        assert.match(error.message, language === "zh" ? /新购买或扩大监控/ : /purchases and monitoring expansion/);
        return true;
      });
      assert.equal(h.calls[0].options.credentials, "same-origin");
      assert.equal(h.calls[0].options.body, options.body);
      assert.equal(h.effects.resets, 0, "Capacity refusal must not log out an authenticated user");
      scenarios += 1;
    }
    for (const requestPath of ["/api/watch-rules", "/api/auth/session"]) {
      const h = harness(language, 401, { code });
      await assert.rejects(h.context.api(requestPath), (error) => {
        assert.equal(error.message, h.context.t("sessionExpired"));
        assert.ok(!Object.hasOwn(error, "code"));
        return true;
      });
      assert.equal(h.effects.resets, requestPath === "/api/auth/session" ? 0 : 1);
      scenarios += 1;
    }
    const payload = { code, value: 42 };
    const h = harness(language, 200, payload);
    assert.deepEqual(await h.context.api("/api/test"), payload);
    assert.equal(h.effects.resets, 0);
    scenarios += 1;
  }
  for (const [code, key, status] of [
    ["maintenance", "maintenanceNotice", 503],
    ["quote_unavailable", "marketRecommendationUnavailable", 503],
    ["subscription_seats_full", "subscriptionSeatsFullNotice", 409],
    ["holder_targets_full", "holderTargetsFullNotice", 409],
  ]) {
    const h = harness(language, status, { code, detail: "Internal allocation details must not be shown" });
    await assert.rejects(h.context.api("/api/test"), (error) => {
      assert.equal(error.message, h.context.t(key));
      assert.equal(error.status, status);
      assert.equal(error.code, code);
      return true;
    });
    assert.equal(h.effects.resets, 0);
    scenarios += 1;
  }
  const h = harness(language, 500, { code: "capacity_state", message: "Original state error" });
  await assert.rejects(h.context.api("/api/test"), (error) => {
    assert.equal(error.message, "Original state error");
    assert.ok(!Object.hasOwn(error, "code"));
    return true;
  });
  scenarios += 1;
}
console.log(`H5 capacity errors passed (${scenarios} scenarios).`);
