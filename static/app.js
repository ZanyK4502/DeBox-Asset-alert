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
  selectedBillingCycle: "monthly",
  entitlement: null,
  groups: [],
  paymentConfig: null,
  paymentError: "",
  tokenInfo: null,
  tokenError: "",
  balanceInfo: null,
  combinationBalanceInfo: null,
  combinationRules: [],
  combinationMembers: [],
  aggregateEvents: [],
  aggregateStats: null,
  aggregateRetentionDays: 30,
  aggregateHasMore: false,
  aggregateNextBeforeId: null,
  aggregateLoading: false,
  aggregateLoadingMore: false,
  aggregateLoadError: "",
  aggregateScrollPaused: false,
  marketCatalog: null,
  marketGoal: "price",
  marketQuery: null,
  marketProjects: [],
  marketDetail: null,
  marketRecommendations: [],
  marketEvents: [],
  marketEventsNextBeforeId: null,
};

const $ = (id) => document.getElementById(id);
const I18N = window.H5_I18N;
const TIME = window.H5_TIME;
let aggregateScrollFrame = 0;
let aggregateScrollLastTime = 0;
let aggregateScrollDirection = 1;
let aggregateScrollHoverPaused = false;

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
  renderCombinationDraft();
  renderProfile();
  renderSubscription(false);
  renderSummaryCapability();
  renderGroups();
  renderRules();
  renderAggregateEvents();
  renderPaymentStatus();
  renderTokenInfo();
  renderBalanceInfo();
  renderBalanceInfo("combination");
  renderMarket();
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

function guardAsync(handler) {
  return (...args) => {
    Promise.resolve(handler(...args)).catch((error) => {
      toast(localizedApiError(error?.message));
    });
  };
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

function chainPickerIds(prefix = "") {
  return prefix
    ? {
        select: `${prefix}ChainSelect`,
        picker: `${prefix}ChainPicker`,
        button: `${prefix}ChainPickerButton`,
        menu: `${prefix}ChainPickerMenu`,
      }
    : {
        select: "chainSelect",
        picker: "chainPicker",
        button: "chainPickerButton",
        menu: "chainPickerMenu",
      };
}

function currentChain(prefix = "") {
  const ids = chainPickerIds(prefix);
  const key = $(ids.select).value || state.chains[0]?.key || "";
  return state.chains.find((chain) => chain.key === key) || state.chains[0] || null;
}

function chainOptionHtml(chain) {
  const logo = chainLogoSrc(chain.key);
  return `
    ${logo ? `<img src="${escapeHtml(logo)}" alt="" />` : `<span class="chain-logo-fallback">${escapeHtml(String(chain.name || "?").slice(0, 1))}</span>`}
    <span>${escapeHtml(chain.name)}</span>
  `;
}

function renderChainPicker(prefix = "") {
  const ids = chainPickerIds(prefix);
  const selected = currentChain(prefix);
  const button = $(ids.button);
  const menu = $(ids.menu);
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
  menu.querySelectorAll("[data-chain]").forEach((option) => {
    option.addEventListener("pointerdown", (event) => {
      event.preventDefault();
      event.stopPropagation();
      selectChain(option.dataset.chain, prefix);
    });
    option.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
    });
  });
}

function closeChainPicker(prefix = "") {
  const ids = chainPickerIds(prefix);
  $(ids.menu).hidden = true;
  $(ids.button).setAttribute("aria-expanded", "false");
}

function closeAllChainPickers() {
  closeChainPicker();
  closeChainPicker("combination");
}

function shieldTapThrough(duration = 260) {
  const shield = $("tapShield");
  clearTimeout(shieldTapThrough.timer);
  shield.hidden = false;
  shieldTapThrough.timer = setTimeout(() => {
    shield.hidden = true;
  }, duration);
}

function toggleChainPicker(prefix = "") {
  const ids = chainPickerIds(prefix);
  const menu = $(ids.menu);
  const willOpen = menu.hidden;
  closeAllChainPickers();
  menu.hidden = !willOpen;
  $(ids.button).setAttribute("aria-expanded", String(willOpen));
  if (willOpen) {
    const active = menu.querySelector(".chain-picker-option.active");
    active?.scrollIntoView({ block: "nearest" });
  }
}

function selectChain(chainKey, prefix = "") {
  const ids = chainPickerIds(prefix);
  $(ids.select).value = chainKey;
  closeChainPicker(prefix);
  shieldTapThrough();
  $(ids.select).dispatchEvent(new Event("change", { bubbles: true }));
  closeChainPicker(prefix);
  $(ids.button).focus();
}

function moveChainSelection(direction, prefix = "") {
  if (!state.chains.length) return;
  const ids = chainPickerIds(prefix);
  const currentKey = $(ids.select).value || state.chains[0].key;
  const currentIndex = Math.max(0, state.chains.findIndex((chain) => chain.key === currentKey));
  const nextIndex = (currentIndex + direction + state.chains.length) % state.chains.length;
  selectChain(state.chains[nextIndex].key, prefix);
  $(ids.menu).hidden = false;
  $(ids.button).setAttribute("aria-expanded", "true");
}

function handleChainPickerKeydown(event, prefix = "") {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    toggleChainPicker(prefix);
    return;
  }
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    moveChainSelection(event.key === "ArrowDown" ? 1 : -1, prefix);
    return;
  }
  if (event.key === "Escape") {
    closeChainPicker(prefix);
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
  state.combinationBalanceInfo = null;
  state.combinationRules = [];
  state.combinationMembers = [];
  state.aggregateEvents = [];
  state.aggregateStats = null;
  state.aggregateRetentionDays = 30;
  state.aggregateHasMore = false;
  state.aggregateNextBeforeId = null;
  state.aggregateLoading = false;
  state.aggregateLoadingMore = false;
  state.aggregateLoadError = "";
  state.aggregateScrollPaused = false;
  state.marketCatalog = null;
  state.marketQuery = null;
  state.marketProjects = [];
  state.marketDetail = null;
  state.marketRecommendations = [];
  state.marketEvents = [];
  state.marketEventsNextBeforeId = null;
  $("walletAddressInput").value = "";
  $("profileBox").innerHTML = t("noWallet");
  $("subscriptionBox").innerHTML = t("connectToView");
  renderRules();
  renderCombinationDraft();
  renderAggregateEvents();
  $("groupsList").innerHTML = "";
  renderTokenInfo();
  renderBalanceInfo();
  renderBalanceInfo("combination");
  renderMarket();
  $("summaryCapability").textContent = t("notConnected");
  $("summaryCapability").classList.add("muted");
  renderSummaryTargetOptions(new Set());
  renderSummaryStatus();
  renderPlans();
  updateConnectionButton();
}

function showIdentityModal() {
  $("identityModal").hidden = false;
}

function renderChains() {
  ["", "combination"].forEach((prefix) => {
    const ids = chainPickerIds(prefix);
    const selectedChain = $(ids.select).value;
    $(ids.select).innerHTML = state.chains
      .map((chain) => `<option value="${escapeHtml(chain.key)}">${escapeHtml(chain.name)}</option>`)
      .join("");
    if (state.chains.some((chain) => chain.key === selectedChain)) {
      $(ids.select).value = selectedChain;
    }
    renderChainPicker(prefix);
  });
}

function renderRuleTypes() {
  ["ruleTypeSelect", "combinationRuleTypeSelect"].forEach((id) => {
    const selectedRuleType = $(id).value;
    $(id).innerHTML = state.ruleTypes
      .map((rule) => `<option value="${escapeHtml(rule.code)}">${escapeHtml(localizedRuleLabel(rule.code))}</option>`)
      .join("");
    if (state.ruleTypes.some((rule) => rule.code === selectedRuleType)) {
      $(id).value = selectedRuleType;
    }
  });
  updateRuleFields();
  updateCombinationMemberFields();
}

