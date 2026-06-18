const state = {
  walletAddress: "",
  deboxUserId: "",
  profile: null,
  plans: [],
  chains: [],
  selectedPlan: "standard",
  entitlement: null,
  groups: [],
};

const $ = (id) => document.getElementById(id);

function toast(message) {
  const node = $("toast");
  node.textContent = message;
  node.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => {
    node.hidden = true;
  }, 4200);
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

function deboxUserIdFromProfile(profile, fallback) {
  if (!profile) return fallback;
  const data = typeof profile.data === "object" && profile.data ? profile.data : profile;
  return data.user_id || data.userId || data.uid || data.id || fallback;
}

function profileName(profile) {
  if (!profile) return "DeBox 用户";
  const data = typeof profile.data === "object" && profile.data ? profile.data : profile;
  return data.name || data.nickname || data.user_name || "DeBox 用户";
}

function profileAvatar(profile) {
  if (!profile) return "";
  const data = typeof profile.data === "object" && profile.data ? profile.data : profile;
  return data.pic || data.avatar || data.avatar_url || "";
}

function setLoading(button, loading) {
  if (!button) return;
  button.disabled = loading;
  if (loading) button.dataset.text = button.textContent;
  button.textContent = loading ? "处理中..." : button.dataset.text || button.textContent;
}

async function loadBootData() {
  const [health, plans, chains] = await Promise.all([
    api("/api/health"),
    api("/api/plans"),
    api("/api/chains"),
  ]);
  $("healthBadge").textContent = health.ok ? "服务正常" : "服务异常";
  state.plans = plans;
  state.chains = chains;
  renderPlans();
  renderChains();
}

function renderChains() {
  $("chainSelect").innerHTML = state.chains
    .map((chain) => `<option value="${chain.key}">${chain.name}</option>`)
    .join("");
}

