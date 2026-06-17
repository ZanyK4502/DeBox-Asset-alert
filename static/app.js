let deboxUser = null;
let paymentConfig = null;
let currentEntitlement = null;
let selectedPlanCode = "standard";

const BSC_CHAIN_ID = "0x38";

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function showResult(message, error = false) {
  const result = document.querySelector("#action-result");
  result.textContent = message;
  result.classList.toggle("error", error);
}

function showPaymentResult(message, error = false) {
  const result = document.querySelector("#payment-result");
  result.textContent = message;
  result.classList.toggle("error", error);
}

async function request(url, options) {
  const response = await fetch(url, options);
  const data = await response.json();
  if (!response.ok) throw new Error(data.detail || "请求失败");
  return data;
}

async function ensureBscNetwork() {
  const chainId = await window.deboxWallet.request({ method: "eth_chainId" });
  if (String(chainId).toLowerCase() === BSC_CHAIN_ID) return;

  await window.deboxWallet.request({
    method: "wallet_switchEthereumChain",
    params: [{ chainId: BSC_CHAIN_ID }],
  });

  const switchedChainId = await window.deboxWallet.request({ method: "eth_chainId" });
  if (String(switchedChainId).toLowerCase() !== BSC_CHAIN_ID) {
    throw new Error("请切换到 BNB Chain 后继续。");
  }
}

async function loadPayment(planCode = selectedPlanCode) {
  paymentConfig = await request(`/api/payment/config?plan_code=${encodeURIComponent(planCode)}`);
  selectedPlanCode = paymentConfig.plan_code;
  document.querySelector("#payment-breakdown").innerHTML = `
    <div><strong>${escapeHtml(paymentConfig.total_amount)} ${escapeHtml(paymentConfig.asset)}</strong><span>用户支付总额</span></div>
    <div><strong>${escapeHtml(paymentConfig.recipient_address)}</strong><span>收款地址</span></div>
  `;
  if (!paymentConfig.ready) {
    showPaymentResult(`支付尚未配置：${paymentConfig.missing.join("、")}`, true);
  }
}

function currentDeBoxUserId() {
  return deboxUser?.uid || "";
}

async function loadPlans() {
  const userParam = currentDeBoxUserId()
    ? `?debox_user_id=${encodeURIComponent(currentDeBoxUserId())}`
    : "";
  const [plans, current] = await Promise.all([
    request("/api/plans"),
    request(`/api/subscription/current${userParam}`),
  ]);
  currentEntitlement = current;
  const summary = document.querySelector("#entitlement-summary");
  summary.textContent = current.plan
    ? `${current.plan.name}：已使用 ${current.rule_count} / ${current.plan.rule_limit} 条规则，到期时间 ${current.subscription.expires_at}`
    : "当前没有有效订阅，无法继续创建监控规则。";
  document.querySelector("#plan-cards").innerHTML = plans.map((plan) => `
    <div class="plan-card ${plan.code === selectedPlanCode ? "selected" : ""}">
      <strong>${escapeHtml(plan.name)}</strong>
      <span class="price">${plan.price === "0" ? "免费" : `${escapeHtml(plan.price)} ${escapeHtml(plan.asset)}`}</span>
      <span>${escapeHtml(plan.description)}</span>
      <span>规则上限：${escapeHtml(plan.rule_limit)}</span>
      <span>群通知：${plan.group_notifications ? "支持" : "不支持"}</span>
      ${plan.code === "free"
        ? ""
        : `<button class="${plan.code === selectedPlanCode ? "" : "secondary"}" type="button" data-plan-code="${escapeHtml(plan.code)}">${plan.code === selectedPlanCode ? "已选择" : "选择套餐"}</button>`}
    </div>
  `).join("");
}

document.querySelector("#plan-cards").addEventListener("click", async (event) => {
  const button = event.target.closest("button[data-plan-code]");
  if (!button) return;
  button.disabled = true;
  try {
    selectedPlanCode = button.dataset.planCode;
    await Promise.all([loadPayment(selectedPlanCode), loadPlans()]);
    showPaymentResult(`已选择${paymentConfig.plan_name}。`);
  } catch (error) {
    showPaymentResult(error.message, true);
  } finally {
    button.disabled = false;
  }
});

