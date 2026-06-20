const state = {
  walletAddress: "",
  deboxUserId: "",
  profile: null,
  plans: [],
  ruleTypes: [],
  chains: [],
  selectedPlan: "standard",
  entitlement: null,
  groups: [],
};

const $ = (id) => document.getElementById(id);

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function toast(message) {
  const node = $("toast");
  node.textContent = message;
  clearTimeout(toast.hideTimer);
  node.classList.remove("leaving");
  node.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => {
    node.classList.add("leaving");
    clearTimeout(toast.hideTimer);
    toast.hideTimer = setTimeout(() => {
      node.hidden = true;
      node.classList.remove("leaving");
    }, 260);
  }, 3600);
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    ...options,
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.detail || data.message || "请求失败");
  }
  return data;
}

function walletProvider() {
  return window.deboxWallet || window.ethereum || null;
}

function shortAddress(address) {
  if (!address) return "-";
  return `${address.slice(0, 8)}...${address.slice(-6)}`;
}

function profileData(profile) {
  return typeof profile?.data === "object" && profile.data ? profile.data : profile || {};
}

function deboxUserIdFromProfile(profile) {
  const data = profileData(profile);
  return data.user_id || data.userId || data.uid || data.id || "";
}

function profileName(profile) {
  const data = profileData(profile);
  return data.name || data.nickname || data.user_name || "DeBox 用户";
}

