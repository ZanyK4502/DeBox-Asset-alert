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
  assert.ok(start >= 0, `Missing production function ${name}`);
  const tail = app.slice(start), next = tail.slice(1).search(/^(?:(?:async )?function |const )/m);
  assert.ok(next >= 0, `Missing function boundary ${name}`);
  return tail.slice(0, next + 1);
}
function harness(language) {
  const state = { uiLanguage: language, deboxUserId: "local-pro-user", combinationMembers: [],
    entitlement: { permanent: true, plan: { code: "professional", rule_limit: 10, interaction_rule_limit: 1, market_project_limit: 5,
      market_pool_limit: 5, allowed_market_chain_keys: ["bsc"] }, interaction_rule_count: 0 } };
  const nodes = new Map(), calls = [], toasts = [];
  const node = (id) => {
    if (!nodes.has(id)) nodes.set(id, { value: "", hidden: false, required: false, selectedOptions: [], focus() {} });
    return nodes.get(id);
  };
  let response = { status: 200, data: {} };
  const context = vm.createContext({ state, window: {}, $: node,
    fetch: async (requestPath, options) => {
      calls.push({ path: requestPath, options, body: options.body ? JSON.parse(options.body) : null });
      return { ok: response.status >= 200 && response.status < 300, status: response.status, json: async () => response.data };
    }, refreshAccount: async () => {}, resetConnectionState: () => { throw new Error("A capacity refusal must not log out the user"); },
    toast: (text) => { toasts.push(text); }, isMobileShell: () => false,
    resetCombinationMemberEditor: () => {}, renderCombinationDraft: () => {},
  });
  vm.runInContext(translations, context); context.I18N = context.window.H5_I18N;
  for (const name of ["t", "quotaNumber", "localizedRuleDescription", "currentPlan", "interactionQuotaText", "ruleTypeHelp", "localizedApiError", "api",
    "isThresholdlessRule", "requiresPositiveThreshold", "thresholdValue", "validateRuleDraft", "updateDeliveryModeFields", "updateRuleFields",
    "updateCombinationMemberFields", "combinationMemberDraft", "addCombinationMember", "createRule", "createCombinationRule"]) {
    vm.runInContext(sourceFunction(name), context, { filename: `app.js:${name}` });
  }
  const values = { ruleTypeSelect: "address_interaction", chainSelect: "ethereum", walletAddressInput: "0x1111111111111111111111111111111111111111",
    targetAddressInput: "0x2222222222222222222222222222222222222222", targetLabelInput: "local treasury",
    tokenAddressInput: "", thresholdInput: "", targetTypeSelect: "private", ruleLanguageSelect: language, deliveryModeSelect: "realtime",
    cycleTypeSelect: "fixed", cycleMinutesInput: "60", triggerCountInput: "1",
    combinationRuleTypeSelect: "address_interaction", combinationChainSelect: "base", combinationAddressInput: "0x3333333333333333333333333333333333333333",
    combinationTargetAddressInput: "0x4444444444444444444444444444444444444444", combinationTargetLabelInput: "local contract",
    combinationTokenAddressInput: "", combinationThresholdInput: "", combinationMemberTriggerInput: "2", combinationLanguageSelect: language,
    combinationTargetTypeSelect: "private", combinationNoteInput: "retained feature acceptance", combinationCycleTypeSelect: "follow", combinationCycleMinutesInput: "60" };
  for (const [id, value] of Object.entries(values)) node(id).value = value;
  return { state, context, node, calls, toasts, response: (value) => { response = value; } };
}
const event = { preventDefault() {} };
for (const language of ["zh", "en"]) {
  {
    const h = harness(language);
    h.context.updateRuleFields();
    assert.equal(h.node("targetAddressWrap").hidden, false);
    assert.equal(h.node("targetLabelInput").required, true);
    assert.equal(h.node("thresholdWrap").hidden, true);
    assert.equal(h.node("tokenAddressInput").placeholder, h.context.t("tokenOptional"));
    assert.ok(h.node("ruleDescription").textContent.includes(h.context.t("interactionRuleMetric", { used: 0, limit: 1 })));
    await h.context.createRule(event);
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].path, "/api/watch-rules");
    assert.equal(h.calls[0].body.rule_type, "address_interaction");
    assert.equal(h.calls[0].body.target_address, h.node("targetAddressInput").value);
    assert.equal(h.calls[0].body.target_label, "local treasury");
    assert.equal(h.calls[0].body.token_address, null);
    assert.equal(h.calls[0].body.threshold, "0");
    assert.equal(h.calls[0].body.chain_key, "ethereum", "BSC market scope must not close other-chain wallet interactions");
    assert.equal(h.calls[0].body.notification_language, language);
    scenarios += 1;
  }
  {
    const h = harness(language);
    h.node("targetAddressInput").value = "";
    await h.context.createRule(event);
    assert.equal(h.calls.length, 0);
    assert.equal(h.toasts.at(-1), h.context.t("interactionTargetRequired"));
    h.node("targetAddressInput").value = "0x2222222222222222222222222222222222222222";
    h.node("targetLabelInput").value = "";
    await h.context.createRule(event);
    assert.equal(h.calls.length, 0);
    assert.equal(h.toasts.at(-1), h.context.t("targetNoteRequired"));
    scenarios += 1;
  }
  {
    const h = harness(language);
    h.context.updateCombinationMemberFields();
    assert.equal(h.node("combinationTargetAddressWrap").hidden, false);
    assert.equal(h.node("combinationTargetLabelInput").required, true);
    assert.equal(h.node("combinationThresholdWrap").hidden, true);
    assert.equal(h.node("combinationTokenAddressInput").placeholder, h.context.t("tokenOptional"));
    h.context.addCombinationMember();
    assert.equal(h.state.combinationMembers.length, 1);
    const interaction = h.state.combinationMembers[0];
    assert.equal(interaction.rule.chain_key, "base");
    assert.equal(interaction.rule.token_address, null);
    assert.equal(interaction.required_trigger_count, 2);
    h.node("combinationRuleTypeSelect").value = "balance_change";
    h.node("combinationTargetAddressInput").value = "";
    h.node("combinationTargetLabelInput").value = "";
    h.context.addCombinationMember();
    const draft = JSON.stringify(h.state.combinationMembers);
    h.response({ status: 503, data: { code: "capacity_not_ready" } });
    await assert.rejects(h.context.createCombinationRule(event), (error) => {
      assert.equal(error.code, "capacity_not_ready");
      assert.equal(error.message, h.context.t("capacityNotReadyNotice"));
      return true;
    });
    assert.equal(h.calls.length, 1, "A rejected purchase/configuration must not retry automatically");
    assert.equal(JSON.stringify(h.state.combinationMembers), draft, "Rejected admission must retain the interaction member and full draft");
    h.response({ status: 200, data: {} });
    await h.context.createCombinationRule(event);
    const payload = h.calls[1].body;
    assert.equal(payload.members.length, 2);
    assert.equal(payload.members.filter((member) => member.rule.rule_type === "address_interaction").length, 1);
    assert.equal(payload.members[0].rule.target_address, interaction.rule.target_address);
    assert.equal(payload.members[0].rule.chain_key, "base");
    assert.equal(payload.members[0].required_trigger_count, 2);
    assert.equal(payload.members[0].rule.notification_language, language);
    assert.equal(payload.members[0].rule.delivery_mode, "realtime");
    assert.equal(payload.members[0].rule.threshold, "0");
    assert.equal(h.state.combinationMembers.length, 0, "Only confirmed successful submission clears a draft");
    scenarios += 1;
  }
}
console.log(`H5 retained interaction acceptance OK: ${scenarios} scenarios`);