function formValues() {
  return {
    wallet_address: document.querySelector("#wallet-address").value.trim(),
    token_address: document.querySelector("#token-address").value.trim() || null,
    rule_type: document.querySelector("#rule-type").value,
    threshold: document.querySelector("#threshold").value.trim() || "0",
    notification_chat_type: "private",
    notification_chat_id: currentDeBoxUserId(),
    debox_user_id: currentDeBoxUserId(),
  };
}

document.querySelector("#query-balance").addEventListener("click", async (event) => {
  const button = event.currentTarget;
  const values = formValues();
  if (!values.wallet_address) return showResult("请先输入钱包地址。", true);
  button.disabled = true;
  try {
    const params = new URLSearchParams({ address: values.wallet_address });
    if (values.token_address) params.set("token_address", values.token_address);
    const data = await request(`/api/chain/balance?${params}`);
    showResult(`当前余额：${data.value} ${data.symbol}`);
  } catch (error) {
    showResult(error.message, true);
  } finally {
    button.disabled = false;
  }
});

document.querySelector("#watch-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  try {
    const data = await request("/api/watch-rules", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(formValues()),
    });
    showResult(`监控已创建，当前余额：${data.current_balance.value} ${data.current_balance.symbol}`);
    await loadPlans();
  } catch (error) {
    showResult(error.message, true);
  } finally {
    button.disabled = false;
  }
});

document.querySelector("#connect-wallet").addEventListener("click", async (event) => {
  const button = event.currentTarget;
  if (!window.deboxWallet) {
    return showPaymentResult("请在 DeBox App 内打开部署后的 H5 页面。", true);
  }
  button.disabled = true;
  try {
    await window.deboxWallet.request({
      method: "wallet_requestPermissions",
      params: [{ eth_accounts: { debox_getUserInfo: {} } }],
    });
    deboxUser = await window.deboxWallet.request({
      method: "debox_getUserInfo",
      params: [],
    });
    await ensureBscNetwork();
    button.textContent = `${deboxUser.name || "DeBox 用户"} 已连接`;
    document.querySelector("#pay-subscription").disabled =
      !(paymentConfig.ready && paymentConfig.payment_enabled);
    showPaymentResult(`钱包已连接并切换至 BNB Chain：${deboxUser.address}`);
    await loadPlans();
  } catch (error) {
    showPaymentResult(error.message || "钱包连接失败", true);
  } finally {
    button.disabled = false;
  }
});

document.querySelector("#pay-subscription").addEventListener("click", async (event) => {
  const button = event.currentTarget;
  if (!deboxUser) return showPaymentResult("请先连接 DeBox 钱包。", true);
  if (!paymentConfig.payment_enabled) return showPaymentResult("支付暂未开放。", true);
  button.disabled = true;
  try {
    const confirmed = window.confirm(
      `即将发起链上交易：支付 ${paymentConfig.total_amount} ${paymentConfig.asset} 至配置的收款地址。是否继续？`
    );
    if (!confirmed) {
      showPaymentResult("已取消，未创建订单，也未发起链上授权。");
      return;
    }
    await ensureBscNetwork();
    const prepared = await request("/api/payment/prepare", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        payer_address: deboxUser.address,
        debox_user_id: deboxUser.uid,
        plan_code: selectedPlanCode,
      }),
    });
    let paymentHash = "";
    for (const transaction of prepared.transactions) {
      showPaymentResult(`请在钱包中确认：${transaction.label}`);
      const hash = await window.deboxWallet.request({
        method: "eth_sendTransaction",
        params: [transaction.request],
      });
      if (transaction.kind === "payment") paymentHash = hash;
    }
    showPaymentResult("交易已提交，正在进行链上验证...");
    const verified = await request("/api/payment/verify", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ order_id: prepared.order.id, tx_hash: paymentHash }),
    });
    showPaymentResult(`订阅已生效，到期时间：${verified.subscription.expires_at}`);
    await loadPlans();
  } catch (error) {
    showPaymentResult(error.message || "支付未完成", true);
  } finally {
    button.disabled = false;
  }
});

Promise.all([loadPayment(), loadPlans()])
  .then(() => {
    document.querySelector("#health").textContent = "服务正常";
  })
  .catch((error) => {
    document.querySelector("#health").textContent = "服务异常";
    showResult(error.message, true);
  });
