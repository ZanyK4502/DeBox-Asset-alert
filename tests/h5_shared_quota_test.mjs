import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const app = fs.readFileSync(path.join(root, "static/app.js"), "utf8");
const translations = fs.readFileSync(path.join(root, "static/i18n.js"), "utf8");
let scenarios = 0;
function sourceFunction(name) {
  const start = app.search(new RegExp(`^(?:async )?function ${name}\\(`, "m"));
  assert.ok(start >= 0, `Missing production function: ${name}`);
  const tail = app.slice(start), next = tail.slice(1).search(/^(?:(?:async )?function |const )/m);
  assert.ok(next >= 0, `Missing function boundary: ${name}`);
  return tail.slice(0, next + 1);
}
function harness(language) {
  const nodes = new Map();
  const node = (id) => {
    if (!nodes.has(id)) nodes.set(id, { innerHTML: "", textContent: "" });
    return nodes.get(id);
  };
  const state = { uiLanguage: language, deboxUserId: "fixture-user", combinationMembers: [], entitlement: null };
  const context = vm.createContext({ window: {}, state, $: node, document: { querySelectorAll: () => [] },
    TIME: { formatExpiryDate: () => "2026-12-01" }, fillSummaryForm: () => {},
    ruleLabel: (code) => code, shortAddress: (address) => address,
  });
  vm.runInContext(translations, context);
  context.I18N = context.window.H5_I18N;
  for (const name of ["t", "quotaNumber", "localizedPlan", "currentPlan", "interactionQuotaText", "localizedRuleDescription", "localizedApiError", "ruleTypeHelp", "combinationDraftQuotaText", "escapeHtml", "renderSubscription", "renderCombinationDraft", "localizedPauseReason"]) {
    vm.runInContext(sourceFunction(name), context);
  }
  return { context, state, node };
}
const free = { code: "free", wallet_limit: 1, rule_limit: 1, interaction_rule_limit: 0, market_project_limit: 0, group_limit: 0, daily_alert_limit: 5 };
const standard = { code: "standard", wallet_limit: 3, rule_limit: 3, interaction_rule_limit: 0, market_project_limit: 1, market_pool_limit: 1, group_limit: 0 };
const professional = { code: "professional", wallet_limit: 20, rule_limit: 10, interaction_rule_limit: 1, market_project_limit: 5, market_pool_limit: 5, group_limit: 5 };
for (const language of ["zh", "en"]) {
  const h = harness(language);
  for (const plan of [free, standard, professional]) {
    const text = h.context.localizedPlan(plan);
    assert.ok(text.description.includes(String(plan.rule_limit)));
    assert.ok(text.description.includes(language === "zh" ? "共享规则" : "shared rule"));
    assert.ok(!text.description.includes("{"), "Plan placeholders must resolve from catalog values");
    assert.ok(!/100\s*(?:条|rules)/.test(text.description), "Obsolete shared-rule capacity leaked into plans");
    if (plan.code !== "free") {
      assert.ok(text.description.includes(String(plan.market_project_limit)));
      assert.ok(text.description.includes(String(plan.market_pool_limit)));
    }
    scenarios += 1;
  }
  assert.match(h.context.localizedPlan(professional).description, language === "zh"
    ? /5 个代币监控项目，每项目最多 5 个所选池/
    : /token project limit 5, up to 5 selected pools in total per project/);
  assert.match(h.context.localizedPlan(standard).description, language === "zh"
    ? /1 个代币监控项目，每项目最多 1 个主池/
    : /token project limit 1, up to 1 primary pool per project/);
  const custom = { ...professional, wallet_limit: 19, rule_limit: 14, interaction_rule_limit: 2, market_project_limit: 3, group_limit: 4 };
  const customText = h.context.localizedPlan(custom).description;
  for (const value of [19, 14, 2, 3, 4]) assert.ok(customText.includes(String(value)), `Catalog override ${value} was ignored`);
  for (const feature of language === "zh" ? ["大户", "Four.meme", "阶段", "组合", "群"] : ["holders", "Four.meme", "stage", "combination", "groups"]) assert.ok(customText.includes(feature));
  assert.equal(h.context.localizedPlan({ code: "other", description: "server {literal}" }).description, "server {literal}");
  scenarios += 1;

  h.state.entitlement = { plan: professional, rule_count: 7, interaction_rule_count: 1, wallet_count: 2, group_count: 1, market_project_count: 1,
    // The current page is intentionally incomplete; never count these cached
    // visible rules to replace the backend's all-member usage accounting.
    active_rules: [], rules: [], permanent: true, subscription: {}, days_remaining: 0 };
  h.context.renderSubscription(false);
  let html = h.node("subscriptionBox").innerHTML;
  assert.ok(html.includes(h.context.t("ruleMetric", { used: 7, limit: 10 })));
  assert.ok(html.includes(h.context.t("interactionRuleMetric", { used: 1, limit: 1 })));
  assert.ok(html.includes(h.context.t("sharedRuleQuotaHelp")));
  assert.ok(html.includes(h.context.t("permanentPlanActive")), "Permanent users must see the same quota contract");
  assert.ok(html.includes(h.context.t("marketProjectMetric", { used: 1, limit: 5 })));
  assert.ok(!/Infinity|undefined|NaN/.test(html));
  scenarios += 1;

  delete h.state.entitlement.interaction_rule_count;
  h.state.entitlement.active_rules = [{ id: 9, rule_type: "address_interaction", rule_scope: "combination" }];
  h.context.renderSubscription(false);
  html = h.node("subscriptionBox").innerHTML;
  assert.ok(html.includes(h.context.t("interactionRuleLimit", { limit: 1 })));
  assert.ok(!html.includes(h.context.t("interactionRuleMetric", { used: 0, limit: 1 })), "Missing backend count cannot become an invented zero");
  assert.ok(!html.includes(h.context.t("interactionRuleMetric", { used: 1, limit: 1 })), "Partial cached members cannot become an authoritative count");
  scenarios += 1;
  const oldPlan = { ...professional }; delete oldPlan.interaction_rule_limit;
  assert.equal(h.context.interactionQuotaText(oldPlan, h.state.entitlement), "", "Old API must not invent a numeric sublimit");
  for (const invalid of [null, undefined, "", " ", true, [], {}, -1, 1.5, "1.5", "1e2", "0x10", Infinity, NaN]) assert.equal(h.context.quotaNumber(invalid), null);
  for (const valid of [0, 1, 10, "0", "10"]) assert.equal(h.context.quotaNumber(valid), Number(valid));
  scenarios += 1;

  h.state.entitlement.interaction_rule_count = 1;
  assert.ok(h.context.ruleTypeHelp("address_interaction").includes(h.context.t("interactionRuleMetric", { used: 1, limit: 1 })));
  assert.equal(h.context.ruleTypeHelp("approval_change"), h.context.localizedRuleDescription("approval_change"), "Approve rules must not inherit the interaction sublimit");
  h.state.combinationMembers = [
    { client_id: "a", required_trigger_count: 1, rule: { wallet_address: "0xa", chain_key: "bsc", rule_type: "balance_change" } },
    { client_id: "b", required_trigger_count: 1, rule: { wallet_address: "0xb", chain_key: "bsc", rule_type: "address_interaction" } },
  ];
  h.context.renderCombinationDraft();
  html = h.node("combinationDraftList").innerHTML;
  assert.ok(html.includes(h.context.t("combinationDraftQuota", { members: 2, interactions: 1, totalLimit: 10, interactionLimit: 1 })));
  assert.equal(h.node("combinationMemberCount").textContent, "2");
  assert.equal(h.state.combinationMembers.length, 2, "Displaying quotas must not remove or rewrite draft members");
  assert.equal(h.context.localizedPauseReason({ pause_reason: "interaction_rule_limit" }, professional), h.context.t("interactionRuleLimitExceeded"));
  scenarios += 1;
  const backendInteractionError = "当前套餐最多支持 1 条指定地址交互规则，独立规则和组合成员合计，并计入总规则额度。";
  const backendSharedError = "当前套餐最多支持 10 条运行规则。";
  assert.equal(h.context.localizedApiError(backendInteractionError), language === "zh" ? backendInteractionError : h.context.t("interactionRuleLimitExceeded"));
  assert.equal(h.context.localizedApiError(backendSharedError), language === "zh" ? backendSharedError : h.context.t("ruleLimitExceeded"));
  assert.equal(h.context.localizedApiError("无关旧错误"), language === "zh" ? "无关旧错误" : h.context.t("requestFailed"));
  scenarios += 1;
}
console.log(`H5 shared rule and interaction quota OK: ${scenarios} scenarios`);