function renderPlans() {
  const currentPaidPlan = ["standard", "professional"].includes(currentPlan()?.code)
    ? currentPlan().code
    : "";
  $("plansGrid").innerHTML = state.plans
    .map((plan) => {
      const selectable = plan.code !== "free";
      const tag = selectable ? "button" : "div";
      const active = selectable && plan.code === state.selectedPlan ? " active" : "";
      const locked = selectable && Boolean(currentPaidPlan && plan.code !== currentPaidPlan);
      const attributes = selectable
        ? ` type="button" data-plan="${escapeHtml(plan.code)}"${locked ? " disabled" : ""}`
        : "";
      const text = localizedPlan(plan);
      const billingOption = selectedBillingOption(plan);
      const price = plan.price === "0"
        ? t("freePrice")
        : `${billingOption.price} ${plan.asset || "USDT"}`;
      const priceIcon = plan.price === "0"
        ? ""
        : '<img class="asset-logo" src="/static/tokens/usdt.svg" alt="" aria-hidden="true">';
      const term = plan.price === "0"
        ? t("permanent")
        : t("planDays", { days: billingOption.days });
      return `
        <${tag} class="plan-card${selectable ? "" : " plan-card-static"}${active}"${attributes}>
          <span>${escapeHtml(text.name)}</span>
          <strong class="plan-price">${priceIcon}${escapeHtml(price)}</strong>
          <span class="plan-term">${escapeHtml(term)}</span>
          <small>${escapeHtml(text.description)}</small>
        </${tag}>
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
  document.querySelectorAll("[data-billing-cycle]").forEach((button) => {
    const active = button.dataset.billingCycle === state.selectedBillingCycle;
    button.classList.toggle("active", active);
    button.setAttribute("aria-pressed", String(active));
  });
  const complimentary = state.entitlement?.complimentary_access;
  const complimentaryAvailable = Boolean(
    complimentary?.available && !currentPaidPlan
  );
  const complimentaryActive = Boolean(complimentary?.used && currentPaidPlan);
  $("payBtn").textContent = complimentaryAvailable
    ? t("complimentaryActivate")
    : complimentaryActive
      ? t("currentPlanButton")
      : t("payRenew");
  $("payBtn").disabled = complimentaryActive;
}

function selectedBillingOption(plan) {
  const options = Array.isArray(plan?.billing_options) ? plan.billing_options : [];
  return options.find((option) => option.code === state.selectedBillingCycle)
    || options.find((option) => option.code === "monthly")
    || { code: "monthly", price: plan?.price || "0", days: plan?.days || 0 };
}

function paymentConfigURL() {
  const query = new URLSearchParams({
    plan_code: state.selectedPlan,
    billing_cycle: state.selectedBillingCycle,
  });
  return `/api/payment/config?${query}`;
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
    <small class="muted">${escapeHtml(isFree ? freeHint : t("expiresAt", { date: TIME.formatExpiryDate(sub.expires_at, state.uiLanguage) }))}</small>
  `;
  if (syncSummary) fillSummaryForm();
}

function renderGroups() {
  const selectedSummaryTargets = selectedSummaryTargetKeys();
  if (!state.deboxUserId) {
    $("groupTargetSelect").innerHTML = `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
    $("combinationGroupTargetSelect").innerHTML = `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
    $("groupsList").innerHTML = "";
    renderSummaryTargetOptions(new Set());
    renderSummaryStatus();
    return;
  }
  const selectedRuleGroup = $("groupTargetSelect").value;
  const selectedCombinationGroup = $("combinationGroupTargetSelect").value;
  const options = state.groups.length
    ? state.groups.map((group) => `<option value="${escapeHtml(group.gid)}">${escapeHtml(group.name || group.gid)}</option>`).join("")
    : `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
  $("groupTargetSelect").innerHTML = options;
  $("combinationGroupTargetSelect").innerHTML = options;
  if (state.groups.some((group) => group.gid === selectedRuleGroup)) {
    $("groupTargetSelect").value = selectedRuleGroup;
  }
  if (state.groups.some((group) => group.gid === selectedCombinationGroup)) {
    $("combinationGroupTargetSelect").value = selectedCombinationGroup;
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
    button.addEventListener("click", guardAsync(() => deleteGroup(button.dataset.deleteGroup)));
  });
  updateTargetVisibility();
  updateCombinationTargetVisibility();
  renderSummaryTargetOptions(selectedSummaryTargets);
  updateSummaryTargetVisibility();
  renderSummaryStatus();
}

function ruleLabel(code) {
  return localizedRuleLabel(code);
}

function isThresholdlessRule(ruleType) {
  return ruleType === "approval_change" || ruleType === "address_interaction";
}

function requiresPositiveThreshold(ruleType) {
  return ["incoming", "outgoing", "balance_threshold", "balance_threshold_high"].includes(ruleType);
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
        <span>${escapeHtml(
          isThresholdlessRule(rule.rule_type)
            ? shortAddress(rule.wallet_address)
            : t("ruleThreshold", { address: shortAddress(rule.wallet_address), threshold: rule.threshold })
        )}</span>
        <small class="muted">${escapeHtml(rule.notification_chat_type === "group" ? rule.notification_label || rule.notification_chat_id : t("privateNotification"))}</small>
        <div class="rule-meta">
          <span>${escapeHtml(t("singleRuleLabel"))}</span>
          <span>${escapeHtml(rule.delivery_mode === "stage" ? t("deliveryStage") : t("deliveryRealtime"))}</span>
          ${
            rule.delivery_mode === "stage"
              ? `<span>${escapeHtml(t("cycleSummary", {
                  minutes: rule.cycle_minutes,
                  count: rule.trigger_count_threshold,
                }))}</span>`
              : ""
          }
        </div>
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

function combinationIsActive(rule) {
  return Number(rule?.enabled) === 1 && rule?.run_status === "active";
}

function combinationCanRestore(rule) {
  return Number(rule?.enabled) === 1 && currentPlan()?.code === "professional";
}

function combinationItemHtml(combination, paused = false) {
  const members = combination.members || [];
  const memberSummary = members
    .map((member) => {
      const rule = member.rule || {};
      return `${ruleLabel(rule.rule_type)} · ${shortAddress(rule.wallet_address)} · ${member.required_trigger_count}`;
    })
    .join(" / ");
  return `
    <div class="list-item${paused ? " paused" : ""}">
      <div>
        <strong>${escapeHtml(combination.note || t("combinationLabel"))}</strong>
        <span>${escapeHtml(t("memberCount", { count: members.length }))}</span>
        <small class="muted">${escapeHtml(combination.notification_chat_type === "group" ? combination.notification_label || combination.notification_chat_id : t("privateNotification"))}</small>
        <div class="rule-meta">
          <span>${escapeHtml(t("combinationLabel"))}</span>
          <span>${escapeHtml(combination.cycle_type === "follow" ? t("followCycle") : t("fixedCycle"))}</span>
          <span>${escapeHtml(t("cycleLength", { minutes: combination.cycle_minutes }))}</span>
        </div>
        <div class="combination-members-summary">${escapeHtml(memberSummary)}</div>
        ${paused ? `<small class="pause-reason">${escapeHtml(t("rulePaused"))}</small>` : ""}
      </div>
      <div class="list-actions">
        <label class="rule-language-control">
          <span>${escapeHtml(t("notificationLanguage"))}</span>
          <select data-combination-language="${escapeHtml(combination.id)}" data-current-language="${ruleLanguage(combination)}" aria-label="${escapeHtml(t("notificationLanguage"))}">
            <option value="zh"${ruleLanguage(combination) === "zh" ? " selected" : ""}>${escapeHtml(t("chinese"))}</option>
            <option value="en"${ruleLanguage(combination) === "en" ? " selected" : ""}>English</option>
          </select>
        </label>
        ${
          paused && combinationCanRestore(combination)
            ? `<button class="secondary" type="button" data-restore-combination="${escapeHtml(combination.id)}">${escapeHtml(t("restoreMonitor"))}</button>`
            : ""
        }
        <button class="secondary" type="button" data-delete-combination="${escapeHtml(combination.id)}">${escapeHtml(t("delete"))}</button>
      </div>
    </div>
  `;
}

function renderRules() {
  if (!state.deboxUserId) {
    $("rulesList").innerHTML = `<div class="notice muted">${escapeHtml(t("noActiveRules"))}</div>`;
    $("pausedRulesList").innerHTML = `<div class="notice muted">${escapeHtml(t("noPausedRules"))}</div>`;
    $("deletePausedRulesBtn").disabled = true;
    return;
  }
  const rules = (state.entitlement?.active_rules || state.entitlement?.rules || [])
    .filter((rule) => rule.rule_scope !== "combination");
  const pausedRules = (state.entitlement?.paused_rules || [])
    .filter((rule) => rule.rule_scope !== "combination");
  const activeCombinations = state.combinationRules.filter(combinationIsActive);
  const pausedCombinations = state.combinationRules.filter((rule) => !combinationIsActive(rule));
  if (!rules.length && !activeCombinations.length) {
    $("rulesList").innerHTML = `<div class="notice muted">${escapeHtml(t("noActiveRules"))}</div>`;
  } else {
    $("rulesList").innerHTML = [
      ...rules.map((rule) => ruleItemHtml(rule)),
      ...activeCombinations.map((rule) => combinationItemHtml(rule)),
    ].join("");
  }
  const pausedCount = pausedRules.length + pausedCombinations.length;
  $("deletePausedRulesBtn").disabled = pausedCount === 0;
  $("pausedRulesList").innerHTML = pausedCount
    ? [
        ...pausedRules.map((rule) => ruleItemHtml(rule, true)),
        ...pausedCombinations.map((rule) => combinationItemHtml(rule, true)),
      ].join("")
    : `<div class="notice muted">${escapeHtml(t("noPausedRules"))}</div>`;
  document.querySelectorAll("[data-delete-rule]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => deleteRule(button.dataset.deleteRule)));
  });
  document.querySelectorAll("[data-restore-rule]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => restoreRule(button.dataset.restoreRule)));
  });
  document.querySelectorAll("[data-rule-language]").forEach((select) => {
    select.addEventListener("change", guardAsync(() => updateRuleLanguage(select.dataset.ruleLanguage, select)));
  });
  document.querySelectorAll("[data-delete-combination]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => deleteCombinationRule(button.dataset.deleteCombination)));
  });
  document.querySelectorAll("[data-restore-combination]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => restoreCombinationRule(button.dataset.restoreCombination)));
  });
  document.querySelectorAll("[data-combination-language]").forEach((select) => {
    select.addEventListener("change", guardAsync(() => updateCombinationLanguage(select.dataset.combinationLanguage, select)));
  });
}

function aggregateMetricHtml(label, value, placeholder = false) {
  return `
    <div class="aggregate-stat${placeholder ? " is-placeholder" : ""}">
      <span>${escapeHtml(label)}</span>
      <strong>${escapeHtml(value)}</strong>
    </div>
  `;
}

function aggregatePlaceholderRows() {
  return Array.from({ length: 7 }, () => `
    <div class="aggregate-event placeholder" aria-hidden="true">
      <div class="aggregate-event-main">
        <strong>-----</strong>
        <span>-----</span>
        <small>-----</small>
      </div>
      <div class="aggregate-event-side">
        <strong>-----</strong>
        <span>-----</span>
      </div>
    </div>
  `).join("");
}

function aggregateEventStatus(event) {
  switch (event.notification_status) {
    case "sent":
      return { text: t("notificationSent"), className: "" };
    case "failed":
      return { text: t("notificationFailed"), className: "failed" };
    case "pending":
      return { text: t("notificationPending"), className: "pending" };
    default:
      return { text: t("notificationNotSent"), className: "" };
  }
}

function formatAggregateEventTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-----";
  return new Intl.DateTimeFormat(state.uiLanguage === "en" ? "en" : "zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

function aggregateEventValue(event) {
  const previous = event.previous_value;
  const current = event.current_value;
  if (previous !== null && previous !== undefined && current !== null && current !== undefined) {
    return t("aggregateValueChange", { previous, current });
  }
  if (current !== null && current !== undefined && String(current) !== "") return String(current);
  return event.note || event.target_label || "-----";
}

function aggregateChainName(chainKey) {
  return state.chains.find((chain) => chain.key === chainKey)?.name || chainKey || "-----";
}

function aggregateEventHtml(event) {
  const combination = event.source_type === "combination";
  const kind = t(combination ? "combinationEvent" : "stageEvent");
  const title = combination
    ? event.combination_note || t("combinationLabel")
    : ruleLabel(event.rule_type);
  const progress = combination
    ? t("aggregateCombinationProgress", {
        total: event.window_total_trigger_count,
        required: event.required_trigger_count,
      })
    : t("aggregateProgress", {
        current: event.window_total_trigger_count,
        required: event.required_trigger_count,
      });
  const status = aggregateEventStatus(event);
  return `
    <article class="aggregate-event">
      <div class="aggregate-event-main">
        <strong>${escapeHtml(title)}</strong>
        <span>${escapeHtml(`${kind} · ${aggregateChainName(event.chain_key)} · ${shortAddress(event.wallet_address)}`)}</span>
        <small>${escapeHtml(aggregateEventValue(event))}</small>
      </div>
      <div class="aggregate-event-side">
        <strong class="${status.className}">${escapeHtml(status.text)}</strong>
        <span>${escapeHtml(progress)}</span>
        <span>${escapeHtml(formatAggregateEventTime(event.occurred_at || event.detected_at || event.created_at))}</span>
      </div>
    </article>
  `;
}

function updateAggregateScrollStatus() {
  const button = $("aggregateScrollToggleBtn");
  const paused = state.aggregateScrollPaused || aggregateScrollHoverPaused;
  button.textContent = t(paused ? "scrollPaused" : "autoScrolling");
  button.classList.toggle("paused", paused);
  button.setAttribute("aria-pressed", String(state.aggregateScrollPaused));
}

function animateAggregateEvents(timestamp) {
  const viewport = $("aggregateEventViewport");
  if (!viewport) {
    aggregateScrollFrame = 0;
    return;
  }
  const elapsed = aggregateScrollLastTime ? Math.min(timestamp - aggregateScrollLastTime, 80) : 0;
  aggregateScrollLastTime = timestamp;
  if (!state.aggregateScrollPaused && !aggregateScrollHoverPaused && !document.hidden) {
    const maximum = viewport.scrollHeight - viewport.clientHeight;
    if (maximum > 1) {
      let next = viewport.scrollTop + aggregateScrollDirection * elapsed * 0.018;
      if (next >= maximum) {
        next = maximum;
        aggregateScrollDirection = -1;
      } else if (next <= 0) {
        next = 0;
        aggregateScrollDirection = 1;
      }
      viewport.scrollTop = next;
    }
  }
  aggregateScrollFrame = requestAnimationFrame(animateAggregateEvents);
}

function startAggregateAutoScroll(reset = false) {
  const viewport = $("aggregateEventViewport");
  if (reset && viewport) {
    viewport.scrollTop = 0;
    aggregateScrollDirection = 1;
  }
  if (aggregateScrollFrame) return;
  aggregateScrollLastTime = 0;
  aggregateScrollFrame = requestAnimationFrame(animateAggregateEvents);
}

function setAggregateScrollPaused(paused) {
  state.aggregateScrollPaused = paused;
  updateAggregateScrollStatus();
  startAggregateAutoScroll();
}

function renderAggregateEvents({ resetScroll = false } = {}) {
  const connected = Boolean(state.deboxUserId);
  const hasEvents = state.aggregateEvents.length > 0;
  const hasStats = connected && !state.aggregateLoading && !state.aggregateLoadError && state.aggregateStats;
  const showValues = Boolean(hasStats && Number(state.aggregateStats.event_count) > 0);
  const stats = state.aggregateStats || {};
  const metrics = [
    [t("aggregateEvents"), stats.event_count],
    [t("aggregateStageEvents"), stats.stage_event_count],
    [t("aggregateCombinationEvents"), stats.combination_event_count],
    [t("aggregateSentNotifications"), stats.sent_notification_count],
  ];
  $("aggregateEventStats").innerHTML = metrics
    .map(([label, value]) => aggregateMetricHtml(label, showValues ? String(value ?? 0) : "-----", !showValues))
    .join("");

  const range = $("aggregateEventsRange");
  const refreshButton = $("refreshAggregateEventsBtn");
  const loadMoreButton = $("loadMoreAggregateEventsBtn");
  refreshButton.disabled = !connected || state.aggregateLoading || state.aggregateLoadingMore;
  if (!connected) {
    range.textContent = t("notConnected");
  } else if (state.aggregateLoading) {
    range.textContent = t("aggregateEventsLoading");
  } else if (state.aggregateLoadError) {
    range.textContent = t("aggregateEventsLoadFailed");
  } else {
    range.textContent = t("aggregateRange", { days: state.aggregateRetentionDays });
  }
  range.classList.toggle("muted", !connected || state.aggregateLoading || Boolean(state.aggregateLoadError));

  const list = $("aggregateEventList");
  if (state.aggregateLoadError && !hasEvents) {
    list.innerHTML = `
      <div class="aggregate-event-error">
        <div>
          <p>${escapeHtml(t("aggregateEventsLoadFailed"))}</p>
          <button class="secondary" type="button" data-retry-aggregate-events>${escapeHtml(t("aggregateEventsRetry"))}</button>
        </div>
      </div>
    `;
    list.querySelector("[data-retry-aggregate-events]")
      ?.addEventListener("click", guardAsync(() => loadAggregateEvents()));
  } else if (hasEvents) {
    list.innerHTML = state.aggregateEvents.map(aggregateEventHtml).join("");
  } else {
    list.innerHTML = aggregatePlaceholderRows();
  }

  const loadedCount = $("aggregateLoadedCount");
  if (!connected) {
    loadedCount.textContent = t("aggregateEventsDisconnected");
  } else if (state.aggregateLoading) {
    loadedCount.textContent = t("aggregateEventsLoading");
  } else if (state.aggregateLoadError) {
    loadedCount.textContent = t("aggregateEventsLoadFailed");
  } else if (!hasEvents) {
    loadedCount.textContent = t("aggregateEventsEmpty");
  } else if (state.aggregateHasMore) {
    loadedCount.textContent = t("aggregateLoaded", {
      loaded: state.aggregateEvents.length,
      total: stats.event_count ?? state.aggregateEvents.length,
    });
  } else {
    loadedCount.textContent = t("noMoreEvents");
  }

  loadMoreButton.hidden = !connected || !hasEvents || !state.aggregateHasMore;
  loadMoreButton.disabled = state.aggregateLoadingMore;
  loadMoreButton.textContent = t(state.aggregateLoadingMore ? "loadingMoreEvents" : "loadMoreEvents");
  $("aggregateScrollToggleBtn").disabled = Boolean(state.aggregateLoadError && !hasEvents);
  updateAggregateScrollStatus();
  startAggregateAutoScroll(resetScroll);
}

async function loadAggregateEvents({ append = false } = {}) {
  if (!state.deboxUserId || state.aggregateLoading || state.aggregateLoadingMore) return;
  if (append && (!state.aggregateHasMore || !state.aggregateNextBeforeId)) return;
  if (append) {
    state.aggregateLoadingMore = true;
  } else {
    state.aggregateLoading = true;
    state.aggregateLoadError = "";
  }
  renderAggregateEvents();
  try {
    const query = new URLSearchParams({ limit: "30" });
    if (append) query.set("before_id", String(state.aggregateNextBeforeId));
    const page = await api(`/api/aggregate-events?${query.toString()}`);
    const incoming = Array.isArray(page.events) ? page.events : [];
    if (append) {
      const known = new Set(state.aggregateEvents.map((event) => String(event.id)));
      state.aggregateEvents = [
        ...state.aggregateEvents,
        ...incoming.filter((event) => !known.has(String(event.id))),
      ];
    } else {
      state.aggregateEvents = incoming;
      state.aggregateScrollPaused = false;
    }
    state.aggregateStats = page.stats || null;
    state.aggregateRetentionDays = Number(page.retention_days || 30);
    state.aggregateHasMore = Boolean(page.has_more);
    state.aggregateNextBeforeId = page.next_before_id || null;
    state.aggregateLoadError = "";
  } catch (error) {
    state.aggregateLoadError = localizedApiError(error.message);
    if (!append) {
      state.aggregateEvents = [];
      state.aggregateStats = null;
      state.aggregateHasMore = false;
      state.aggregateNextBeforeId = null;
    }
  } finally {
    state.aggregateLoading = false;
    state.aggregateLoadingMore = false;
    renderAggregateEvents({ resetScroll: !append });
  }
}

function fillSummaryForm() {
  const settings = state.entitlement?.summary_settings || {};
  $("summaryEnabledInput").checked = Boolean(settings.enabled);
  $("summaryTimeInput").value = settings.time || "20:00";
  $("summaryTimezoneInput").value = normalizeSummaryTimezone(settings.timezone);
  $("summaryLanguageInput").value = settings.language === "en" ? "en" : "zh";
  $("summaryLabelInput").value = settings.label || "";
  const selectedTargets = new Set(
    summaryTargetsFromSettings(settings).map((target) =>
      target.chat_type === "private" ? "private" : `group:${target.chat_id}`
    )
  );
  renderSummaryTargetOptions(selectedTargets);
  renderSummaryCapability();
  updateSummaryTargetVisibility();
}

function summaryTargetsFromSettings(settings = {}) {
  const targets = Array.isArray(settings.targets)
    ? settings.targets.filter((target) => target?.chat_type && target?.chat_id)
    : [];
  if (targets.length) return targets;
  if (settings.chat_type && settings.chat_id) {
    return [{ chat_type: settings.chat_type, chat_id: settings.chat_id }];
  }
  return [];
}

function selectedSummaryTargetKeys() {
  return new Set(
    [...document.querySelectorAll("#summaryTargetOptions input:checked")].map((input) =>
      input.dataset.chatType === "private" ? "private" : `group:${input.dataset.chatId}`
    )
  );
}

function selectedSummaryTargets() {
  return [...document.querySelectorAll("#summaryTargetOptions input:checked")].map((input) => ({
    chat_type: input.dataset.chatType,
    chat_id: input.dataset.chatType === "private" ? state.deboxUserId : input.dataset.chatId,
  }));
}

function renderSummaryTargetOptions(selectedKeys = selectedSummaryTargetKeys()) {
  const plan = currentPlan();
  const available = Boolean(state.deboxUserId && plan?.daily_summary);
  const professional = plan?.code === "professional";
  const selected = new Set(selectedKeys);
  if (available && selected.size === 0) selected.add("private");

  const options = [
    `<label class="summary-target-option">
      <input
        type="checkbox"
        data-chat-type="private"
        data-chat-id="${escapeHtml(state.deboxUserId)}"
        ${selected.has("private") ? "checked" : ""}
        ${!available || !professional ? "disabled" : ""}
      />
      <span>${escapeHtml(t("privateSelf"))}</span>
    </label>`,
  ];

  if (professional) {
    if (state.groups.length) {
      options.push(
        ...state.groups.map((group) => {
          const key = `group:${group.gid}`;
          return `<label class="summary-target-option">
            <input
              type="checkbox"
              data-chat-type="group"
              data-chat-id="${escapeHtml(group.gid)}"
              ${selected.has(key) ? "checked" : ""}
              ${available ? "" : "disabled"}
            />
            <span>${escapeHtml(group.name || group.gid)}</span>
          </label>`;
        })
      );
    } else {
      options.push(`<div class="notice muted">${escapeHtml(t("noBoundGroups"))}</div>`);
    }
  }

  $("summaryTargetOptions").innerHTML = options.join("");
}

function renderSummaryStatus(settings = state.entitlement?.summary_settings || {}) {
  const plan = currentPlan();
  const available = Boolean(state.deboxUserId && plan?.daily_summary);
  const targets = summaryTargetsFromSettings(settings);
  const configured = targets.length > 0;
  const enabled = available && Boolean(settings.enabled);
  const groupNames = new Map(state.groups.map((group) => [group.gid, group.name || group.gid]));
  const targetLabels = targets.map((target) =>
    target.chat_type === "private"
      ? t("privateSelf")
      : groupNames.get(target.chat_id) || target.chat_id
  );

  $("summaryStatusState").textContent = available
    ? (enabled ? t("summaryEnabledStatus") : t("summaryDisabledStatus"))
    : "--";
  $("summaryStatusTime").textContent = available && configured ? settings.time || "20:00" : "--";
  $("summaryStatusTimezone").textContent = available && configured
    ? normalizeSummaryTimezone(settings.timezone)
    : "--";
  $("summaryStatusLanguage").textContent = available && configured
    ? (settings.language === "en" ? "English" : t("chinese"))
    : "--";
  $("summaryStatusLabel").textContent = available && configured && settings.label ? settings.label : "--";
  $("summaryStatusTargets").textContent = available && targetLabels.length
    ? targetLabels.join(state.uiLanguage === "en" ? ", " : "、")
    : "--";
  $("summaryEditBtn").disabled = !available;
  $("summaryDisableBtn").disabled = !enabled;
}

function renderSummaryCapability() {
  const plan = currentPlan();
  const available = Boolean(state.deboxUserId && plan?.daily_summary);
  $("summaryCapability").textContent = state.deboxUserId
    ? (available ? t("available") : t("planUnavailable"))
    : t("notConnected");
  $("summaryCapability").classList.toggle("muted", !available);
  renderSummaryStatus();
}

function updateRuleFields() {
  const type = $("ruleTypeSelect").value;
  const needsTarget = type === "approval_change" || type === "address_interaction";
  const thresholdless = isThresholdlessRule(type);
  $("targetAddressWrap").hidden = !needsTarget;
  $("targetLabelWrap").hidden = !needsTarget;
  $("thresholdWrap").hidden = thresholdless;
  $("tokenAddressInput").placeholder = type === "approval_change" ? t("tokenRequired") : t("tokenOptional");
  $("ruleDescription").textContent = localizedRuleDescription(type);
  $("thresholdHint").textContent = thresholdless
    ? t("thresholdNotRequired")
    : requiresPositiveThreshold(type)
      ? t("thresholdPositiveRequired")
      : t("thresholdZeroAllowed");
  $("thresholdInput").min = requiresPositiveThreshold(type) ? "0.000001" : "0";
  updateDeliveryModeFields();
}

function updateDeliveryModeFields() {
  const stage = $("deliveryModeSelect").value === "stage";
  $("stageSettingsWrap").hidden = !stage;
  $("deliveryModeHint").textContent = t(stage ? "stageModeHint" : "realtimeModeHint");
}

function resetSingleRuleDraft() {
  $("walletAddressInput").value = "";
  $("tokenAddressInput").value = "";
  $("targetAddressInput").value = "";
  $("targetLabelInput").value = "";
  $("thresholdInput").value = "";
  state.tokenInfo = null;
  state.tokenError = "";
  state.balanceInfo = null;
  renderTokenInfo();
  renderBalanceInfo();
}

function updateCombinationMemberFields() {
  const type = $("combinationRuleTypeSelect").value;
  const needsTarget = type === "approval_change" || type === "address_interaction";
  const thresholdless = isThresholdlessRule(type);
  $("combinationTargetAddressWrap").hidden = !needsTarget;
  $("combinationTargetLabelWrap").hidden = !needsTarget;
  $("combinationThresholdWrap").hidden = thresholdless;
  $("combinationTokenAddressInput").placeholder = type === "approval_change" ? t("tokenRequired") : t("tokenOptional");
  $("combinationRuleDescription").textContent = localizedRuleDescription(type);
  $("combinationThresholdHint").textContent = thresholdless
    ? t("thresholdNotRequired")
    : requiresPositiveThreshold(type)
      ? t("thresholdPositiveRequired")
      : t("thresholdZeroAllowed");
  $("combinationThresholdInput").min = requiresPositiveThreshold(type) ? "0.000001" : "0";
}

function setRuleCreationMode(mode) {
  const combination = mode === "combination";
  $("singleRulePanel").hidden = combination;
  $("combinationRulePanel").hidden = !combination;
  $("singleRuleModeBtn").classList.toggle("active", !combination);
  $("combinationRuleModeBtn").classList.toggle("active", combination);
  $("singleRuleModeBtn").setAttribute("aria-selected", String(!combination));
  $("combinationRuleModeBtn").setAttribute("aria-selected", String(combination));
}

function updateCombinationTargetVisibility() {
  $("combinationGroupTargetWrap").hidden = $("combinationTargetTypeSelect").value !== "group";
}

function validateRuleDraft(rule, requiredCount = null) {
  if (!rule.wallet_address) throw new Error(t("enterMonitoredAddress"));
  const threshold = Number(rule.threshold || 0);
  if (requiresPositiveThreshold(rule.rule_type) && !(threshold > 0)) {
    throw new Error(t("enterPositiveThreshold"));
  }
  if (rule.rule_type === "approval_change" && (!rule.token_address || !rule.target_address)) {
    throw new Error(t("approvalFieldsRequired"));
  }
  if (rule.rule_type === "address_interaction" && !rule.target_address) {
    throw new Error(t("interactionTargetRequired"));
  }
  if (requiredCount !== null && (!Number.isInteger(requiredCount) || requiredCount <= 0)) {
    throw new Error(t("enterPositiveCount"));
  }
}

function thresholdValue(ruleType, input) {
  if (isThresholdlessRule(ruleType)) return "0";
  const value = input.value.trim();
  if (requiresPositiveThreshold(ruleType) && !(Number(value) > 0)) {
    input.value = "";
    input.focus();
    throw new Error(t("enterPositiveThreshold"));
  }
  return value || "0";
}

function validateThresholdOnBlur(ruleTypeSelectId, thresholdInputId) {
  const input = $(thresholdInputId);
  const value = input.value.trim();
  if (!value || !requiresPositiveThreshold($(ruleTypeSelectId).value) || Number(value) > 0) return;
  input.value = "";
  toast(t("enterPositiveThreshold"));
}

function combinationMemberDraft() {
  const ruleType = $("combinationRuleTypeSelect").value;
  return {
    client_id: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
    required_trigger_count: Number($("combinationMemberTriggerInput").value),
    rule: {
      chain_key: $("combinationChainSelect").value,
      wallet_address: $("combinationAddressInput").value.trim(),
      token_address: $("combinationTokenAddressInput").value.trim() || null,
      target_address: $("combinationTargetAddressInput").value.trim() || null,
      target_label: $("combinationTargetLabelInput").value.trim(),
      rule_type: ruleType,
      threshold: thresholdValue(ruleType, $("combinationThresholdInput")),
      notification_language: $("combinationLanguageSelect").value,
      delivery_mode: "realtime",
      cycle_type: "fixed",
      cycle_minutes: 60,
      trigger_count_threshold: 1,
    },
  };
}

function resetCombinationMemberEditor() {
  $("combinationAddressInput").value = "";
  $("combinationTokenAddressInput").value = "";
  $("combinationTargetAddressInput").value = "";
  $("combinationTargetLabelInput").value = "";
  $("combinationThresholdInput").value = "";
  $("combinationMemberTriggerInput").value = "1";
  state.combinationBalanceInfo = null;
  renderBalanceInfo("combination");
  updateCombinationMemberFields();
}

function renderCombinationDraft() {
  $("combinationMemberCount").textContent = String(state.combinationMembers.length);
  $("combinationDraftList").innerHTML = state.combinationMembers.length
    ? state.combinationMembers
        .map(
          (member) => `
            <div class="member-draft-item">
              <div>
                <strong>${escapeHtml(ruleLabel(member.rule.rule_type))} / ${escapeHtml(member.rule.chain_key)}</strong>
                <span>${escapeHtml(shortAddress(member.rule.wallet_address))} · ${escapeHtml(t("triggerCount"))} ${escapeHtml(member.required_trigger_count)}</span>
              </div>
              <button class="secondary" type="button" data-remove-member="${escapeHtml(member.client_id)}">${escapeHtml(t("removeMember"))}</button>
            </div>
          `
        )
        .join("")
    : `<div class="notice muted">${escapeHtml(t("combinationNeedsTwoMembers"))}</div>`;
  document.querySelectorAll("[data-remove-member]").forEach((button) => {
    button.addEventListener("click", () => {
      state.combinationMembers = state.combinationMembers.filter(
        (member) => member.client_id !== button.dataset.removeMember
      );
      renderCombinationDraft();
    });
  });
}

function addCombinationMember() {
  try {
    const member = combinationMemberDraft();
    validateRuleDraft(member.rule, member.required_trigger_count);
    state.combinationMembers.push(member);
    resetCombinationMemberEditor();
    renderCombinationDraft();
    toast(t("memberAdded"));
  } catch (error) {
    toast(error.message);
  }
}

function renderPaymentStatus() {
  const status = $("paymentStatus");
  const config = state.paymentConfig;
  const complimentary = state.entitlement?.complimentary_access;
  const currentPaidPlan = ["standard", "professional"].includes(currentPlan()?.code);
  if (complimentary?.available && !currentPaidPlan) {
    status.textContent = t("complimentaryAvailable");
  } else if (complimentary?.used && currentPaidPlan) {
    status.textContent = t("complimentaryActive", { date: complimentary.expires_at || "-" });
  } else if (state.paymentError) {
    status.textContent = localizedApiError(state.paymentError);
  } else if (!config) {
    status.textContent = "";
  } else if (config.mode !== "live") {
    status.textContent = t("previewMode");
  } else if (!config.ready) {
    status.textContent = t("paymentMissing", { items: config.missing.join(", ") });
  } else {
    status.innerHTML = `
      <span class="payment-detail">
        <span class="payment-asset">
          <img class="asset-logo" src="/static/chains/bsc.png" alt="" aria-hidden="true">
          ${escapeHtml(config.chain_name)}
        </span>
      </span>
    `;
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

function renderBalanceInfo(mode = "single") {
  const combination = mode === "combination";
  const box = $(combination ? "combinationBalanceBox" : "balanceBox");
  const balanceInfo = combination ? state.combinationBalanceInfo : state.balanceInfo;
  if (!balanceInfo) {
    box.textContent = t("noBalance");
    return;
  }
  box.innerHTML = t("currentBalance", {
    value: escapeHtml(balanceInfo.value),
    symbol: escapeHtml(balanceInfo.symbol),
    chain: escapeHtml(balanceInfo.chain_name),
  });
}

function updateTargetVisibility() {
  $("groupTargetWrap").hidden = $("targetTypeSelect").value !== "group";
}

function updateSummaryTargetVisibility() {
  const plan = currentPlan();
  const available = Boolean(state.deboxUserId && plan?.daily_summary);
  $("summaryForm").querySelectorAll("input, select, button").forEach((control) => {
    if (control.closest("#summaryTargetOptions")) return;
    control.disabled = !available;
  });
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
  const [current, combinations] = await Promise.all([
    api("/api/subscription/current"),
    api("/api/combination-rules"),
  ]);
  state.entitlement = current;
  state.combinationRules = combinations.combination_rules || [];
  state.groups = current.groups || [];
  const activePlanCode = current.plan?.code;
  if (["standard", "professional"].includes(activePlanCode)) {
    state.selectedPlan = activePlanCode;
  }
  renderPlans();
  renderSubscription();
  renderGroups();
  renderRules();
  await Promise.all([
    loadPaymentConfig(),
    loadAggregateEvents(),
    loadMarketContext(),
  ]);
}

async function loadPaymentConfig() {
  try {
    state.paymentConfig = await api(paymentConfigURL());
    state.paymentError = "";
  } catch (error) {
    state.paymentConfig = null;
    state.paymentError = error.message;
  }
  renderPaymentStatus();
}

async function payOrRenew() {
  if (!state.deboxUserId || !state.walletAddress) {
    toast(t("connectFirst"));
    return;
  }
  const complimentary = state.entitlement?.complimentary_access;
  const hasPaidPlan = ["standard", "professional"].includes(currentPlan()?.code);
  if (complimentary?.available && !hasPaidPlan) {
    const planName = localizedPlan(state.plans.find((plan) => plan.code === state.selectedPlan)).name;
    if (!confirm(t("complimentaryConfirm", { plan: planName }))) {
      return;
    }
    const button = $("payBtn");
    button.disabled = true;
    try {
      await api("/api/subscription/complimentary", {
        method: "POST",
        body: JSON.stringify({ plan_code: state.selectedPlan }),
      });
      await refreshAccount();
      toast(t("complimentaryActivated"));
    } catch (error) {
      toast(localizedApiError(error.message));
    } finally {
      renderPlans();
    }
    return;
  }
  const button = $("payBtn");
  button.disabled = true;
  try {
    const config = await api(paymentConfigURL());
    if (config.mode !== "live") {
      toast(t("previewNoPayment"));
      return;
    }
    if (!config.ready) {
      throw new Error(t("paymentMissing", { items: config.missing.join(", ") }));
    }
    const planName = localizedPlan(config.plan).name;
    if (!confirm(t("purchaseConfirm", {
      plan: planName,
      amount: config.total_amount,
      asset: config.asset,
      days: config.subscription_days,
    }))) {
      return;
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
      body: JSON.stringify({
        plan_code: state.selectedPlan,
        billing_cycle: state.selectedBillingCycle,
      }),
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
  const chainKey = $("chainSelect").value;
  if (!token) {
    state.tokenInfo = null;
    state.tokenError = "";
    renderTokenInfo();
    return;
  }
  try {
    const data = await api(`/api/debox/token?contract_address=${encodeURIComponent(token)}&chain_key=${encodeURIComponent(chainKey)}`);
    if ($("tokenAddressInput").value.trim() !== token || $("chainSelect").value !== chainKey) return;
    const source = profileData(data);
    state.tokenInfo = {
      name: source.name || "-",
      symbol: source.symbol || "-",
      decimals: source.decimal || source.decimals || "-",
    };
    state.tokenError = "";
  } catch (error) {
    if ($("tokenAddressInput").value.trim() !== token || $("chainSelect").value !== chainKey) return;
    state.tokenInfo = null;
    state.tokenError = error.message;
  }
  renderTokenInfo();
}

async function queryBalance(mode = "single") {
  const combination = mode === "combination";
  const address = $(combination ? "combinationAddressInput" : "walletAddressInput").value.trim();
  if (!address) {
    toast(t("enterMonitoredAddress"));
    return;
  }
  const query = new URLSearchParams({
    address,
    chain_key: $(combination ? "combinationChainSelect" : "chainSelect").value,
  });
  const token = $(combination ? "combinationTokenAddressInput" : "tokenAddressInput").value.trim();
  if (token) query.set("token_address", token);
  const balanceInfo = await api(`/api/chain/balance?${query.toString()}`);
  if (combination) {
    state.combinationBalanceInfo = balanceInfo;
  } else {
    state.balanceInfo = balanceInfo;
  }
  renderBalanceInfo(mode);
}

async function createRule(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  const targetType = $("targetTypeSelect").value;
  const selectedGroup = $("groupTargetSelect").selectedOptions[0];
  const ruleType = $("ruleTypeSelect").value;
  const deliveryMode = $("deliveryModeSelect").value;
  const cycleMinutes = Number($("cycleMinutesInput").value);
  const triggerCount = Number($("triggerCountInput").value);
  let payload;
  try {
    payload = {
      chain_key: $("chainSelect").value,
      wallet_address: $("walletAddressInput").value.trim(),
      token_address: $("tokenAddressInput").value.trim() || null,
      target_address: $("targetAddressInput").value.trim() || null,
      target_label: $("targetLabelInput").value.trim(),
      rule_type: ruleType,
      threshold: thresholdValue(ruleType, $("thresholdInput")),
      notification_chat_type: targetType,
      notification_chat_id: targetType === "group" ? $("groupTargetSelect").value : "",
      notification_label: targetType === "group" && selectedGroup ? selectedGroup.textContent : "",
      notification_language: $("ruleLanguageSelect").value,
      delivery_mode: deliveryMode,
      cycle_type: $("cycleTypeSelect").value,
      cycle_minutes: deliveryMode === "stage" ? cycleMinutes : 60,
      trigger_count_threshold: deliveryMode === "stage" ? triggerCount : 1,
    };
    validateRuleDraft(payload);
    if (deliveryMode === "stage") {
      if (!Number.isInteger(cycleMinutes) || cycleMinutes <= 0) throw new Error(t("enterPositiveCycle"));
      if (!Number.isInteger(triggerCount) || triggerCount <= 0) throw new Error(t("enterPositiveCount"));
    }
  } catch (error) {
    toast(error.message);
    return;
  }
  await api("/api/watch-rules", {
    method: "POST",
    body: JSON.stringify(payload),
  });
  await refreshAccount();
  toast(t("ruleCreated"));
}

async function createCombinationRule(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  if (state.combinationMembers.length < 2) {
    toast(t("combinationNeedsTwoMembers"));
    return;
  }
  const cycleMinutes = Number($("combinationCycleMinutesInput").value);
  if (!Number.isInteger(cycleMinutes) || cycleMinutes <= 0) {
    toast(t("enterPositiveCycle"));
    return;
  }
  const targetType = $("combinationTargetTypeSelect").value;
  const selectedGroup = $("combinationGroupTargetSelect").selectedOptions[0];
  await api("/api/combination-rules", {
    method: "POST",
    body: JSON.stringify({
      note: $("combinationNoteInput").value.trim(),
      cycle_type: $("combinationCycleTypeSelect").value,
      cycle_minutes: cycleMinutes,
      notification_chat_type: targetType,
      notification_chat_id: targetType === "group" ? $("combinationGroupTargetSelect").value : "",
      notification_label: targetType === "group" && selectedGroup ? selectedGroup.textContent : "",
      notification_language: $("combinationLanguageSelect").value,
      members: state.combinationMembers.map(({ rule, required_trigger_count: count }) => ({
        rule: {
          ...rule,
          notification_language: $("combinationLanguageSelect").value,
        },
        required_trigger_count: count,
      })),
    }),
  });
  state.combinationMembers = [];
  $("combinationNoteInput").value = "";
  renderCombinationDraft();
  await refreshAccount();
  toast(t("combinationCreated"));
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

async function deleteCombinationRule(combinationId) {
  await api(`/api/combination-rules/${combinationId}`, { method: "DELETE" });
  await refreshAccount();
  toast(t("combinationDeleted"));
}

async function restoreCombinationRule(combinationId) {
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  await api(`/api/combination-rules/${combinationId}/restore`, { method: "POST" });
  await refreshAccount();
  toast(t("combinationRestored"));
}

async function updateCombinationLanguage(combinationId, select) {
  if (!state.deboxUserId) {
    select.value = select.dataset.currentLanguage || "zh";
    toast(t("connectFirst"));
    return;
  }
  const previousLanguage = select.dataset.currentLanguage || "zh";
  select.disabled = true;
  try {
    await api(`/api/combination-rules/${combinationId}/notification-language`, {
      method: "PATCH",
      body: JSON.stringify({ language: select.value }),
    });
    await refreshAccount();
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
  const pausedCombinationIds = state.combinationRules
    .filter((rule) => !combinationIsActive(rule))
    .map((rule) => rule.id);
  const result = await api("/api/watch-rules/paused", {
    method: "DELETE",
  });
  await Promise.all(
    pausedCombinationIds.map((id) => api(`/api/combination-rules/${id}`, { method: "DELETE" }))
  );
  await refreshAccount();
  toast(t("pausedDeleted", { count: Number(result.deleted || 0) + pausedCombinationIds.length }));
}

async function saveSummary(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  await persistSummarySettings($("summaryEnabledInput").checked);
}

async function persistSummarySettings(enabled) {
  const targets = selectedSummaryTargets();
  if (!targets.length) {
    toast(t("summaryTargetRequired"));
    return false;
  }
  await api("/api/subscription/summary-settings", {
    method: "POST",
    body: JSON.stringify({
      enabled,
      push_time: $("summaryTimeInput").value || "20:00",
      timezone: normalizeSummaryTimezone($("summaryTimezoneInput").value),
      targets,
      label: $("summaryLabelInput").value.trim(),
      language: $("summaryLanguageInput").value,
    }),
  });
  await refreshAccount();
  toast(t("summarySaved"));
  return true;
}

function editSummary() {
  $("summaryForm").scrollIntoView({ behavior: "smooth", block: "center" });
  $("summaryEnabledInput").focus({ preventScroll: true });
}

async function disableSummary() {
  await persistSummarySettings(false);
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

const MARKET_GOAL_RULES = {
  price: ["market_price_above", "market_price_below", "market_price_increase", "market_price_decrease"],
  liquidity: ["market_liquidity_below", "market_liquidity_decrease", "market_liquidity_added", "market_liquidity_removed", "market_new_pool"],
  volume: ["market_volume_above", "market_volume_spike", "market_trade_imbalance"],
  large_trade: ["market_large_buy", "market_large_sell", "market_consecutive_large_buy", "market_consecutive_large_sell"],
  holder: ["market_holder_increase", "market_holder_decrease", "market_holder_rank_entered", "market_holder_rank_exited"],
  four_meme: ["market_four_meme_large_trade", "market_four_meme_progress", "market_four_meme_migration"],
};

const MARKET_GOAL_HINT_KEYS = {
  price: "marketGoalPriceHint",
  liquidity: "marketGoalLiquidityHint",
  volume: "marketGoalVolumeHint",
  large_trade: "marketGoalLargeTradeHint",
  holder: "marketGoalHolderHint",
  four_meme: "marketGoalFourMemeHint",
};

const MARKET_UNIT_KEYS = {
  usd: "unitUsd",
  token: "unitToken",
  percent: "unitPercent",
  ratio: "unitRatio",
  count: "unitCount",
  progress: "unitProgress",
};

function marketRuleDefinition(code) {
  return state.marketCatalog?.rules?.find((rule) => rule.code === code) || null;
}

function marketRuleName(rule) {
  if (!rule) return "";
  return state.uiLanguage === "en" ? rule.name_en : rule.name_zh;
}

function marketRuleDescription(rule) {
  if (!rule) return "";
  return state.uiLanguage === "en" ? rule.description_en : rule.description_zh;
}

function marketMoney(value, currency = true) {
  const number = Number(value);
  if (!Number.isFinite(number)) return "-";
  const absolute = Math.abs(number);
  const digits = absolute > 0 && absolute < 0.01 ? 8 : absolute < 1 ? 6 : 2;
  const formatted = new Intl.NumberFormat(state.uiLanguage === "en" ? "en-US" : "zh-CN", {
    maximumFractionDigits: digits,
  }).format(number);
  return currency ? `$${formatted}` : formatted;
}

function marketDate(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat(state.uiLanguage === "en" ? "en-US" : "zh-CN", {
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
    hour12: false,
  }).format(date);
}

function marketEventLabel(type) {
  const labels = state.uiLanguage === "en"
    ? {
        buy: "Buy", sell: "Sell", liquidity_added: "Liquidity added",
        liquidity_removed: "Liquidity removed", holder_increase: "Holder increase",
        holder_decrease: "Holder decrease", holder_rank_entered: "Entered holder ranking",
        holder_rank_exited: "Exited holder ranking", pool_initialized: "New pool",
        migrated: "Migration", token_transfer: "Token transfer",
      }
    : {
        buy: "买入", sell: "卖出", liquidity_added: "加池",
        liquidity_removed: "撤池", holder_increase: "大户增持",
        holder_decrease: "大户减持", holder_rank_entered: "进入大户榜",
        holder_rank_exited: "退出大户榜", pool_initialized: "新交易池",
        migrated: "迁移外盘", token_transfer: "代币转账",
      };
  return labels[type] || type;
}

function marketStatusSuffix(status) {
  if (status === "active") return "Active";
  if (status === "archived") return "Archived";
  return "Paused";
}

function renderMarket() {
  renderMarketCapability();
  renderMarketGoal();
  renderMarketQuery();
  renderMarketProjects();
  renderMarketDetail();
}

function renderMarketCapability() {
  const badge = $("marketCapability");
  if (!state.deboxUserId) {
    badge.textContent = t("notConnected");
    badge.classList.add("muted");
    return;
  }
  const plan = currentPlan();
  badge.textContent = localizedPlan(plan).name;
  badge.classList.toggle("muted", plan?.code === "free");
}

function renderMarketGoal() {
  document.querySelectorAll("[data-market-goal]").forEach((button) => {
    button.classList.toggle("active", button.dataset.marketGoal === state.marketGoal);
  });
  $("marketGoalHint").textContent = t(MARKET_GOAL_HINT_KEYS[state.marketGoal]);
  renderMarketRuleEditor();
}

function renderMarketQuery() {
  const result = $("marketQueryResult");
  if (!state.marketQuery) {
    result.hidden = true;
    return;
  }
  result.hidden = false;
  const token = state.marketQuery.token || {};
  $("marketTokenOverview").innerHTML = `
    <div>
      <strong>${escapeHtml(token.name || token.symbol || "-")} (${escapeHtml(token.symbol || "-")})</strong>
      <span>${escapeHtml(shortAddress(token.address))}</span>
    </div>
    <div>
      <span>${escapeHtml(t("chain"))}</span>
      <strong>BNB Chain</strong>
    </div>
  `;
  const planCode = currentPlan()?.code || "free";
  let selectedCount = 0;
  $("marketQueryPools").innerHTML = (state.marketQuery.pools || []).map((preview, index) => {
    const pair = preview.pair || {};
    const checked = preview.supported && selectedCount++ === 0;
    const disabled = !preview.supported;
    return `
      <label class="market-pool-card${disabled ? " unsupported" : ""}">
        <input type="checkbox" data-market-query-pool="${escapeHtml(pair.pairAddress || "")}" ${checked ? "checked" : ""} ${disabled ? "disabled" : ""} />
        <div>
          <strong>${escapeHtml(preview.protocol || pair.dexId || "-")} ${escapeHtml(preview.protocol_version || "")}</strong>
          <span>${escapeHtml(pair.baseToken?.symbol || "-")} / ${escapeHtml(pair.quoteToken?.symbol || "-")}</span>
          <small>${escapeHtml(shortAddress(pair.pairAddress))}</small>
        </div>
        <div class="market-pool-values">
          <strong>${marketMoney(pair.liquidity?.usd)}</strong>
          <span>${disabled ? t("marketPoolUnsupported") : marketMoney(pair.priceUsd)}</span>
        </div>
      </label>
    `;
  }).join("") || `<div class="empty-state">${escapeHtml(t("marketNoPools"))}</div>`;
  const createButton = $("createMarketProjectBtn");
  createButton.disabled = planCode === "free";
  createButton.title = planCode === "free" ? t("marketFreeQueryOnly") : "";
  $("marketQueryPools").querySelectorAll("[data-market-query-pool]").forEach((checkbox) => {
    checkbox.addEventListener("change", () => {
      if (planCode === "standard" && checkbox.checked) {
        $("marketQueryPools").querySelectorAll("[data-market-query-pool]").forEach((other) => {
          if (other !== checkbox) other.checked = false;
        });
      }
    });
  });
}

function renderMarketProjects() {
  const list = $("marketProjectsList");
  if (!state.deboxUserId) {
    list.innerHTML = `<div class="empty-state">${escapeHtml(t("connectFirst"))}</div>`;
    return;
  }
  const projects = state.marketProjects;
  if (!projects.length) {
    list.innerHTML = `<div class="empty-state">${escapeHtml(t("marketNoProjects"))}</div>`;
    return;
  }
  list.innerHTML = projects.map((project) => `
    <button class="market-project-card${project.status === "archived" ? " archived" : ""}" type="button" data-market-project="${project.id}">
      <div>
        <strong>${escapeHtml(project.token_name || project.token_symbol || "-")} (${escapeHtml(project.token_symbol || "-")})</strong>
        <span>${escapeHtml(shortAddress(project.token_address))}</span>
      </div>
      <div>
        <span class="badge">${escapeHtml(t(`marketProjectStatus${marketStatusSuffix(project.status)}`))}</span>
        <small>${escapeHtml(t("marketOpenProject"))} →</small>
      </div>
    </button>
  `).join("");
  list.querySelectorAll("[data-market-project]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => openMarketProject(Number(button.dataset.marketProject))));
  });
}

function renderMarketDetail() {
  const wrap = $("marketProjectDetail");
  const detail = state.marketDetail;
  if (!detail?.project) {
    wrap.hidden = true;
    return;
  }
  wrap.hidden = false;
  const project = detail.project;
  $("marketProjectHeader").innerHTML = `
    <div>
      <p class="eyebrow">BNB Chain · ${escapeHtml(project.four_meme_status || "unknown")}</p>
      <h3>${escapeHtml(project.token_name || project.token_symbol)} (${escapeHtml(project.token_symbol)})</h3>
      <span>${escapeHtml(project.token_address)}</span>
    </div>
    <span class="badge">${escapeHtml(t(`marketProjectStatus${marketStatusSuffix(project.status)}`))}</span>
  `;
  $("archiveMarketProjectBtn").textContent = t(
    project.status === "archived" ? "restoreMarketProject" : "archiveMarketProject",
  );
  $("archiveMarketProjectBtn").classList.toggle("danger", project.status !== "archived");
  const snapshot = detail.latest_snapshot || {};
  const metrics = [
    [t("marketMetricPrice"), marketMoney(snapshot.price_usd)],
    [t("marketMetricLiquidity"), marketMoney(snapshot.liquidity_usd)],
    [t("marketMetricVolume24h"), marketMoney(snapshot.volume_24h_usd)],
    [t("marketMetricBuys24h"), snapshot.buys_24h ?? "-"],
    [t("marketMetricSells24h"), snapshot.sells_24h ?? "-"],
    [t("marketMetricMarketCap"), `${marketMoney(snapshot.market_cap_usd)} / ${marketMoney(snapshot.fdv_usd)}`],
  ];
  $("marketMetricGrid").innerHTML = metrics.map(([label, value]) => `
    <div><span>${escapeHtml(label)}</span><strong>${escapeHtml(String(value))}</strong></div>
  `).join("");
  const providerHealth = detail.provider_health || [];
  const degraded = providerHealth.some((item) => item.status !== "healthy");
  const providerStatusKey = providerHealth.length
    ? (degraded ? "marketProviderDegraded" : "marketProviderHealthy")
    : "marketProviderPending";
  $("marketProviderStatus").textContent = t(providerStatusKey);
  $("marketProviderStatus").classList.toggle("warning", degraded);
  renderMarketProjectPools();
  renderMarketRuleEditor();
  renderMarketRules();
  renderMarketHolders();
  renderMarketEvents();
}

function renderMarketProjectPools() {
  const detail = state.marketDetail;
  if (!detail) return;
  const editable = detail.project?.status === "active";
  $("marketProjectPools").innerHTML = (detail.pools || []).map((pool) => {
    const selected = Number(pool.selected) === 1;
    const primary = Number(pool.is_primary) === 1;
    const supported = Number(pool.supports_event_parsing) === 1;
    return `
      <div class="market-pool-card${supported ? "" : " unsupported"}">
        <div>
          <strong>${escapeHtml(pool.protocol)} ${escapeHtml(pool.protocol_version)}</strong>
          <span>${escapeHtml(pool.token0_symbol)} / ${escapeHtml(pool.token1_symbol)}</span>
          <small>${escapeHtml(shortAddress(pool.pool_address || pool.pool_key))}</small>
        </div>
        <div class="market-pool-values">
          <strong>${marketMoney(pool.liquidity_usd)}</strong>
          <span>${escapeHtml(primary ? t("marketPoolPrimary") : selected ? t("marketPoolSelected") : supported ? "" : t("marketPoolUnsupported"))}</span>
        </div>
        ${supported && editable ? `
          <div class="market-pool-actions">
            ${!primary ? `<button type="button" class="secondary compact" data-market-primary="${pool.id}">${escapeHtml(t("marketSetPrimary"))}</button>` : ""}
            ${!primary ? `<button type="button" class="secondary compact" data-market-toggle-pool="${pool.id}" data-selected="${selected}">${escapeHtml(t(selected ? "marketDisablePool" : "marketEnablePool"))}</button>` : ""}
          </div>
        ` : ""}
      </div>
    `;
  }).join("") || `<div class="empty-state">${escapeHtml(t("marketNoPools"))}</div>`;
  $("marketProjectPools").querySelectorAll("[data-market-primary]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => updateMarketPool(Number(button.dataset.marketPrimary), true, true)));
  });
  $("marketProjectPools").querySelectorAll("[data-market-toggle-pool]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => updateMarketPool(
      Number(button.dataset.marketTogglePool),
      button.dataset.selected !== "true",
      false,
    )));
  });
}

function renderMarketRuleEditor() {
  const select = $("marketRuleTypeSelect");
  if (!select || !state.marketCatalog) return;
  const previous = select.value;
  const goalRules = new Set(MARKET_GOAL_RULES[state.marketGoal] || []);
  const definitions = (state.marketCatalog.rules || []).filter((rule) => goalRules.has(rule.code));
  select.innerHTML = definitions.map((rule) => `
    <option value="${escapeHtml(rule.code)}" ${rule.allowed ? "" : "disabled"}>
      ${escapeHtml(marketRuleName(rule))}${rule.professional ? ` · ${escapeHtml(t("professional"))}` : ""}
    </option>
  `).join("");
  if (definitions.some((rule) => rule.code === previous && rule.allowed)) {
    select.value = previous;
  } else {
    const firstAllowed = definitions.find((rule) => rule.allowed);
    if (firstAllowed) select.value = firstAllowed.code;
  }
  const definition = marketRuleDefinition(select.value);
  $("marketRuleDescription").textContent = definition
    ? marketRuleDescription(definition)
    : t("marketProfessionalOnly");
  const units = definition?.units || ["usd"];
  const oldUnit = $("marketThresholdUnitSelect").value;
  $("marketThresholdUnitSelect").innerHTML = units.map((unit) => `
    <option value="${escapeHtml(unit)}">${escapeHtml(t(MARKET_UNIT_KEYS[unit] || unit))}</option>
  `).join("");
  $("marketThresholdUnitSelect").value = units.includes(oldUnit) ? oldUnit : (definition?.default_unit || units[0]);
  if ($("marketSensitivitySelect").value !== "custom" && definition) {
    const recommendation = selectedMarketRecommendation(definition.code, $("marketSensitivitySelect").value);
    $("marketThresholdInput").value = recommendation?.threshold || definition.default_threshold;
    $("marketCooldownInput").value = recommendation?.cooldown_seconds || 300;
    $("marketWindowInput").value = recommendation?.window_minutes || definition.default_window_minutes || 60;
  }
  $("marketWindowWrap").hidden = !Number(definition?.default_window_minutes);
  const pools = (state.marketDetail?.pools || []).filter((pool) => Number(pool.selected) === 1);
  $("marketRulePoolSelect").innerHTML = `
    <option value="">${escapeHtml(t("marketAllSelectedPools"))}</option>
    ${pools.map((pool) => `<option value="${pool.id}">${escapeHtml(pool.protocol)} ${escapeHtml(pool.protocol_version)} · ${escapeHtml(pool.token0_symbol)}/${escapeHtml(pool.token1_symbol)}</option>`).join("")}
  `;
  const editable = state.marketDetail?.project?.status === "active";
  $("marketRuleForm").querySelectorAll("input, select, button").forEach((control) => {
    control.disabled = !editable || !definition?.allowed;
  });
  const groupOption = [...$("marketTargetTypeSelect").options]
    .find((option) => option.value === "group");
  const groupAllowed = currentPlan()?.code === "professional" && state.groups.length > 0;
  if (groupOption) groupOption.disabled = !groupAllowed;
  if (!groupAllowed && $("marketTargetTypeSelect").value === "group") {
    $("marketTargetTypeSelect").value = "private";
  }
  updateMarketDeliveryFields();
  renderMarketGroupOptions();
}

function selectedMarketRecommendation(ruleType, sensitivity) {
  return state.marketRecommendations.find(
    (item) => item.rule_type === ruleType && item.sensitivity === sensitivity,
  ) || null;
}

function renderMarketGroupOptions() {
  const selected = $("marketGroupSelect").value;
  $("marketGroupSelect").innerHTML = state.groups.map((group) => `
    <option value="${escapeHtml(group.gid)}">${escapeHtml(group.name || group.gid)}</option>
  `).join("");
  if (state.groups.some((group) => group.gid === selected)) $("marketGroupSelect").value = selected;
  $("marketGroupWrap").hidden = $("marketTargetTypeSelect").value !== "group";
}

function updateMarketDeliveryFields() {
  $("marketStageSettings").hidden = $("marketDeliveryModeSelect").value !== "stage";
}

function renderMarketRules() {
  const rules = state.marketDetail?.rules || [];
  const projectActive = state.marketDetail?.project?.status === "active";
  $("marketRulesList").innerHTML = rules.length ? rules.map((rule) => {
    const definition = marketRuleDefinition(rule.rule_type);
    const active = Number(rule.enabled) === 1 && rule.run_status === "active";
    return `
      <div class="list-item">
        <div>
          <strong>${escapeHtml(marketRuleName(definition) || rule.rule_type)}</strong>
          <span>${escapeHtml(rule.threshold_value)} ${escapeHtml(t(MARKET_UNIT_KEYS[rule.threshold_unit] || rule.threshold_unit))} · ${escapeHtml(rule.delivery_mode)}</span>
          <small>${escapeHtml(active ? t("marketRuleStatusActive") : t("marketRuleStatusPaused"))}${rule.pause_reason ? ` · ${escapeHtml(rule.pause_reason)}` : ""}</small>
        </div>
        <div class="list-item-actions">
          ${active || !projectActive ? "" : `<button type="button" class="secondary compact" data-restore-market-rule="${rule.id}">${escapeHtml(t("restoreMonitor"))}</button>`}
          <button type="button" class="secondary compact danger" data-delete-market-rule="${rule.id}">${escapeHtml(t("delete"))}</button>
        </div>
      </div>
    `;
  }).join("") : `<div class="empty-state">${escapeHtml(t("marketNoRules"))}</div>`;
  $("marketRulesList").querySelectorAll("[data-delete-market-rule]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => deleteMarketRule(Number(button.dataset.deleteMarketRule))));
  });
  $("marketRulesList").querySelectorAll("[data-restore-market-rule]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => restoreMarketRule(Number(button.dataset.restoreMarketRule))));
  });
}

function renderMarketHolders() {
  const detail = state.marketDetail;
  if (!detail) return;
  $("marketLabelForm").querySelectorAll("input, select, button").forEach((control) => {
    control.disabled = detail.project?.status !== "active";
  });
  $("marketLabelsList").innerHTML = (detail.labels || []).map((label) => `
    <div class="market-label-chip">
      <span>${escapeHtml(label.label || label.label_type)} · ${escapeHtml(shortAddress(label.address))}${Number(label.excluded) ? " · ⊘" : ""}</span>
      <button type="button" data-delete-market-label="${label.id}" aria-label="${escapeHtml(t("delete"))}">×</button>
    </div>
  `).join("");
  $("marketLabelsList").querySelectorAll("[data-delete-market-label]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => deleteMarketLabel(Number(button.dataset.deleteMarketLabel))));
  });
  const labels = new Map((detail.labels || []).map((label) => [label.address, label]));
  $("marketHoldersList").innerHTML = (detail.holders || []).map((holder) => {
    const label = labels.get(holder.holder_address);
    return `
      <div class="market-holder-row">
        <strong>#${holder.rank ?? "-"} · ${escapeHtml(label?.label || shortAddress(holder.holder_address))}</strong>
        <span>${escapeHtml(holder.balance)} ${escapeHtml(detail.project.token_symbol)} · ${escapeHtml(holder.supply_percent || "-")}%</span>
        <small>${escapeHtml(holder.address_kind || "wallet")}${Number(holder.excluded) ? " · excluded" : ""}</small>
      </div>
    `;
  }).join("") || `<div class="empty-state">${escapeHtml(t("marketNoHolders"))}</div>`;
}

function renderMarketEvents() {
  const list = $("marketEventsList");
  list.innerHTML = state.marketEvents.length ? state.marketEvents.map((event) => `
    <div class="market-event-row">
      <div>
        <strong>${escapeHtml(marketEventLabel(event.event_type))}</strong>
        <span>${event.usd_value ? marketMoney(event.usd_value) : escapeHtml(event.token_amount || "-")} · ${escapeHtml(event.wallet_address ? shortAddress(event.wallet_address) : "-")}</span>
      </div>
      <div>
        <time>${escapeHtml(marketDate(event.occurred_at))}</time>
        <small>${escapeHtml(event.confirmed ? "✓" : "…")} ${escapeHtml(event.transaction_hash ? shortAddress(event.transaction_hash) : event.source || "")}</small>
      </div>
    </div>
  `).join("") : `<div class="empty-state">${escapeHtml(t("marketNoEvents"))}</div>`;
  $("loadMoreMarketEventsBtn").hidden = !state.marketEventsNextBeforeId;
}

async function loadMarketContext() {
  if (!state.deboxUserId) {
    renderMarket();
    return;
  }
  const [catalog, projects] = await Promise.all([
    api("/api/market/catalog"),
    api("/api/market/projects?include_archived=true"),
  ]);
  state.marketCatalog = catalog;
  state.marketProjects = projects.projects || [];
  renderMarket();
}

async function queryMarketToken(event) {
  event.preventDefault();
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  const address = $("marketTokenInput").value.trim();
  if (!/^0x[0-9a-fA-F]{40}$/.test(address)) {
    toast(t("marketInvalidToken"));
    return;
  }
  $("marketQueryStatus").textContent = t("marketQuerying");
  try {
    state.marketQuery = await api("/api/market/query", {
      method: "POST",
      body: JSON.stringify({ chain_key: "bsc", token_address: address }),
    });
  } catch (error) {
    $("marketQueryStatus").textContent = t("marketQueryFailed");
    throw error;
  }
  $("marketQueryStatus").textContent = t("marketQuerySuccess", {
    symbol: state.marketQuery.token?.symbol || "-",
    count: state.marketQuery.pools?.length || 0,
  });
  renderMarketQuery();
}

async function createMarketProject() {
  if (!state.marketQuery) return;
  if (currentPlan()?.code === "free") {
    toast(t("marketFreeQueryOnly"));
    return;
  }
  const selectedPools = [...$("marketQueryPools").querySelectorAll("[data-market-query-pool]:checked")]
    .map((input) => input.dataset.marketQueryPool);
  const supportedPools = (state.marketQuery.pools || []).filter((pool) => pool.supported);
  if (supportedPools.length && !selectedPools.length) {
    toast(t("marketSelectPoolRequired"));
    return;
  }
  const detail = await api("/api/market/projects", {
    method: "POST",
    body: JSON.stringify({
      chain_key: "bsc",
      token_address: state.marketQuery.token.address,
      pool_addresses: selectedPools,
    }),
  });
  state.marketQuery = null;
  $("marketTokenInput").value = "";
  await loadMarketContext();
  state.marketDetail = detail;
  await loadMarketProjectExtras(detail.project.id);
  toast(t("marketProjectCreated"));
}

async function openMarketProject(projectId) {
  state.marketDetail = await api(`/api/market/projects/${projectId}`);
  await loadMarketProjectExtras(projectId);
  $("marketProjectDetail").scrollIntoView({ behavior: "smooth", block: "start" });
}

async function loadMarketProjectExtras(projectId) {
  const [recommendations] = await Promise.all([
    api(`/api/market/projects/${projectId}/recommendations`),
    loadMarketEvents(projectId),
  ]);
  state.marketRecommendations = recommendations.recommendations || [];
  renderMarketDetail();
}

async function loadMarketEvents(projectId = state.marketDetail?.project?.id, append = false) {
  if (!projectId) return;
  const query = new URLSearchParams({ limit: "50" });
  if (append && state.marketEventsNextBeforeId) {
    query.set("before_id", state.marketEventsNextBeforeId);
  }
  const result = await api(`/api/market/projects/${projectId}/events?${query}`);
  state.marketEvents = append
    ? [...state.marketEvents, ...(result.events || [])]
    : (result.events || []);
  state.marketEventsNextBeforeId = result.next_before_id || null;
  renderMarketEvents();
}

async function refreshMarketProject() {
  const projectId = state.marketDetail?.project?.id;
  if (!projectId) return;
  state.marketDetail = await api(`/api/market/projects/${projectId}`);
  await loadMarketProjectExtras(projectId);
}

async function archiveMarketProject() {
  const projectId = state.marketDetail?.project?.id;
  if (!projectId) return;
  if (state.marketDetail.project.status === "archived") {
    await api(`/api/market/projects/${projectId}/restore`, { method: "POST" });
    await loadMarketContext();
    state.marketDetail = await api(`/api/market/projects/${projectId}`);
    await loadMarketProjectExtras(projectId);
    toast(t("marketProjectRestored"));
    return;
  }
  if (!confirm(t("archiveMarketConfirm"))) return;
  await api(`/api/market/projects/${projectId}`, { method: "DELETE" });
  state.marketDetail = null;
  await loadMarketContext();
  toast(t("marketProjectArchived"));
}

async function updateMarketPool(poolId, selected, isPrimary) {
  const projectId = state.marketDetail?.project?.id;
  if (!projectId) return;
  state.marketDetail = await api(`/api/market/projects/${projectId}/pool`, {
    method: "PATCH",
    body: JSON.stringify({
      market_pool_id: poolId,
      selected,
      is_primary: isPrimary,
    }),
  });
  renderMarketDetail();
  toast(t("marketPoolUpdated"));
}

async function createMarketRule(event) {
  event.preventDefault();
  const projectId = state.marketDetail?.project?.id;
  const definition = marketRuleDefinition($("marketRuleTypeSelect").value);
  if (!projectId || !definition?.allowed) {
    toast(t("marketNotLoaded"));
    return;
  }
  const poolValue = $("marketRulePoolSelect").value;
  await api(`/api/market/projects/${projectId}/rules`, {
    method: "POST",
    body: JSON.stringify({
      market_pool_id: poolValue ? Number(poolValue) : null,
      rule_type: definition.code,
      threshold_value: $("marketThresholdInput").value,
      threshold_unit: $("marketThresholdUnitSelect").value,
      window_minutes: $("marketWindowWrap").hidden ? null : Number($("marketWindowInput").value),
      sensitivity: $("marketSensitivitySelect").value,
      cooldown_seconds: Number($("marketCooldownInput").value),
      delivery_mode: $("marketDeliveryModeSelect").value,
      cycle_type: $("marketCycleTypeSelect").value,
      cycle_minutes: Number($("marketCycleMinutesInput").value),
      trigger_count_threshold: Number($("marketTriggerCountInput").value),
      notification_chat_type: $("marketTargetTypeSelect").value,
      notification_chat_id: $("marketTargetTypeSelect").value === "group" ? $("marketGroupSelect").value : "",
      notification_language: $("marketRuleLanguageSelect").value,
    }),
  });
  await refreshMarketProject();
  toast(t("marketRuleCreated"));
}

async function deleteMarketRule(ruleId) {
  await api(`/api/market/rules/${ruleId}`, { method: "DELETE" });
  await refreshMarketProject();
  toast(t("marketRuleDeleted"));
}

async function restoreMarketRule(ruleId) {
  await api(`/api/market/rules/${ruleId}/restore`, { method: "POST" });
  await refreshMarketProject();
  toast(t("marketRuleRestored"));
}

async function saveMarketLabel(event) {
  event.preventDefault();
  const projectId = state.marketDetail?.project?.id;
  if (!projectId) return;
  await api(`/api/market/projects/${projectId}/labels`, {
    method: "POST",
    body: JSON.stringify({
      address: $("marketLabelAddressInput").value.trim(),
      label_type: $("marketLabelTypeSelect").value,
      label: $("marketLabelInput").value.trim(),
      excluded: $("marketLabelExcludedInput").checked,
    }),
  });
  $("marketLabelAddressInput").value = "";
  $("marketLabelInput").value = "";
  $("marketLabelExcludedInput").checked = false;
  await refreshMarketProject();
  toast(t("holderLabelSaved"));
}

async function deleteMarketLabel(labelId) {
  await api(`/api/market/labels/${labelId}`, { method: "DELETE" });
  await refreshMarketProject();
  toast(t("holderLabelDeleted"));
}

function bindEvents() {
  $("languageToggleBtn").addEventListener("click", toggleUiLanguage);
  $("connectWalletBtn").addEventListener("click", guardAsync(toggleWalletConnection));
  $("payBtn").addEventListener("click", guardAsync(payOrRenew));
  document.querySelectorAll("[data-billing-cycle]").forEach((button) => {
    button.addEventListener("click", () => {
      state.selectedBillingCycle = button.dataset.billingCycle;
      renderPlans();
      loadPaymentConfig();
    });
  });
  $("deletePausedRulesBtn").addEventListener("click", guardAsync(deletePausedRules));
  $("refreshRulesBtn").addEventListener("click", guardAsync(refreshAccount));
  $("refreshAggregateEventsBtn").addEventListener("click", guardAsync(() => loadAggregateEvents()));
  $("loadMoreAggregateEventsBtn").addEventListener("click", guardAsync(() => loadAggregateEvents({ append: true })));
  $("aggregateScrollToggleBtn").addEventListener("click", () => {
    setAggregateScrollPaused(!state.aggregateScrollPaused);
  });
  $("aggregateEventViewport").addEventListener("mouseenter", () => {
    if (!window.matchMedia("(hover: hover) and (pointer: fine)").matches) return;
    aggregateScrollHoverPaused = true;
    updateAggregateScrollStatus();
  });
  $("aggregateEventViewport").addEventListener("mouseleave", () => {
    aggregateScrollHoverPaused = false;
    updateAggregateScrollStatus();
  });
  $("aggregateEventViewport").addEventListener("pointerdown", () => {
    if (window.matchMedia("(hover: none), (pointer: coarse)").matches && !state.aggregateScrollPaused) {
      setAggregateScrollPaused(true);
    }
  });
  $("aggregateEventViewport").addEventListener("keydown", (event) => {
    if (event.key !== " ") return;
    event.preventDefault();
    setAggregateScrollPaused(!state.aggregateScrollPaused);
  });
  $("queryBalanceBtn").addEventListener("click", guardAsync(() => queryBalance()));
  $("combinationQueryBalanceBtn").addEventListener("click", guardAsync(() => queryBalance("combination")));
  $("ruleForm").addEventListener("submit", guardAsync(createRule));
  $("combinationRuleForm").addEventListener("submit", guardAsync(createCombinationRule));
  $("singleRuleModeBtn").addEventListener("click", () => setRuleCreationMode("single"));
  $("combinationRuleModeBtn").addEventListener("click", () => setRuleCreationMode("combination"));
  $("addCombinationMemberBtn").addEventListener("click", addCombinationMember);
  $("groupForm").addEventListener("submit", guardAsync(addGroup));
  $("summaryForm").addEventListener("submit", guardAsync(saveSummary));
  $("summaryEditBtn").addEventListener("click", editSummary);
  $("summaryDisableBtn").addEventListener("click", guardAsync(disableSummary));
  $("marketQueryForm").addEventListener("submit", guardAsync(queryMarketToken));
  $("createMarketProjectBtn").addEventListener("click", guardAsync(createMarketProject));
  $("refreshMarketProjectsBtn").addEventListener("click", guardAsync(loadMarketContext));
  $("closeMarketDetailBtn").addEventListener("click", () => {
    state.marketDetail = null;
    state.marketRecommendations = [];
    state.marketEvents = [];
    state.marketEventsNextBeforeId = null;
    renderMarketDetail();
    $("marketProjectsList").scrollIntoView({ behavior: "smooth", block: "center" });
  });
  $("archiveMarketProjectBtn").addEventListener("click", guardAsync(archiveMarketProject));
  $("marketRuleForm").addEventListener("submit", guardAsync(createMarketRule));
  $("marketLabelForm").addEventListener("submit", guardAsync(saveMarketLabel));
  $("refreshMarketEventsBtn").addEventListener("click", guardAsync(() => loadMarketEvents()));
  $("loadMoreMarketEventsBtn").addEventListener("click", guardAsync(() => loadMarketEvents(undefined, true)));
  $("marketRuleTypeSelect").addEventListener("change", renderMarketRuleEditor);
  $("marketSensitivitySelect").addEventListener("change", renderMarketRuleEditor);
  $("marketDeliveryModeSelect").addEventListener("change", updateMarketDeliveryFields);
  $("marketTargetTypeSelect").addEventListener("change", renderMarketGroupOptions);
  $("marketThresholdInput").addEventListener("input", () => {
    $("marketSensitivitySelect").value = "custom";
  });
  document.querySelectorAll("[data-market-goal]").forEach((button) => {
    button.addEventListener("click", () => {
      state.marketGoal = button.dataset.marketGoal;
      renderMarketGoal();
    });
  });
  $("targetTypeSelect").addEventListener("change", updateTargetVisibility);
  $("combinationTargetTypeSelect").addEventListener("change", updateCombinationTargetVisibility);
  $("ruleTypeSelect").addEventListener("change", () => {
    resetSingleRuleDraft();
    updateRuleFields();
  });
  $("combinationRuleTypeSelect").addEventListener("change", resetCombinationMemberEditor);
  $("deliveryModeSelect").addEventListener("change", updateDeliveryModeFields);
  $("thresholdInput").addEventListener("blur", () => validateThresholdOnBlur("ruleTypeSelect", "thresholdInput"));
  $("combinationThresholdInput").addEventListener("blur", () => {
    validateThresholdOnBlur("combinationRuleTypeSelect", "combinationThresholdInput");
  });
  $("tokenAddressInput").addEventListener("blur", lookupToken);
  $("chainSelect").addEventListener("change", lookupToken);
  $("chainSelect").addEventListener("change", () => renderChainPicker());
  $("combinationChainSelect").addEventListener("change", () => renderChainPicker("combination"));
  $("chainPickerButton").addEventListener("click", (event) => {
    event.stopPropagation();
    toggleChainPicker();
  });
  $("chainPickerButton").addEventListener("keydown", handleChainPickerKeydown);
  $("combinationChainPickerButton").addEventListener("click", (event) => {
    event.stopPropagation();
    toggleChainPicker("combination");
  });
  $("combinationChainPickerButton").addEventListener("keydown", (event) => {
    handleChainPickerKeydown(event, "combination");
  });
  ["balanceHelpBtn", "combinationBalanceHelpBtn"].forEach((id) => {
    $(id).addEventListener("click", (event) => {
      event.stopPropagation();
      const control = event.currentTarget.closest(".help-control");
      const open = !control.classList.contains("open");
      control.classList.toggle("open", open);
      event.currentTarget.setAttribute("aria-expanded", String(open));
    });
  });
  document.addEventListener("click", (event) => {
    if (!$("chainPicker").contains(event.target)) closeChainPicker();
    if (!$("combinationChainPicker").contains(event.target)) closeChainPicker("combination");
    if (!event.target.closest(".help-control")) {
      document.querySelectorAll(".help-control.open").forEach((control) => control.classList.remove("open"));
      ["balanceHelpBtn", "combinationBalanceHelpBtn"].forEach((id) => {
        $(id).setAttribute("aria-expanded", "false");
        $(id).blur();
      });
    }
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closeAllChainPickers();
      document.querySelectorAll(".help-control.open").forEach((control) => control.classList.remove("open"));
      ["balanceHelpBtn", "combinationBalanceHelpBtn"].forEach((id) => {
        $(id).setAttribute("aria-expanded", "false");
        $(id).blur();
      });
    }
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
  updateCombinationTargetVisibility();
  updateSummaryTargetVisibility();
  updateDeliveryModeFields();
  setRuleCreationMode("single");
  renderRules();
  renderCombinationDraft();
  renderAggregateEvents({ resetScroll: true });
  renderMarket();
  updateConnectionButton();
  await loadBootData();
  if (!(await restoreSession())) {
    await loadPaymentConfig();
  }
}

boot().catch((error) => {
  toast(localizedApiError(error.message));
});