function renderPlans() {
  $("plansGrid").innerHTML = state.plans
    .map((plan) => {
      const active = plan.code === state.selectedPlan ? " active" : "";
      const price = plan.price === "0" ? "免费" : `${plan.price} ${plan.asset || "USDT"}`;
      return `
        <button class="plan-card${active}" type="button" data-plan="${plan.code}">
          <strong>${plan.name}</strong>
          <p>${price} / ${plan.days} 天</p>
          <p class="muted">${plan.description}</p>
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
  $("profileBox").innerHTML = `
    ${avatar ? `<img src="${avatar}" alt="">` : ""}
    <div>
      <strong>${profileName(state.profile)}</strong>
      <div class="muted">${shortAddress(state.walletAddress)}</div>
      <div class="muted">DeBox ID: ${state.deboxUserId || "-"}</div>
    </div>
  `;
}

function renderSubscription() {
  const box = $("subscriptionBox");
  if (!state.entitlement || !state.entitlement.plan) {
    box.innerHTML = "还没有有效订阅";
    return;
  }
  const plan = state.entitlement.plan;
  const sub = state.entitlement.subscription || {};
  box.innerHTML = `
    <strong>${plan.name}</strong>
    <span>剩余 ${state.entitlement.days_remaining} 天</span>
    <span>规则 ${state.entitlement.rule_count} / ${plan.rule_limit}</span>
    <span>群通知 ${state.entitlement.group_count} / ${plan.group_limit}</span>
    <span class="muted">到期：${sub.expires_at || "-"}</span>
  `;
}

function renderGroups() {
  const groupSelect = $("groupTargetSelect");
  if (!state.groups.length) {
    groupSelect.innerHTML = `<option value="">暂无已绑定群</option>`;
    $("groupsList").innerHTML = `<div class="info-box muted">专业版可绑定群 ID，用于把监控通知发送到群里。</div>`;
    return;
  }
  groupSelect.innerHTML = state.groups
    .map((group) => `<option value="${group.gid}">${group.name || group.gid}</option>`)
    .join("");
  $("groupsList").innerHTML = state.groups
    .map(
      (group) => `
      <div class="list-item">
        <div>
          <strong>${group.name || group.gid}</strong>
          <span class="muted">GID: ${group.gid}</span>
        </div>
        <button class="ghost" type="button" data-delete-group="${group.id}">删除</button>
      </div>
    `
    )
    .join("");
  document.querySelectorAll("[data-delete-group]").forEach((button) => {
    button.addEventListener("click", () => deleteGroup(button.dataset.deleteGroup));
  });
}

function renderRules() {
  const rules = (state.entitlement && state.entitlement.rules) || [];
  if (!rules.length) {
    $("rulesList").innerHTML = `<div class="info-box muted">还没有监控规则。</div>`;
    return;
  }
  $("rulesList").innerHTML = rules
    .map(
      (rule) => `
      <div class="list-item">
        <div>
          <strong>${rule.token_address ? "代币余额" : "原生资产"} / ${rule.chain_key}</strong>
          <span class="muted">${shortAddress(rule.wallet_address)} · ${rule.rule_type} · 阈值 ${rule.threshold}</span>
          <div class="muted">通知：${rule.notification_chat_type === "group" ? rule.notification_label || rule.notification_chat_id : "私聊"}</div>
        </div>
        <button class="ghost" type="button" data-delete-rule="${rule.id}">删除</button>
      </div>
    `
    )
    .join("");
  document.querySelectorAll("[data-delete-rule]").forEach((button) => {
    button.addEventListener("click", () => deleteRule(button.dataset.deleteRule));
  });
}

function updateTargetVisibility() {
  const type = $("targetTypeSelect").value;
  $("groupTargetWrap").style.display = type === "group" ? "grid" : "none";
}

async function connectWallet() {
  const provider = walletProvider();
  if (!provider || !provider.request) {
    toast("当前浏览器没有检测到 DeBox 钱包或 EVM 钱包。");
    return;
  }
  const accounts = await provider.request({ method: "eth_requestAccounts" });
  state.walletAddress = accounts && accounts[0] ? accounts[0] : "";
  $("walletAddressInput").value = state.walletAddress;

  let profile = null;
  try {
    if (provider.request) {
      profile = await provider.request({ method: "debox_getUserInfo" });
    }
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
  state.profile = profile;
  state.deboxUserId = deboxUserIdFromProfile(profile, state.walletAddress);
  renderProfile();
  await refreshAccount();
  toast("钱包已连接");
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
  const tx = prepared.transactions[0].request;
  const txHash = await provider.request({ method: "eth_sendTransaction", params: [tx] });
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
    $("tokenInfoBox").textContent = "输入代币合约后会自动识别代币信息。";
    return;
  }
  try {
    const data = await api(`/api/debox/token?contract_address=${encodeURIComponent(token)}&chain_key=${encodeURIComponent($("chainSelect").value)}`);
    const source = typeof data.data === "object" && data.data ? data.data : data;
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
    token_address: $("tokenAddressInput").value.trim(),
    chain_key: $("chainSelect").value,
  });
  const data = await api(`/api/chain/balance?${query.toString()}`);
  $("balanceBox").innerHTML = `当前余额：<strong>${data.value} ${data.symbol}</strong> · ${data.chain_name}`;
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
  $("connectWalletBtn").addEventListener("click", connectWallet);
  $("freeTrialBtn").addEventListener("click", startFreeTrial);
  $("payBtn").addEventListener("click", payOrRenew);
  $("refreshRulesBtn").addEventListener("click", refreshAccount);
  $("queryBalanceBtn").addEventListener("click", queryBalance);
  $("ruleForm").addEventListener("submit", createRule);
  $("groupForm").addEventListener("submit", addGroup);
  $("targetTypeSelect").addEventListener("change", updateTargetVisibility);
  $("tokenAddressInput").addEventListener("blur", lookupToken);
  $("chainSelect").addEventListener("change", lookupToken);
}

async function boot() {
  bindEvents();
  updateTargetVisibility();
  await loadBootData();
  await loadPaymentConfig();
}

boot().catch((error) => {
  $("healthBadge").textContent = "服务异常";
  toast(error.message);
});
