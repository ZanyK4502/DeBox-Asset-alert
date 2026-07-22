const UI_LANGUAGE_STORAGE_KEY = "debox_asset_alert_h5_language";

function storedUiLanguage() {
  try {
    return localStorage.getItem(UI_LANGUAGE_STORAGE_KEY) === "en" ? "en" : "zh";
  } catch (_) {
    return "zh";
  }
}

const state = {
  uiLanguage: storedUiLanguage(),
  walletAddress: "",
  deboxUserId: "",
  profile: null,
  plans: [],
  ruleTypes: [],
  chains: [],
  selectedPlan: "standard",
  entitlement: null,
  groups: [],
  paymentConfig: null,
  paymentError: "",
  tokenInfo: null,
  tokenError: "",
  balanceInfo: null,
};

const $ = (id) => document.getElementById(id);
const I18N = window.H5_I18N;

function t(key, values = {}) {
  const dictionary = I18N[state.uiLanguage] || I18N.zh;
  const template = dictionary[key] ?? I18N.zh[key] ?? key;
  return String(template).replace(/\{([a-zA-Z0-9_]+)\}/g, (_, name) => String(values[name] ?? ""));
}

function localizedPlan(plan) {
  const localized = I18N.plans[state.uiLanguage]?.[plan?.code];
  return {
    name: localized?.[0] || plan?.name || "-",
    description: localized?.[1] || plan?.description || "",
  };
}

function localizedRuleLabel(code) {
  return I18N.rules[state.uiLanguage]?.[code] || code;
}

function localizedRuleDescription(code) {
  return I18N.ruleDescriptions[state.uiLanguage]?.[code] || "";
}

function localizedApiError(message) {
  const value = String(message || "").trim();
  if (state.uiLanguage === "zh" || !/[\u3400-\u9fff]/u.test(value)) return value || t("requestFailed");
  return t("requestFailed");
}

const CHAIN_LOGOS = {
  bsc: "/static/chains/bsc.png",
  ethereum: "/static/chains/ethereum.png",
  base: "/static/chains/base.png",
  polygon: "/static/chains/polygon.png",
  arbitrum: "/static/chains/arbitrum.png",
  optimism: "/static/chains/optimism.png",
};

const SUMMARY_TIMEZONES = new Set([
  "Asia/Shanghai",
  "Asia/Tokyo",
  "Asia/Bangkok",
  "Asia/Kolkata",
  "Europe/Berlin",
  "Europe/London",
  "America/New_York",
  "America/Los_Angeles",
  "UTC",
]);

function chainLogoSrc(chainKey) {
  return CHAIN_LOGOS[String(chainKey || "").toLowerCase()] || "";
}

function normalizeSummaryTimezone(value) {
  const timezone = String(value || "").trim();
  return SUMMARY_TIMEZONES.has(timezone) ? timezone : "Asia/Shanghai";
}

function parseDeBoxGroupLink(value) {
  const raw = String(value || "").trim();
  try {
    const url = new URL(raw);
    const host = url.hostname.toLowerCase();
    if ((host === "m.debox.pro" || host === "www.debox.pro" || host === "debox.pro") && url.pathname === "/group") {
      return url.searchParams.get("id")?.trim() || "";
    }
  } catch (_) {
    return "";
  }
  return "";
}

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function applyStaticTranslations() {
  document.documentElement.lang = state.uiLanguage === "en" ? "en" : "zh-CN";
  document.querySelectorAll("[data-i18n]").forEach((node) => {
    node.textContent = t(node.dataset.i18n);
  });
  document.querySelectorAll("[data-i18n-placeholder]").forEach((node) => {
    node.placeholder = t(node.dataset.i18nPlaceholder);
  });
  document.querySelectorAll("[data-i18n-aria-label]").forEach((node) => {
    node.setAttribute("aria-label", t(node.dataset.i18nAriaLabel));
  });
  document.querySelectorAll("[data-i18n-label]").forEach((label) => {
    const textNode = [...label.childNodes].find((node) => node.nodeType === Node.TEXT_NODE && node.textContent.trim());
    if (textNode) textNode.textContent = `\n            ${t(label.dataset.i18nLabel)}\n            `;
  });
  const toggle = $("languageToggleBtn");
  toggle.textContent = state.uiLanguage === "en" ? t("chinese") : "EN";
  toggle.setAttribute("aria-label", t(state.uiLanguage === "en" ? "switchToChinese" : "switchToEnglish"));
}

