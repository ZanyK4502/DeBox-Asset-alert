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
};

const $ = (id) => document.getElementById(id);
const I18N = window.H5_I18N;
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
  $("walletAddressInput").value = "";
  $("profileBox").innerHTML = t("noWallet");
  $("subscriptionBox").innerHTML = t("connectToView");
  renderRules();
  renderCombinationDraft();
  renderAggregateEvents();
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
  const complimentary = state.entitlement?.complimentary_access;
  const complimentaryAvailable = Boolean(
    complimentary?.available && !currentPaidPlan && state.selectedPlan !== "free"
  );
  const complimentaryActive = Boolean(complimentary?.used && currentPaidPlan);
  $("payBtn").textContent = state.selectedPlan === "free"
    ? t("activate")
    : complimentaryAvailable
      ? t("complimentaryActivate")
      : complimentaryActive
        ? t("currentPlanButton")
        : t("payRenew");
  $("payBtn").disabled = complimentaryActive;
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
    $("combinationGroupTargetSelect").innerHTML = `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
    $("summaryGroupSelect").innerHTML = `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
    $("groupsList").innerHTML = "";
    return;
  }
  const selectedRuleGroup = $("groupTargetSelect").value;
  const selectedCombinationGroup = $("combinationGroupTargetSelect").value;
  const selectedSummaryGroup = $("summaryGroupSelect").value;
  const options = state.groups.length
    ? state.groups.map((group) => `<option value="${escapeHtml(group.gid)}">${escapeHtml(group.name || group.gid)}</option>`).join("")
    : `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
  $("groupTargetSelect").innerHTML = options;
  $("combinationGroupTargetSelect").innerHTML = options;
  $("summaryGroupSelect").innerHTML = options;
  if (state.groups.some((group) => group.gid === selectedRuleGroup)) {
    $("groupTargetSelect").value = selectedRuleGroup;
  }
  if (state.groups.some((group) => group.gid === selectedSummaryGroup)) {
    $("summaryGroupSelect").value = selectedSummaryGroup;
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
  updateSummaryTargetVisibility();
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
      threshold: isThresholdlessRule(ruleType) ? "0" : $("combinationThresholdInput").value || "0",
      notification_language: $("combinationLanguageSelect").value,
      delivery_mode: "realtime",
      cycle_type: "fixed",
      cycle_minutes: 60,
      trigger_count_threshold: 1,
    },
  };
}

function resetCombinationMemberEditor() {
  $("combinationTokenAddressInput").value = "";
  $("combinationTargetAddressInput").value = "";
  $("combinationTargetLabelInput").value = "";
  $("combinationThresholdInput").value = "0";
  $("combinationMemberTriggerInput").value = "1";
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
  if (complimentary?.available && !currentPaidPlan && state.selectedPlan !== "free") {
    status.textContent = t("complimentaryAvailable");
  } else if (complimentary?.used && currentPaidPlan) {
    status.textContent = t("complimentaryActive", { date: complimentary.expires_at || "-" });
  } else if (state.paymentError) {
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
  ]);
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
    toast(t("enterMonitoredAddress"));
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
  const ruleType = $("ruleTypeSelect").value;
  const deliveryMode = $("deliveryModeSelect").value;
  const cycleMinutes = Number($("cycleMinutesInput").value);
  const triggerCount = Number($("triggerCountInput").value);
  const payload = {
    chain_key: $("chainSelect").value,
    wallet_address: $("walletAddressInput").value.trim(),
    token_address: $("tokenAddressInput").value.trim() || null,
    target_address: $("targetAddressInput").value.trim() || null,
    target_label: $("targetLabelInput").value.trim(),
    rule_type: ruleType,
    threshold: isThresholdlessRule(ruleType) ? "0" : $("thresholdInput").value || "0",
    notification_chat_type: targetType,
    notification_chat_id: targetType === "group" ? $("groupTargetSelect").value : "",
    notification_label: targetType === "group" && selectedGroup ? selectedGroup.textContent : "",
    notification_language: $("ruleLanguageSelect").value,
    delivery_mode: deliveryMode,
    cycle_type: $("cycleTypeSelect").value,
    cycle_minutes: deliveryMode === "stage" ? cycleMinutes : 60,
    trigger_count_threshold: deliveryMode === "stage" ? triggerCount : 1,
  };
  try {
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
  $("connectWalletBtn").addEventListener("click", guardAsync(toggleWalletConnection));
  $("freeTrialBtn").addEventListener("click", guardAsync(enableFreePlan));
  $("payBtn").addEventListener("click", guardAsync(payOrRenew));
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
  $("queryBalanceBtn").addEventListener("click", guardAsync(queryBalance));
  $("ruleForm").addEventListener("submit", guardAsync(createRule));
  $("combinationRuleForm").addEventListener("submit", guardAsync(createCombinationRule));
  $("singleRuleModeBtn").addEventListener("click", () => setRuleCreationMode("single"));
  $("combinationRuleModeBtn").addEventListener("click", () => setRuleCreationMode("combination"));
  $("addCombinationMemberBtn").addEventListener("click", addCombinationMember);
  $("groupForm").addEventListener("submit", guardAsync(addGroup));
  $("summaryForm").addEventListener("submit", guardAsync(saveSummary));
  $("targetTypeSelect").addEventListener("change", updateTargetVisibility);
  $("combinationTargetTypeSelect").addEventListener("change", updateCombinationTargetVisibility);
  $("summaryTargetSelect").addEventListener("change", updateSummaryTargetVisibility);
  $("ruleTypeSelect").addEventListener("change", updateRuleFields);
  $("combinationRuleTypeSelect").addEventListener("change", updateCombinationMemberFields);
  $("deliveryModeSelect").addEventListener("change", updateDeliveryModeFields);
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
  $("balanceHelpBtn").addEventListener("click", (event) => {
    event.stopPropagation();
    const control = event.currentTarget.closest(".help-control");
    const open = !control.classList.contains("open");
    control.classList.toggle("open", open);
    event.currentTarget.setAttribute("aria-expanded", String(open));
  });
  document.addEventListener("click", (event) => {
    if (!$("chainPicker").contains(event.target)) closeChainPicker();
    if (!$("combinationChainPicker").contains(event.target)) closeChainPicker("combination");
    if (!event.target.closest(".help-control")) {
      document.querySelectorAll(".help-control.open").forEach((control) => control.classList.remove("open"));
      $("balanceHelpBtn").setAttribute("aria-expanded", "false");
      $("balanceHelpBtn").blur();
    }
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closeAllChainPickers();
      document.querySelectorAll(".help-control.open").forEach((control) => control.classList.remove("open"));
      $("balanceHelpBtn").setAttribute("aria-expanded", "false");
      $("balanceHelpBtn").blur();
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
  updateConnectionButton();
  await loadBootData();
  if (!(await restoreSession())) {
    await loadPaymentConfig();
  }
}

boot().catch((error) => {
  toast(localizedApiError(error.message));
});
