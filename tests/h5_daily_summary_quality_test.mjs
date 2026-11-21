import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const context = {
  window: {},
  document: { createElement() { return {
    value: "", textContent: "",
    set innerHTML(value) { this.value = String(value); this.textContent = this.value; },
  }; } },
  fetch() { throw new Error("Notification rendering must not request or revalue data"); },
};
vm.runInNewContext(fs.readFileSync(path.join(root, "static/i18n.js"), "utf8"), context);
vm.runInNewContext(fs.readFileSync(path.join(root, "static/notification-detail.js"), "utf8"), context);
const renderer = context.window.H5_NOTIFICATION_DETAIL;
let scenarios = 0;

function translation(language) {
  return (key, values = {}) => {
    const text = context.window.H5_I18N[language][key];
    assert.equal(typeof text, "string", `Missing ${language} translation: ${key}`);
    return text.replace(/\{(\w+)\}/g, (_, name) => String(values[name] ?? ""));
  };
}

function render(language, summary = {}, statistics = {}, schema) {
  const t = translation(language);
  return renderer.render({
    schema_version: 1, notification_kind: "daily_summary", access_scope: "private", language,
    label: "Daily fixture", notification_text: "Original sent text",
    data: {
      schema_version: arguments.length >= 4 ? schema : 2,
      statistics: { market_buy_count: 2, market_sell_count: 1, market_buy_valued_count: 0, market_sell_valued_count: 0, ...statistics },
      market_project_chain_summaries: [{
        token_name: "Fixture", token_symbol: "FIX", chain_key: "bsc", start_price_usd: "1", end_price_usd: "2",
        trade_volume_usd: "0", buy_count: 2, sell_count: 1, buy_valued_count: 0, sell_valued_count: 0,
        large_trade_count: 0, quote_unavailable_count: 3, ...summary,
      }],
    },
  }, { language, t, chainName: () => "BNB Chain" });
}

function fact(html, label) {
  const marker = `<dt>${label}</dt><dd>`;
  const start = html.indexOf(marker);
  return start < 0 ? undefined : html.slice(start + marker.length).split("</dd>", 1)[0];
}

// Recorded from the original schema-1 renderer before this change. No dates or
// locale-dependent number formatting enter this fixture, so the entire HTML
// contract can remain byte-for-byte stable across this schema-2 addition.
const legacyHashes = {
  zh: "dff1205482a5691d08a9d5b3107b3f81160b7af2867ad4dfdc0e44a421438167",
  en: "fcd4fb022a3783de86bda6fd8215cc47088347b716a3589cf46b6f63cc3f2c36",
};

for (const language of ["zh", "en"]) {
  const t = translation(language);
  const legacy = render(language, {}, {}, 1);
  assert.equal(crypto.createHash("sha256").update(legacy).digest("hex"), legacyHashes[language], "schema-1 HTML changed");
  assert.equal(fact(legacy, t("detailTradeVolume")), "0");
  assert.equal(fact(legacy, t("detailQuoteGap")), undefined);
  assert.equal(fact(legacy, t("detailValuationGap")), undefined);
  assert.equal(render(language, {}, {}, undefined), legacy, "unversioned legacy details changed");
  scenarios += 1;

  const unknown = render(language);
  assert.equal(fact(unknown, t("detailTradeVolume")), t("detailValuationUnavailable"));
  assert.equal(fact(unknown, t("detailQuoteGap")), t("detailQuoteGapCount", { count: 3 }));
  assert.ok(unknown.includes(t("detailValuationAllGap", { total: 3 })));
  assert.ok(!unknown.includes(`<dt>${t("detailTradeVolume")}</dt><dd>0</dd>`), "unvalued trades became complete zero volume");
  assert.ok(unknown.includes("Original sent text"), "stored notification text was rewritten");
  scenarios += 1;

  const partial = render(language, { buy_valued_count: 1, trade_volume_usd: "12.340000000000000000" }, { market_buy_valued_count: 1 });
  assert.equal(fact(partial, t("detailTradeVolumePartial")), "12.340000000000000000", "stored subtotal was reformatted or revalued");
  assert.equal(fact(partial, t("detailTradeVolume")), undefined, "partial subtotal received the complete-volume label");
  assert.equal(fact(partial, t("detailReliableValuations")), "1 / 3");
  assert.ok(partial.includes(t("detailValuationPartialGap", { missing: 2, total: 3 })));
  scenarios += 1;

  const partialZero = render(language, { buy_valued_count: 1 }, { market_buy_valued_count: 1 });
  assert.equal(fact(partialZero, t("detailTradeVolumePartial")), "0");
  assert.equal(fact(partialZero, t("detailTradeVolume")), undefined);
  scenarios += 1;

  const complete = render(language, { buy_valued_count: 2, sell_valued_count: 1, trade_volume_usd: "123.4500", quote_unavailable_count: 0 }, { market_buy_valued_count: 2, market_sell_valued_count: 1 });
  assert.equal(fact(complete, t("detailTradeVolume")), "123.4500");
  assert.equal(fact(complete, t("detailTradeVolumePartial")), undefined);
  assert.equal(fact(complete, t("detailValuationGap")), undefined);
  assert.equal(fact(complete, t("detailQuoteGap")), undefined);
  scenarios += 1;

  const zero = render(language, { buy_count: 0, sell_count: 0, quote_unavailable_count: 0 }, { market_buy_count: 0, market_sell_count: 0 });
  assert.equal(fact(zero, t("detailTradeVolume")), "0", "real zero trades stopped displaying zero");
  assert.equal(fact(zero, t("detailReliableValuations")), "0 / 0");
  assert.equal(fact(zero, t("detailValuationGap")), undefined);
  scenarios += 1;

  const valuedZero = render(language, { buy_count: 1, sell_count: 0, buy_valued_count: 1, quote_unavailable_count: 0 }, { market_buy_count: 1, market_sell_count: 0, market_buy_valued_count: 1 });
  assert.equal(fact(valuedZero, t("detailTradeVolume")), "0", "a reliably valued zero amount became unknown");
  assert.equal(fact(valuedZero, t("detailValuationGap")), undefined);
  scenarios += 1;

  const missingEvidence = render(language, { buy_valued_count: undefined });
  assert.equal(fact(missingEvidence, t("detailTradeVolume")), t("detailValuationUnavailable"));
  const wrongDirectionCounts = render(language, { buy_count: 0, sell_count: 2, buy_valued_count: 2, sell_valued_count: 0 });
  assert.equal(fact(wrongDirectionCounts, t("detailTradeVolume")), t("detailValuationUnavailable"), "directionally invalid counts falsely proved full valuation");
  scenarios += 1;
}