function renderLocalizedState() {
  applyStaticTranslations();
  updateConnectionButton();
  renderPlans();
  renderChains();
  renderRuleTypes();
  renderProfile();
  renderSubscription(false);
  renderSummaryCapability();
  renderGroups();
  renderRules();
  renderPaymentStatus();
  renderTokenInfo();
  renderBalanceInfo();
}

function toggleUiLanguage() {
  state.uiLanguage = state.uiLanguage === "en" ? "zh" : "en";
  try {
    localStorage.setItem(UI_LANGUAGE_STORAGE_KEY, state.uiLanguage);
  } catch (_) {
    // The current page still switches language when browser storage is unavailable.
  }
  renderLocalizedState();
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
  const { headers = {}, ...requestOptions } = options;
  const response = await fetch(path, {
    credentials: "same-origin",
    ...requestOptions,
    headers: { "Content-Type": "application/json", ...headers },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const message = response.status === 401
      ? t("sessionExpired")
      : localizedApiError(data.detail || data.message || t("requestFailed"));
    const error = new Error(message);
    error.status = response.status;
    if (response.status === 401 && path !== "/api/auth/session") {
      resetConnectionState();
    }
    throw error;
  }
  return data;
}

function walletProvider() {
  return window.deboxWallet || window.ethereum || null;
}

function utf8ToHex(value) {
  return `0x${[...new TextEncoder().encode(value)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("")}`;
}

async function signWalletMessage(provider, message, walletAddress) {
  const encodedMessage = utf8ToHex(message);
  try {
    return await provider.request({
      method: "personal_sign",
      params: [encodedMessage, walletAddress],
    });
  } catch (error) {
    if (error?.code === 4001) throw error;
    return provider.request({
      method: "personal_sign",
      params: [walletAddress, encodedMessage],
    });
  }
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
  return data.name || data.nickname || data.user_name || t("deboxUser");
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

function currentChain() {
  const key = $("chainSelect").value || state.chains[0]?.key || "";
  return state.chains.find((chain) => chain.key === key) || state.chains[0] || null;
}

function chainOptionHtml(chain) {
  const logo = chainLogoSrc(chain.key);
  return `
    ${logo ? `<img src="${escapeHtml(logo)}" alt="" />` : `<span class="chain-logo-fallback">${escapeHtml(String(chain.name || "?").slice(0, 1))}</span>`}
    <span>${escapeHtml(chain.name)}</span>
  `;
}

function renderChainPicker() {
  const selected = currentChain();
  const button = $("chainPickerButton");
  const menu = $("chainPickerMenu");
  if (!selected) {
    button.textContent = t("selectChain");
    menu.innerHTML = "";
    return;
  }
  button.innerHTML = `${chainOptionHtml(selected)}<span class="chain-picker-arrow">⌄</span>`;
  menu.innerHTML = state.chains
    .map(
      (chain) => `
        <button class="chain-picker-option${chain.key === selected.key ? " active" : ""}" type="button" role="option" aria-selected="${chain.key === selected.key}" data-chain="${escapeHtml(chain.key)}">
          ${chainOptionHtml(chain)}
        </button>
      `
    )
    .join("");
  document.querySelectorAll("[data-chain]").forEach((option) => {
    option.addEventListener("pointerdown", (event) => {
      event.preventDefault();
      event.stopPropagation();
      selectChain(option.dataset.chain);
    });
    option.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
    });
  });
}

function closeChainPicker() {
  $("chainPickerMenu").hidden = true;
  $("chainPickerButton").setAttribute("aria-expanded", "false");
}

function shieldTapThrough(duration = 260) {
  const shield = $("tapShield");
  clearTimeout(shieldTapThrough.timer);
  shield.hidden = false;
  shieldTapThrough.timer = setTimeout(() => {
    shield.hidden = true;
  }, duration);
}

function toggleChainPicker() {
  const menu = $("chainPickerMenu");
  const willOpen = menu.hidden;
  menu.hidden = !willOpen;
  $("chainPickerButton").setAttribute("aria-expanded", String(willOpen));
  if (willOpen) {
    const active = $("chainPickerMenu").querySelector(".chain-picker-option.active");
    active?.scrollIntoView({ block: "nearest" });
  }
}

function selectChain(chainKey) {
  $("chainSelect").value = chainKey;
  closeChainPicker();
  shieldTapThrough();
  $("chainSelect").dispatchEvent(new Event("change", { bubbles: true }));
  closeChainPicker();
  $("chainPickerButton").focus();
}