function normalizeAvatarUrl(value) {
  const url = String(value || "").trim();
  if (!url) return "";
  if (/^https?:\/\//i.test(url)) return url;
  if (url.startsWith("//")) return `https:${url}`;
  if (url.startsWith("ipfs://")) return `https://ipfs.io/ipfs/${url.slice(7)}`;
  if (url.startsWith("/")) return `https://data.debox.pro${url}`;
  return url;
}

function profileAvatar(profile) {
  const data = profileData(profile);
  return normalizeAvatarUrl(
    data.pic ||
      data.avatar ||
      data.avatar_url ||
      data.avatarUrl ||
      data.headimgurl ||
      data.headImgUrl ||
      data.icon ||
      ""
  );
}

function profileInitial(profile) {
  return profileName(profile).trim().slice(0, 1).toUpperCase() || "D";
}

function currentPlan() {
  return state.entitlement?.plan || null;
}

function updateConnectionButton() {
  $("connectWalletBtn").textContent = state.deboxUserId ? "断开连接" : "连接钱包";
}

function resetConnectionState() {
  state.walletAddress = "";
  state.deboxUserId = "";
  state.profile = null;
  state.entitlement = null;
  state.groups = [];
  $("walletAddressInput").value = "";
  $("profileBox").innerHTML = "尚未连接钱包";
  $("subscriptionBox").innerHTML = "连接钱包后查看订阅状态";
  $("rulesList").innerHTML = "";
  $("groupsList").innerHTML = "";
  $("balanceBox").innerHTML = "还没有查询余额。";
  $("summaryCapability").textContent = "未连接";
  $("summaryCapability").classList.add("muted");
  updateConnectionButton();
}

function showIdentityModal() {
  $("identityModal").hidden = false;
}

function renderChains() {
  $("chainSelect").innerHTML = state.chains
    .map((chain) => `<option value="${escapeHtml(chain.key)}">${escapeHtml(chain.name)}</option>`)
    .join("");
}

function renderRuleTypes() {
  $("ruleTypeSelect").innerHTML = state.ruleTypes
    .map((rule) => `<option value="${escapeHtml(rule.code)}">${escapeHtml(rule.label)}</option>`)
    .join("");
  updateRuleFields();
}

function renderPlans() {
  $("plansGrid").innerHTML = state.plans
    .map((plan) => {
      const active = plan.code === state.selectedPlan ? " active" : "";
      const price = plan.price === "0" ? "免费" : `${plan.price} ${plan.asset || "USDT"}`;
      return `
        <button class="plan-card${active}" type="button" data-plan="${escapeHtml(plan.code)}">
          <span>${escapeHtml(plan.name)}</span>
          <strong>${escapeHtml(price)}</strong>
          <small>${escapeHtml(plan.description)}</small>
        </button>
      `;
    })
    .join("");
  document.querySelectorAll("[data-plan]").forEach((button) => {
    button.addEventListener("click", () => {
      state.selectedPlan = button.dataset.plan;
      renderPlans();
      loadPaymentConfig();
    });
  });
}

function renderProfile() {
  if (!state.walletAddress) {
    $("profileBox").innerHTML = "尚未连接钱包";
    return;
  }
  const avatar = profileAvatar(state.profile);
  const initial = profileInitial(state.profile);
  $("profileBox").innerHTML = `
    <div class="profile-row">
      ${
        avatar
          ? `<img src="${escapeHtml(avatar)}" alt="" referrerpolicy="no-referrer" onerror="this.hidden=true;this.nextElementSibling.hidden=false;" /><span class="profile-avatar-fallback" hidden>${escapeHtml(initial)}</span>`
          : `<span class="profile-avatar-fallback">${escapeHtml(initial)}</span>`
      }
      <div>
        <strong>${escapeHtml(profileName(state.profile))}</strong>
        <span>${escapeHtml(shortAddress(state.walletAddress))}</span>
        <span>DeBox ID: ${escapeHtml(state.deboxUserId || "-")}</span>
      </div>
    </div>
  `;
}

function renderSubscription() {
  const box = $("subscriptionBox");
  const plan = currentPlan();
  if (!state.entitlement || !plan) {
    box.innerHTML = "还没有有效订阅";
    return;
  }
  const sub = state.entitlement.subscription || {};
  box.innerHTML = `
    <div class="metric-row">
      <strong>${escapeHtml(plan.name)}</strong>
      <span>剩余 ${escapeHtml(state.entitlement.days_remaining)} 天</span>
    </div>
    <div class="mini-grid">
      <span>钱包 ${state.entitlement.wallet_count} / ${plan.wallet_limit}</span>
      <span>规则 ${state.entitlement.rule_count} / ${plan.rule_limit}</span>
      <span>群 ${state.entitlement.group_count} / ${plan.group_limit}</span>
    </div>
    <small class="muted">到期：${escapeHtml(sub.expires_at || "-")}</small>
  `;
  fillSummaryForm();
}

function renderGroups() {
  const options = state.groups.length
    ? state.groups.map((group) => `<option value="${escapeHtml(group.gid)}">${escapeHtml(group.name || group.gid)}</option>`).join("")
    : `<option value="">暂无已绑定群</option>`;
  $("groupTargetSelect").innerHTML = options;
  $("summaryGroupSelect").innerHTML = options;

  if (!state.groups.length) {
    $("groupsList").innerHTML = `<div class="notice muted">专业版可绑定群，用于群通知和群每日摘要。</div>`;
  } else {
    $("groupsList").innerHTML = state.groups
      .map(
        (group) => `
          <div class="list-item">
            <div>
              <strong>${escapeHtml(group.name || group.gid)}</strong>
              <span>GID: ${escapeHtml(group.gid)}</span>
            </div>
            <button class="secondary" type="button" data-delete-group="${escapeHtml(group.id)}">删除</button>
          </div>
        `
      )
      .join("");
  }
  document.querySelectorAll("[data-delete-group]").forEach((button) => {
    button.addEventListener("click", () => deleteGroup(button.dataset.deleteGroup));
  });
  updateTargetVisibility();
  updateSummaryTargetVisibility();
}

function ruleLabel(code) {
  return state.ruleTypes.find((rule) => rule.code === code)?.label || code;
}

function renderRules() {
  const rules = state.entitlement?.rules || [];
  if (!rules.length) {
    $("rulesList").innerHTML = `<div class="notice muted">还没有监控规则。</div>`;
    return;
  }
  $("rulesList").innerHTML = rules
    .map(
      (rule) => `
        <div class="list-item">
          <div>
            <strong>${escapeHtml(ruleLabel(rule.rule_type))} / ${escapeHtml(rule.chain_key)}</strong>
            <span>${escapeHtml(shortAddress(rule.wallet_address))} · 阈值 ${escapeHtml(rule.threshold)}</span>
            <small class="muted">${escapeHtml(rule.notification_chat_type === "group" ? rule.notification_label || rule.notification_chat_id : "私聊通知")}</small>
          </div>
          <button class="secondary" type="button" data-delete-rule="${escapeHtml(rule.id)}">删除</button>
        </div>
      `
    )
    .join("");
  document.querySelectorAll("[data-delete-rule]").forEach((button) => {
    button.addEventListener("click", () => deleteRule(button.dataset.deleteRule));
  });
}

function fillSummaryForm() {
  const settings = state.entitlement?.summary_settings || {};
  $("summaryEnabledInput").checked = Boolean(settings.enabled);
  $("summaryTimeInput").value = settings.time || "20:00";
  $("summaryTimezoneInput").value = settings.timezone || "Asia/Shanghai";
  $("summaryTargetSelect").value = settings.chat_type || "private";
  $("summaryLabelInput").value = settings.label || "";
  const plan = currentPlan();
  $("summaryCapability").textContent = plan?.daily_summary ? "可用" : "当前套餐不可用";
  $("summaryCapability").classList.toggle("muted", !plan?.daily_summary);
  updateSummaryTargetVisibility();
}

function updateRuleFields() {
  const type = $("ruleTypeSelect").value;
  const needsTarget = type === "approval_change" || type === "address_interaction";
  $("targetAddressWrap").hidden = !needsTarget;
  $("targetLabelWrap").hidden = !needsTarget;
  $("tokenAddressInput").placeholder =
    type === "approval_change" ? "必填，授权代币合约" : "可选，留空监控原生资产";
}

function updateTargetVisibility() {
  $("groupTargetWrap").hidden = $("targetTypeSelect").value !== "group";
}

function updateSummaryTargetVisibility() {
  $("summaryGroupWrap").hidden = $("summaryTargetSelect").value !== "group";
}

async function loadBootData() {
  const [planPayload, chains] = await Promise.all([api("/api/plans"), api("/api/chains")]);
  state.plans = planPayload.plans || planPayload;
  state.ruleTypes = planPayload.rule_types || [];
  state.chains = chains;
  renderPlans();
  renderChains();
  renderRuleTypes();
}

async function connectWallet() {
  const provider = walletProvider();
  if (!provider?.request) {
    toast("当前浏览器没有检测到 DeBox 钱包或 EVM 钱包。");
    return;
  }
  const accounts = await provider.request({ method: "eth_requestAccounts" });
  state.walletAddress = accounts?.[0] || "";
  $("walletAddressInput").value = state.walletAddress;

  let profile = null;
  try {
    profile = await provider.request({ method: "debox_getUserInfo" });
  } catch (_) {
    profile = null;
  }
  if (!profile && state.walletAddress) {
    try {
      profile = await api(`/api/debox/user?wallet_address=${encodeURIComponent(state.walletAddress)}`);
    } catch (_) {
      profile = null;
    }
  }
  const deboxUserId = deboxUserIdFromProfile(profile);
  if (!deboxUserId) {
    resetConnectionState();
    showIdentityModal();
    return;
  }
  state.profile = profile;
  state.deboxUserId = deboxUserId;
  renderProfile();
  updateConnectionButton();
  await refreshAccount();
  toast("钱包已连接");
}

function disconnectWallet() {
  resetConnectionState();
  toast("已断开连接");
}

async function toggleWalletConnection() {
  const button = $("connectWalletBtn");
  button.classList.add("is-pressing");
  setTimeout(() => button.classList.remove("is-pressing"), 180);
  await new Promise((resolve) => setTimeout(resolve, 140));
  if (state.deboxUserId) {
    disconnectWallet();
    return;
  }
  await connectWallet();
}

async function refreshAccount() {
  if (!state.deboxUserId) return;
  const current = await api(`/api/subscription/current?debox_user_id=${encodeURIComponent(state.deboxUserId)}`);
  state.entitlement = current;
  state.groups = current.groups || [];
  renderSubscription();
  renderGroups();
  renderRules();
  await loadPaymentConfig();
}

async function loadPaymentConfig() {
  const status = $("paymentStatus");
  try {
    const config = await api(`/api/payment/config?plan_code=${encodeURIComponent(state.selectedPlan)}`);
    if (state.selectedPlan === "free") {
      status.textContent = "免费体验无需支付。";
    } else if (config.mode !== "live") {
      status.textContent = "当前为预览模式，不会发起真实支付。";
    } else if (!config.ready) {
      status.textContent = `支付配置缺少：${config.missing.join(", ")}`;
    } else {
      status.textContent = `${config.total_amount} ${config.asset} / ${config.chain_name}`;
    }
  } catch (error) {
    status.textContent = error.message;
  }
}

async function startFreeTrial() {
  if (!state.deboxUserId) {
    toast("请先连接钱包。");
    return;
  }
  await api("/api/subscription/free-trial", {
    method: "POST",
    body: JSON.stringify({ debox_user_id: state.deboxUserId }),
  });
  await refreshAccount();
  toast("免费体验已开启");
}

async function payOrRenew() {
  if (!state.deboxUserId || !state.walletAddress) {
    toast("请先连接钱包。");
    return;
  }
  if (state.selectedPlan === "free") {
    await startFreeTrial();
    return;
  }
  const config = await api(`/api/payment/config?plan_code=${encodeURIComponent(state.selectedPlan)}`);
  if (config.mode !== "live") {
    toast("当前是预览模式，未发起真实支付。");
    return;
  }
  const provider = walletProvider();
  await provider.request({ method: "wallet_switchEthereumChain", params: [{ chainId: config.chain_id_hex }] });
  const prepared = await api("/api/payment/prepare", {
    method: "POST",
    body: JSON.stringify({
      payer_address: state.walletAddress,
      debox_user_id: state.deboxUserId,
      plan_code: state.selectedPlan,
    }),
  });
  const txHash = await provider.request({
    method: "eth_sendTransaction",
    params: [prepared.transactions[0].request],
  });
  await api("/api/payment/verify", {
    method: "POST",
    body: JSON.stringify({ order_id: prepared.order.id, tx_hash: txHash }),
  });
  await refreshAccount();
  toast("订阅已生效");
}

async function lookupToken() {
  const token = $("tokenAddressInput").value.trim();
  if (!token) {
    $("tokenInfoBox").textContent = "填写代币合约后可识别 Token 信息。";
    return;
  }
  try {
    const data = await api(`/api/debox/token?contract_address=${encodeURIComponent(token)}&chain_key=${encodeURIComponent($("chainSelect").value)}`);
    const source = profileData(data);
    $("tokenInfoBox").textContent = `识别结果：${source.name || "-"} (${source.symbol || "-"}) · 精度 ${source.decimal || source.decimals || "-"}`;
  } catch (error) {
    $("tokenInfoBox").textContent = `代币识别失败：${error.message}`;
  }
}

async function queryBalance() {
  const address = $("walletAddressInput").value.trim();
  if (!address) {
    toast("请填写钱包地址。");
    return;
  }
  const query = new URLSearchParams({
    address,
    chain_key: $("chainSelect").value,
  });
  const token = $("tokenAddressInput").value.trim();
  if (token) query.set("token_address", token);
  const data = await api(`/api/chain/balance?${query.toString()}`);
  $("balanceBox").innerHTML = `当前余额：<strong>${escapeHtml(data.value)} ${escapeHtml(data.symbol)}</strong> · ${escapeHtml(data.chain_name)}`;
}

async function createRule(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast("请先连接钱包。");
    return;
  }
  const targetType = $("targetTypeSelect").value;
  const selectedGroup = $("groupTargetSelect").selectedOptions[0];
  await api("/api/watch-rules", {
    method: "POST",
    body: JSON.stringify({
      chain_key: $("chainSelect").value,
      wallet_address: $("walletAddressInput").value.trim(),
      token_address: $("tokenAddressInput").value.trim() || null,
      target_address: $("targetAddressInput").value.trim() || null,
      target_label: $("targetLabelInput").value.trim(),
      rule_type: $("ruleTypeSelect").value,
      threshold: $("thresholdInput").value || "0",
      debox_user_id: state.deboxUserId,
      notification_chat_type: targetType,
      notification_chat_id: targetType === "group" ? $("groupTargetSelect").value : "",
      notification_label: targetType === "group" && selectedGroup ? selectedGroup.textContent : "",
    }),
  });
  await refreshAccount();
  toast("监控规则已创建");
}

