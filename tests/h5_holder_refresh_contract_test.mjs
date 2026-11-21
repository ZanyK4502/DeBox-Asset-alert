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
  const start = app.search(new RegExp(`^function ${name}\\(`, "m"));
  assert.ok(start >= 0);
  const tail = app.slice(start), next = tail.slice(1).search(/^(?:(?:async )?function |const )/m);
  assert.ok(next >= 0);
  return tail.slice(0, next + 1);
}
for (const language of ["zh", "en"]) {
  const context = vm.createContext({ window: {}, state: { uiLanguage: language },
    marketChainName: (key) => key, marketDate: (time) => time });
  vm.runInContext(i18n, context); context.I18N = context.window.H5_I18N;
  for (const name of ["t", "escapeHtml", "marketHolderCoverageHTML"]) vm.runInContext(sourceFunction(name), context);
  const note = context.t("marketHolderCoverageLimit");
  assert.match(note, language === "zh" ? /2000.*原始持有人/ : /2,000 raw holders/);
  assert.match(note, language === "zh" ? /目标每 15 分钟一轮，排队或失败可能延后/ : /Targets a 15-minute refresh cycle; queues or failures may delay it/);
  assert.match(note, language === "zh" ? /同链同币共享/ : /shared by chain and token/);
  assert.match(note, language === "zh" ? /专业版有效大户规则/ : /active Professional holder rules/);
  assert.match(note, language === "zh" ? /前 1～100 名.*前 20 名/ : /Top 1–100.*Top 20/);
  assert.match(note, language === "zh" ? /超限.*保留旧基准.*暂停本轮/ : /Above this limit.*previous baseline.*pause/);
  assert.match(note, language === "zh" ? /不代表同一区块快照/ : /not a same-block snapshot/);
  assert.ok(!/1,000|1000/.test(note), "The old raw-sampling ceiling must not remain in the user copy");
  scenarios += 1;

  const previous = { chain_key: "bsc", covered_top_n: 100, last_success_at: "2026-11-14T00:00:00Z" };
  const complete = context.marketHolderCoverageHTML([{ ...previous, status: "complete" }]);
  assert.ok(complete.includes(context.t("marketHolderCoverageReady")));
  assert.ok(complete.includes("Top 100"));
  assert.ok(complete.includes(previous.last_success_at));
  assert.ok(!/Top 2000|Top 2,000/.test(complete), "Raw sampling ceiling must not become supported ranking N");
  scenarios += 1;

  for (const status of ["source_coverage_unknown", "pending", "source_capacity"]) {
    const saved = { ...previous, status };
    const original = JSON.stringify(saved);
    const output = context.marketHolderCoverageHTML([saved]);
    assert.ok(output.includes(context.t("marketHolderCoverageUnknown")));
    assert.ok(!output.includes(context.t("marketHolderCoverageReady")));
    assert.ok(output.includes(previous.last_success_at), "Failed/incomplete sampling must retain the previous baseline time");
    assert.equal(JSON.stringify(saved), original);
    scenarios += 1;
  }
  const empty = context.marketHolderCoverageHTML([]);
  assert.ok(empty.includes(context.t("marketHolderCoverageUnknown")));
  assert.ok(!empty.includes(context.t("marketHolderCoverageReady")));
  scenarios += 1;
}
console.log(`H5 holder refresh contract OK: ${scenarios} scenarios`);