function moveChainSelection(direction) {
  if (!state.chains.length) return;
  const currentKey = $("chainSelect").value || state.chains[0].key;
  const currentIndex = Math.max(0, state.chains.findIndex((chain) => chain.key === currentKey));
  const nextIndex = (currentIndex + direction + state.chains.length) % state.chains.length;
  selectChain(state.chains[nextIndex].key);
  $("chainPickerMenu").hidden = false;
  $("chainPickerButton").setAttribute("aria-expanded", "true");
}

function handleChainPickerKeydown(event) {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    toggleChainPicker();
    return;
  }
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    moveChainSelection(event.key === "ArrowDown" ? 1 : -1);
    return;
  }
  if (event.key === "Escape") {
    closeChainPicker();
  }
}

function updateConnectionButton() {
  $("connectWalletBtn").textContent = state.deboxUserId ? t("disconnectWallet") : t("connectWallet");
}

function resetConnectionState() {
  state.walletAddress = "";
  state.deboxUserId = "";
  state.profile = null;
  state.entitlement = null;
  state.groups = [];
  state.paymentConfig = null;
  state.paymentError = "";
  state.tokenInfo = null;
  state.tokenError = "";
  state.balanceInfo = null;
  $("walletAddressInput").value = "";
  $("profileBox").innerHTML = t("noWallet");
  $("subscriptionBox").innerHTML = t("connectToView");
  $("rulesList").innerHTML = "";
  $("pausedRulesWrap").hidden = true;
  $("pausedRulesList").innerHTML = "";
  $("groupsList").innerHTML = "";
  renderTokenInfo();
  renderBalanceInfo();
  $("summaryCapability").textContent = t("notConnected");
  $("summaryCapability").classList.add("muted");
  renderPlans();
  updateConnectionButton();
}

function showIdentityModal() {
  $("identityModal").hidden = false;
}

function renderChains() {
  const selectedChain = $("chainSelect").value;
  $("chainSelect").innerHTML = state.chains
    .map((chain) => `<option value="${escapeHtml(chain.key)}">${escapeHtml(chain.name)}</option>`)
    .join("");
  if (state.chains.some((chain) => chain.key === selectedChain)) {
    $("chainSelect").value = selectedChain;
  }
  renderChainPicker();
}

function renderRuleTypes() {
  const selectedRuleType = $("ruleTypeSelect").value;
  $("ruleTypeSelect").innerHTML = state.ruleTypes
    .map((rule) => `<option value="${escapeHtml(rule.code)}">${escapeHtml(localizedRuleLabel(rule.code))}</option>`)
    .join("");
  if (state.ruleTypes.some((rule) => rule.code === selectedRuleType)) {
    $("ruleTypeSelect").value = selectedRuleType;
  }
  updateRuleFields();
}