function renderMarket(language, kind, snapshot = {}) {
  const t = translation(language);
  return renderer.render({
    schema_version: 1, notification_kind: kind, access_scope: "private", language,
    label: "Market fixture", notification_text: "Original market text",
    rule: { type: "market_price_above", threshold: "10" }, actual_value: "10",
    data: { delivery: {
      project: { token_name: "Fixture", token_symbol: "FIX", chain_key: "bsc" },
      event: { event_type: "buy", chain_key: "bsc", token_amount: "2", usd_value: "4", price_usd: "2" },
      pool: { protocol: "PancakeSwap", protocol_version: "v3", liquidity_usd: "999" },
      snapshot: { price_usd: "1", liquidity_usd: null, ...snapshot },
      current_value: "10", trigger_count: 2, rule: { threshold_unit: "usd", trigger_count_threshold: 2 },
    } },
  }, { language, t, chainName: () => "BNB Chain" });
}

function metric(html, label) {
  const marker = `<span>${label}</span>`;
  const start = html.indexOf(marker);
  if (start < 0) return undefined;
  return html.slice(start + marker.length).split("<strong>", 2)[1]?.split("</strong>", 1)[0];
}

// Original marketBase HTML, captured before adding the quote-evidence branch.
const legacyMarketHashes = {
  zh: {
    market_realtime: "b3fa07aea2d982b1fe344271b6cc0b1f604cba1d651e3a8aad9189a613724939",
    market_stage: "190ab3aa679ea932c4609f68e963706475a298931080ce5f8800b469799e6a95",
  },
  en: {
    market_realtime: "87c9f4fe3309f19c65e253d5748c8057addd5c52ba5ce99abcdf7c7bc468ebc4",
    market_stage: "487b1639997a9e8f4ce0ceff28f8964cf5aebc4a996dc32738ec5546467b4c83",
  },
};

for (const language of ["zh", "en"]) {
  const t = translation(language);
  for (const kind of ["market_realtime", "market_stage"]) {
    const legacy = renderMarket(language, kind);
    assert.equal(crypto.createHash("sha256").update(legacy).digest("hex"), legacyMarketHashes[language][kind]);
    assert.equal(metric(legacy, t("marketLiquidity")), "999");
    assert.equal(fact(legacy, t("detailHistoricalQuote")), undefined);
    scenarios += 1;

    const evidence = { status: "cached", fetched_at: "2020-05-20T10:00:00Z", source_timestamp: "2020-05-20T09:59:00Z" };
    const missing = renderMarket(language, kind, { quote_quality: evidence });
    assert.equal(metric(missing, t("marketLiquidity")), t("detailValueUnknown"));
    assert.ok(!missing.includes("999"), "new missing snapshot liquidity fell back to the old pool value");
    const reference = fact(missing, t("detailHistoricalQuote"));
    assert.ok(reference.includes("2020") && reference.includes(t("marketQuoteFetchedTime", { time: "" }).trim()) && reference.includes(t("marketQuoteSourceTime", { time: "" }).trim()));
    assert.ok(reference.includes(t("detailHistoricalQuoteReference")));
    assert.ok(missing.includes("Original market text"));
    scenarios += 1;

    const zero = renderMarket(language, kind, { quote_quality: evidence, liquidity_usd: 0 });
    assert.equal(metric(zero, t("marketLiquidity")), "0");
    const historic = renderMarket(language, kind, { quote_quality: evidence, liquidity_usd: "5.0000" });
    assert.equal(metric(historic, t("marketLiquidity")), "5.0000", "history was revalued or rejected using the current clock");
    scenarios += 1;
  }
}

console.log(`H5 notification quote quality OK: ${scenarios} scenarios`);