async function deleteRule(ruleId) {
  await api(`/api/watch-rules/${ruleId}?debox_user_id=${encodeURIComponent(state.deboxUserId)}`, { method: "DELETE" });
  await refreshAccount();
  toast("监控规则已删除");
}

async function saveSummary(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast("请先连接钱包。");
    return;
  }
  await api("/api/subscription/summary-settings", {
    method: "POST",
    body: JSON.stringify({
      debox_user_id: state.deboxUserId,
      enabled: $("summaryEnabledInput").checked,
      push_time: $("summaryTimeInput").value || "20:00",
      timezone: $("summaryTimezoneInput").value.trim() || "Asia/Shanghai",
      chat_type: $("summaryTargetSelect").value,
      chat_id: $("summaryTargetSelect").value === "group" ? $("summaryGroupSelect").value : "",
      label: $("summaryLabelInput").value.trim(),
    }),
  });
  await refreshAccount();
  toast("每日摘要设置已保存");
}

async function addGroup(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast("请先连接钱包。");
    return;
  }
  await api("/api/notification-groups", {
    method: "POST",
    body: JSON.stringify({
      debox_user_id: state.deboxUserId,
      wallet_address: state.walletAddress,
      gid: $("groupIdInput").value.trim(),
      label: $("groupLabelInput").value.trim(),
    }),
  });
  $("groupIdInput").value = "";
  $("groupLabelInput").value = "";
  await refreshAccount();
  toast("群通知已绑定");
}

