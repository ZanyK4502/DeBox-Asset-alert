import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const html = fs.readFileSync(path.join(root, "static", "index.html"), "utf8");
const app = fs.readFileSync(path.join(root, "static", "app.js"), "utf8");
const i18nSource = fs.readFileSync(path.join(root, "static", "i18n.js"), "utf8");

const htmlIDs = new Set([...html.matchAll(/\bid="([^"]+)"/g)].map((match) => match[1]));
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
  "/api/subscription/free-trial",
  "/api/subscription/complimentary",
  "/api/payment/config",
  "/api/payment/prepare",
  "/api/payment/verify",
  "/api/debox/token",
  "/api/chain/balance",
  "/api/watch-rules",
  "/api/aggregate-events",
  "/api/subscription/summary-settings",
  "/api/notification-groups",
];
for (const endpoint of requiredAPIs) {
  assert.ok(app.includes(endpoint), `H5 no longer references required API: ${endpoint}`);
}

const context = { window: {} };
vm.runInNewContext(i18nSource, context, { filename: "static/i18n.js" });
const translations = context.window.H5_I18N;
assert.ok(translations?.zh && translations?.en, "Chinese and English H5 dictionaries are required");

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
const appScript = html.indexOf('<script src="/static/app.js"></script>');
assert.ok(i18nScript >= 0 && appScript > i18nScript, "i18n.js must load before app.js");

console.log(
  `H5 contract OK: ${referencedIDs.size} DOM references, ${requiredAPIs.length} APIs, ${translationKeys.size} translation keys`,
);