function renderPlans() {
  const currentPaidPlan = ["standard", "professional"].includes(currentPlan()?.code)
    ? currentPlan().code
    : "";
  $("freeTrialBtn").disabled = Boolean(currentPaidPlan);
  $("plansGrid").innerHTML = state.plans
    .map((plan) => {
      const active = plan.code === state.selectedPlan ? " active" : "";
      const locked = Boolean(currentPaidPlan && plan.code !== currentPaidPlan);
      const text = localizedPlan(plan);
      const price = plan.price === "0" ? t("freePrice") : `${plan.price} ${plan.asset || "USDT"}`;
      return `
        <button class="plan-card${active}" type="button" data-plan="${escapeHtml(plan.code)}"${locked ? " disabled" : ""}>
          <span>${escapeHtml(text.name)}</span>
          <strong>${escapeHtml(price)}</strong>
          <small>${escapeHtml(text.description)}</small>
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
    $("profileBox").innerHTML = t("noWallet");
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

function renderSubscription(syncSummary = true) {
  const box = $("subscriptionBox");
  const plan = currentPlan();
  if (!state.entitlement || !plan) {
    box.innerHTML = state.deboxUserId ? t("noSubscription") : t("connectToView");
    return;
  }
  const sub = state.entitlement.subscription || {};
  const isFree = plan.code === "free";
  const planText = localizedPlan(plan);
  const freeHint =
    state.entitlement.paid_history && state.entitlement.fallback_free
      ? t("freeRestoreHint")
      : t("freeUpgradeHint");
  box.innerHTML = `
    <div class="metric-row">
      <strong>${escapeHtml(planText.name)}</strong>
      <span>${escapeHtml(isFree ? t("permanent") : t("remainingDays", { days: state.entitlement.days_remaining }))}</span>
    </div>
    <div class="mini-grid">
      <span>${escapeHtml(t("walletMetric", { used: state.entitlement.wallet_count, limit: plan.wallet_limit }))}</span>
      <span>${escapeHtml(t("ruleMetric", { used: state.entitlement.rule_count, limit: plan.rule_limit }))}</span>
      <span>${escapeHtml(t("groupMetric", { used: state.entitlement.group_count, limit: plan.group_limit }))}</span>
    </div>
    <small class="muted">${escapeHtml(isFree ? freeHint : t("expiresAt", { date: sub.expires_at || "-" }))}</small>
  `;
  if (syncSummary) fillSummaryForm();
}

function renderGroups() {
  if (!state.deboxUserId) {
    $("groupTargetSelect").innerHTML = `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
    $("summaryGroupSelect").innerHTML = `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
    $("groupsList").innerHTML = "";
    return;
  }
  const selectedRuleGroup = $("groupTargetSelect").value;
  const selectedSummaryGroup = $("summaryGroupSelect").value;
  const options = state.groups.length
    ? state.groups.map((group) => `<option value="${escapeHtml(group.gid)}">${escapeHtml(group.name || group.gid)}</option>`).join("")
    : `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
  $("groupTargetSelect").innerHTML = options;
  $("summaryGroupSelect").innerHTML = options;
  if (state.groups.some((group) => group.gid === selectedRuleGroup)) {
    $("groupTargetSelect").value = selectedRuleGroup;
  }
  if (state.groups.some((group) => group.gid === selectedSummaryGroup)) {
    $("summaryGroupSelect").value = selectedSummaryGroup;
  }

  if (!state.groups.length) {
    $("groupsList").innerHTML = `<div class="notice muted">${escapeHtml(t("groupsHint"))}</div>`;
  } else {
    $("groupsList").innerHTML = state.groups
      .map(
        (group) => `
          <div class="list-item">
            <div>
              <strong>${escapeHtml(group.name || group.gid)}</strong>
              <span>GID: ${escapeHtml(group.gid)}</span>
            </div>
            <button class="secondary" type="button" data-delete-group="${escapeHtml(group.id)}">${escapeHtml(t("delete"))}</button>
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
  return localizedRuleLabel(code);
}

function localizedPauseReason(rule, plan) {
  if (state.uiLanguage === "zh") return rule.pause_reason || t("rulePaused");
  if (!rule.enabled) return t("ruleClosed");
  if (!plan?.allowed_rule_types?.includes(rule.rule_type)) return t("planRuleUnsupported");
  if (rule.notification_chat_type === "group" && !plan.group_notification) return t("planGroupUnsupported");
  if (state.entitlement?.fallback_free && state.entitlement?.paid_history) return t("paidExpired");
  const reason = String(rule.pause_reason || "");
  if (reason.includes("\u89c4\u5219\u989d\u5ea6")) return t("ruleLimitExceeded");
  if (reason.includes("\u94b1\u5305\u989d\u5ea6")) return t("walletLimitExceeded");
  if (rule.can_select_free && plan?.code === "free") return t("selectFreeRule");
  return t("rulePaused");
}

function ruleLanguage(rule) {
  return rule?.notification_language === "en" ? "en" : "zh";
}

function canRestoreRule(rule, plan) {
  if (!pausedRuleCanRun(rule, plan)) return false;
  if (plan.code === "free") return Boolean(rule.can_select_free);
  return true;
}

function pausedRuleCanRun(rule, plan) {
  if (!plan || !rule) return false;
  if (!rule.enabled) return false;
  if (!plan.allowed_rule_types?.includes(rule.rule_type)) return false;
  if (rule.notification_chat_type === "group" && !plan.group_notification) return false;
  return true;
}

function ruleItemHtml(rule, paused = false) {
  const plan = currentPlan();
  const actionText = plan?.code === "free" ? t("setFreeMonitor") : t("restoreMonitor");
  const restoreAction =
    paused && canRestoreRule(rule, plan)
      ? `<button class="secondary" type="button" data-restore-rule="${escapeHtml(rule.id)}">${escapeHtml(actionText)}</button>`
      : "";
  return `
    <div class="list-item${paused ? " paused" : ""}">
      <div>
        <strong>${escapeHtml(ruleLabel(rule.rule_type))} / ${escapeHtml(rule.chain_key)}</strong>
        <span>${escapeHtml(t("ruleThreshold", { address: shortAddress(rule.wallet_address), threshold: rule.threshold }))}</span>
        <small class="muted">${escapeHtml(rule.notification_chat_type === "group" ? rule.notification_label || rule.notification_chat_id : t("privateNotification"))}</small>
        ${paused ? `<small class="pause-reason">${escapeHtml(localizedPauseReason(rule, plan))}</small>` : ""}
      </div>
      <div class="list-actions">
        <label class="rule-language-control">
          <span>${escapeHtml(t("notificationLanguage"))}</span>
          <select data-rule-language="${escapeHtml(rule.id)}" data-current-language="${ruleLanguage(rule)}" aria-label="${escapeHtml(t("notificationLanguage"))}">
            <option value="zh"${ruleLanguage(rule) === "zh" ? " selected" : ""}>${escapeHtml(t("chinese"))}</option>
            <option value="en"${ruleLanguage(rule) === "en" ? " selected" : ""}>English</option>
          </select>
        </label>
        ${restoreAction}
        <button class="secondary" type="button" data-delete-rule="${escapeHtml(rule.id)}">${escapeHtml(t("delete"))}</button>
      </div>
    </div>
  `;
}

function renderRules() {
  if (!state.deboxUserId) {
    $("rulesList").innerHTML = "";
    $("pausedRulesWrap").hidden = true;
    $("pausedRulesList").innerHTML = "";
    return;
  }
  const rules = state.entitlement?.active_rules || state.entitlement?.rules || [];
  const pausedRules = state.entitlement?.paused_rules || [];
  if (!rules.length) {
    $("rulesList").innerHTML = `<div class="notice muted">${escapeHtml(pausedRules.length ? t("noActiveRules") : t("noRules"))}</div>`;
  } else {
    $("rulesList").innerHTML = rules.map((rule) => ruleItemHtml(rule)).join("");
  }
  $("pausedRulesWrap").hidden = pausedRules.length === 0;
  $("deletePausedRulesBtn").disabled = pausedRules.length === 0;
  $("pausedRulesList").innerHTML = pausedRules.map((rule) => ruleItemHtml(rule, true)).join("");
  document.querySelectorAll("[data-delete-rule]").forEach((button) => {
    button.addEventListener("click", () => deleteRule(button.dataset.deleteRule));
  });
  document.querySelectorAll("[data-restore-rule]").forEach((button) => {
    button.addEventListener("click", () => restoreRule(button.dataset.restoreRule));
  });
  document.querySelectorAll("[data-rule-language]").forEach((select) => {
    select.addEventListener("change", () => updateRuleLanguage(select.dataset.ruleLanguage, select));
  });
}

function fillSummaryForm() {
  const settings = state.entitlement?.summary_settings || {};
  $("summaryEnabledInput").checked = Boolean(settings.enabled);
  $("summaryTimeInput").value = settings.time || "20:00";
  $("summaryTimezoneInput").value = normalizeSummaryTimezone(settings.timezone);
  $("summaryTargetSelect").value = settings.chat_type || "private";
  $("summaryLanguageInput").value = settings.language === "en" ? "en" : "zh";
  $("summaryLabelInput").value = settings.label || "";
  renderSummaryCapability();
  updateSummaryTargetVisibility();
}

function renderSummaryCapability() {
  const plan = currentPlan();
  const available = Boolean(state.deboxUserId && plan?.daily_summary);
  $("summaryCapability").textContent = state.deboxUserId
    ? (available ? t("available") : t("planUnavailable"))
    : t("notConnected");
  $("summaryCapability").classList.toggle("muted", !available);
}

function updateRuleFields() {
  const type = $("ruleTypeSelect").value;
  const needsTarget = type === "approval_change" || type === "address_interaction";
  $("targetAddressWrap").hidden = !needsTarget;
  $("targetLabelWrap").hidden = !needsTarget;
  $("tokenAddressInput").placeholder = type === "approval_change" ? t("tokenRequired") : t("tokenOptional");
  $("ruleDescription").textContent = localizedRuleDescription(type);
}

function renderPaymentStatus() {
  const status = $("paymentStatus");
  const config = state.paymentConfig;
  if (state.paymentError) {
    status.textContent = localizedApiError(state.paymentError);
  } else if (!config) {
    status.textContent = "";
  } else if (state.selectedPlan === "free") {
    status.textContent = t("freeNoPayment");
  } else if (config.mode !== "live") {
    status.textContent = t("previewMode");
  } else if (!config.ready) {
    status.textContent = t("paymentMissing", { items: config.missing.join(", ") });
  } else {
    status.textContent = `${config.total_amount} ${config.asset} / ${config.chain_name}`;
  }
}

function renderTokenInfo() {
  const box = $("tokenInfoBox");
  if (state.tokenError) {
    box.textContent = t("tokenFailed", { error: localizedApiError(state.tokenError) });
  } else if (state.tokenInfo) {
    box.textContent = t("tokenResult", state.tokenInfo);
  } else {
    box.textContent = t("tokenHint");
  }
}

function renderBalanceInfo() {
  const box = $("balanceBox");
  if (!state.balanceInfo) {
    box.textContent = t("noBalance");
    return;
  }
  box.innerHTML = t("currentBalance", {
    value: escapeHtml(state.balanceInfo.value),
    symbol: escapeHtml(state.balanceInfo.symbol),
    chain: escapeHtml(state.balanceInfo.chain_name),
  });
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
    toast(t("browserNoWallet"));
    return;
  }
  const accounts = await provider.request({ method: "eth_requestAccounts" });
  state.walletAddress = accounts?.[0] || "";
  if (!state.walletAddress) {
    throw new Error(t("walletAccountMissing"));
  }
  $("walletAddressInput").value = state.walletAddress;

  const challenge = await api("/api/auth/challenge", {
    method: "POST",
    body: JSON.stringify({ wallet_address: state.walletAddress }),
  });
  toast(t("signingIdentity"));
  const signature = await signWalletMessage(provider, challenge.message, state.walletAddress);
  const authenticated = await api("/api/auth/verify", {
    method: "POST",
    body: JSON.stringify({
      challenge_id: challenge.challenge_id,
      wallet_address: state.walletAddress,
      signature,
    }),
  });
  state.walletAddress = authenticated.wallet_address;
  state.profile = authenticated.profile || { user_id: authenticated.debox_user_id };
  state.deboxUserId = authenticated.debox_user_id;
  $("walletAddressInput").value = state.walletAddress;
  renderProfile();
  updateConnectionButton();
  await refreshAccount();
  toast(t("walletConnected"));
}

async function restoreSession() {
  try {
    const authenticated = await api("/api/auth/session");
    state.walletAddress = authenticated.wallet_address;
    state.deboxUserId = authenticated.debox_user_id;
    state.profile = authenticated.profile || { user_id: authenticated.debox_user_id };
    $("walletAddressInput").value = state.walletAddress;
    renderProfile();
    updateConnectionButton();
    await refreshAccount();
    return true;
  } catch (error) {
    resetConnectionState();
    if (error.status && error.status !== 401) {
      toast(localizedApiError(error.message));
    }
    return false;
  }
}

async function disconnectWallet() {
  await api("/api/auth/logout", { method: "POST" });
  resetConnectionState();
  toast(t("walletDisconnected"));
}

async function toggleWalletConnection() {
  const button = $("connectWalletBtn");
  const isMobile = window.matchMedia("(max-width: 620px)").matches;
  button.classList.add("is-pressing");
  setTimeout(() => button.classList.remove("is-pressing"), 180);
  if (isMobile) {
    await new Promise((resolve) => setTimeout(resolve, 140));
  }
  if (state.deboxUserId) {
    try {
      await disconnectWallet();
    } catch (error) {
      toast(localizedApiError(error.message));
    }
    return;
  }
  try {
    await connectWallet();
  } catch (error) {
    resetConnectionState();
    if (error.status === 403) {
      showIdentityModal();
      return;
    }
    toast(error?.code === 4001 ? t("signatureCancelled") : localizedApiError(error.message));
  }
}

async function refreshAccount() {
  if (!state.deboxUserId) return;
  const current = await api("/api/subscription/current");
  state.entitlement = current;
  state.groups = current.groups || [];
  const activePlanCode = current.plan?.code;
  if (["standard", "professional"].includes(activePlanCode)) {
    state.selectedPlan = activePlanCode;
  }
  renderPlans();
  renderSubscription();
  renderGroups();
  renderRules();
  await loadPaymentConfig();
}

async function loadPaymentConfig() {
  try {
    state.paymentConfig = await api(`/api/payment/config?plan_code=${encodeURIComponent(state.selectedPlan)}`);
    state.paymentError = "";
  } catch (error) {
    state.paymentConfig = null;
    state.paymentError = error.message;
  }
  renderPaymentStatus();
}

async function enableFreePlan() {
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  await api("/api/subscription/free-trial", {
    method: "POST",
  });
  await refreshAccount();
  toast(t("freeEnabled"));
}

async function payOrRenew() {
  if (!state.deboxUserId || !state.walletAddress) {
    toast(t("connectFirst"));
    return;
  }
  if (state.selectedPlan === "free") {
    await enableFreePlan();
    return;
  }
  if (!confirm(t("refundConfirm"))) {
    return;
  }
  const button = $("payBtn");
  button.disabled = true;
  try {
    const config = await api(`/api/payment/config?plan_code=${encodeURIComponent(state.selectedPlan)}`);
    if (config.mode !== "live") {
      toast(t("previewNoPayment"));
      return;
    }
    if (!config.ready) {
      throw new Error(t("paymentMissing", { items: config.missing.join(", ") }));
    }
    const provider = walletProvider();
    if (!provider?.request) {
      throw new Error(t("browserNoWallet"));
    }
    const accounts = await provider.request({ method: "eth_accounts" });
    if (!accounts?.[0] || accounts[0].toLowerCase() !== state.walletAddress.toLowerCase()) {
      throw new Error(t("paymentWalletMismatch"));
    }
    await provider.request({ method: "wallet_switchEthereumChain", params: [{ chainId: config.chain_id_hex }] });
    const prepared = await api("/api/payment/prepare", {
      method: "POST",
      body: JSON.stringify({ plan_code: state.selectedPlan }),
    });
    const txHash = await provider.request({
      method: "eth_sendTransaction",
      params: [prepared.transactions[0].request],
    });
    const result = await waitForPaymentConfirmation(prepared.order.id, txHash);
    if (result.payment_status === "paid") {
      await refreshAccount();
      toast(t("subscriptionActive"));
    }
  } catch (error) {
    toast(error?.code === 4001 ? t("paymentCancelled") : localizedApiError(error.message));
  } finally {
    button.disabled = false;
  }
}

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitForPaymentConfirmation(orderId, txHash) {
  let lastConfirmations = 0;
  const maxAttempts = 45;
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    try {
      const result = await api("/api/payment/verify", {
        method: "POST",
        body: JSON.stringify({ order_id: orderId, tx_hash: txHash }),
      });
      if (result.payment_status === "paid") return result;
      if (result.payment_status === "failed") {
        const error = new Error(result.error || t("paymentVerificationFailed"));
        error.status = 400;
        throw error;
      }
      lastConfirmations = Number(result.confirmations || 0);
      $("paymentStatus").textContent = t("paymentConfirming", {
        current: lastConfirmations,
        required: result.required_confirmations || 3,
      });
    } catch (error) {
      if (error.status && error.status < 500) throw error;
    }
    await wait(4000);
  }
  $("paymentStatus").textContent = t("paymentContinuing", {
    current: lastConfirmations,
    required: 3,
  });
  return { payment_status: "confirming" };
}

async function lookupToken() {
  const token = $("tokenAddressInput").value.trim();
  if (!token) {
    state.tokenInfo = null;
    state.tokenError = "";
    renderTokenInfo();
    return;
  }
  try {
    const data = await api(`/api/debox/token?contract_address=${encodeURIComponent(token)}&chain_key=${encodeURIComponent($("chainSelect").value)}`);
    const source = profileData(data);
    state.tokenInfo = {
      name: source.name || "-",
      symbol: source.symbol || "-",
      decimals: source.decimal || source.decimals || "-",
    };
    state.tokenError = "";
  } catch (error) {
    state.tokenInfo = null;
    state.tokenError = error.message;
  }
  renderTokenInfo();
}

async function queryBalance() {
  const address = $("walletAddressInput").value.trim();
  if (!address) {
    toast(t("enterWallet"));
    return;
  }
  const query = new URLSearchParams({
    address,
    chain_key: $("chainSelect").value,
  });
  const token = $("tokenAddressInput").value.trim();
  if (token) query.set("token_address", token);
  state.balanceInfo = await api(`/api/chain/balance?${query.toString()}`);
  renderBalanceInfo();
}

async function createRule(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
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
      notification_chat_type: targetType,
      notification_chat_id: targetType === "group" ? $("groupTargetSelect").value : "",
      notification_label: targetType === "group" && selectedGroup ? selectedGroup.textContent : "",
      notification_language: $("ruleLanguageSelect").value,
    }),
  });
  await refreshAccount();
  toast(t("ruleCreated"));
}

async function deleteRule(ruleId) {
  await api(`/api/watch-rules/${ruleId}`, { method: "DELETE" });
  await refreshAccount();
  toast(t("ruleDeleted"));
}

async function restoreRule(ruleId) {
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  state.entitlement = await api(`/api/watch-rules/${ruleId}/restore`, {
    method: "POST",
  });
  state.groups = state.entitlement.groups || [];
  renderSubscription();
  renderGroups();
  renderRules();
  toast(t("ruleRestored"));
}

async function updateRuleLanguage(ruleId, select) {
  if (!state.deboxUserId) {
    select.value = select.dataset.currentLanguage || "zh";
    toast(t("connectFirst"));
    return;
  }
  const previousLanguage = select.dataset.currentLanguage || "zh";
  select.disabled = true;
  try {
    const result = await api(`/api/watch-rules/${ruleId}/notification-language`, {
      method: "PATCH",
      body: JSON.stringify({
        language: select.value,
      }),
    });
    state.entitlement = result.entitlement;
    renderRules();
    toast(t("ruleLanguageUpdated"));
  } catch (error) {
    select.disabled = false;
    select.value = previousLanguage;
    toast(localizedApiError(error.message));
  }
}

async function deletePausedRules() {
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  if (!confirm(t("deletePausedConfirm"))) {
    return;
  }
  const result = await api("/api/watch-rules/paused", {
    method: "DELETE",
  });
  state.entitlement = result.entitlement;
  state.groups = state.entitlement.groups || [];
  renderSubscription();
  renderGroups();
  renderRules();
  toast(t("pausedDeleted", { count: result.deleted || 0 }));
}

async function saveSummary(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  await api("/api/subscription/summary-settings", {
    method: "POST",
    body: JSON.stringify({
      enabled: $("summaryEnabledInput").checked,
      push_time: $("summaryTimeInput").value || "20:00",
      timezone: normalizeSummaryTimezone($("summaryTimezoneInput").value),
      chat_type: $("summaryTargetSelect").value,
      chat_id: $("summaryTargetSelect").value === "group" ? $("summaryGroupSelect").value : "",
      label: $("summaryLabelInput").value.trim(),
      language: $("summaryLanguageInput").value,
    }),
  });
  await refreshAccount();
  toast(t("summarySaved"));
}

async function addGroup(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  const groupLink = $("groupIdInput").value.trim();
  const gid = parseDeBoxGroupLink(groupLink);
  if (!gid) {
    toast(t("invalidGroupLink"));
    return;
  }
  await api("/api/notification-groups", {
    method: "POST",
    body: JSON.stringify({
      gid: groupLink,
      label: $("groupLabelInput").value.trim(),
    }),
  });
  $("groupIdInput").value = "";
  $("groupLabelInput").value = "";
  await refreshAccount();
  toast(t("groupBound"));
}

async function deleteGroup(groupId) {
  const result = await api(`/api/notification-groups/${groupId}`, { method: "DELETE" });
  await refreshAccount();
  if (result.summary_disabled) {
    toast(t("groupDeletedSummaryDisabled"));
  } else if (result.summary_target_changed) {
    toast(t("groupDeletedSummaryPrivate"));
  } else {
    toast(t("groupDeleted"));
  }
}

function bindEvents() {
  $("languageToggleBtn").addEventListener("click", toggleUiLanguage);
  $("connectWalletBtn").addEventListener("click", toggleWalletConnection);
  $("freeTrialBtn").addEventListener("click", enableFreePlan);
  $("payBtn").addEventListener("click", payOrRenew);
  $("deletePausedRulesBtn").addEventListener("click", deletePausedRules);
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
  $("chainSelect").addEventListener("change", renderChainPicker);
  $("chainPickerButton").addEventListener("click", (event) => {
    event.stopPropagation();
    toggleChainPicker();
  });
  $("chainPickerButton").addEventListener("keydown", handleChainPickerKeydown);
  document.addEventListener("click", (event) => {
    if (!$("chainPicker").contains(event.target)) {
      closeChainPicker();
    }
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") closeChainPicker();
  });
  $("tapShield").addEventListener("pointerdown", (event) => {
    event.preventDefault();
    event.stopPropagation();
  });
  $("tapShield").addEventListener("click", (event) => {
    event.preventDefault();
    event.stopPropagation();
  });
  $("identityModalClose").addEventListener("click", () => {
    $("identityModal").hidden = true;
  });
}

async function boot() {
  applyStaticTranslations();
  bindEvents();
  updateTargetVisibility();
  updateSummaryTargetVisibility();
  updateConnectionButton();
  await loadBootData();
  if (!(await restoreSession())) {
    await loadPaymentConfig();
  }
}

boot().catch((error) => {
  toast(localizedApiError(error.message));
});