async function deleteGroup(groupId) {
  await api(`/api/notification-groups/${groupId}?debox_user_id=${encodeURIComponent(state.deboxUserId)}`, { method: "DELETE" });
  await refreshAccount();
  toast("群通知已删除");
}

function bindEvents() {
  $("connectWalletBtn").addEventListener("click", toggleWalletConnection);
  $("freeTrialBtn").addEventListener("click", startFreeTrial);
  $("payBtn").addEventListener("click", payOrRenew);
  $("refreshRulesBtn").addEventListener("click", refreshAccount);
  $("queryBalanceBtn").addEventListener("click", queryBalance);
  $("ruleForm").addEventListener("submit", createRule);
  $("groupForm").addEventListener("submit", addGroup);
  $("summaryForm").addEventListener("submit", saveSummary);
  $("targetTypeSelect").addEventListener("change", updateTargetVisibility);
  $("summaryTargetSelect").addEventListener("change", updateSummaryTargetVisibility);
  $("ruleTypeSelect").addEventListener("change", updateRuleFields);
  $("tokenAddressInput").addEventListener("blur", lookupToken);
  $("chainSelect").addEventListener("change", lookupToken);
  $("identityModalClose").addEventListener("click", () => {
    $("identityModal").hidden = true;
  });
}

async function boot() {
  bindEvents();
  updateTargetVisibility();
  updateSummaryTargetVisibility();
  updateConnectionButton();
  await loadBootData();
  await loadPaymentConfig();
}

boot().catch((error) => {
  toast(error.message);
});
