const UI_LANGUAGE_STORAGE_KEY = "debox_asset_alert_h5_language";
const NOTIFICATION_ID_PATTERN = /^nd_[a-f0-9]{40}$/;

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
  notificationDetailID: notificationIDFromLocation(),
  notificationDetail: null,
  notificationDetailLoading: false,
  notificationDetailError: "",
  detailDrawer: null,
  marketCatalog: null,
  marketGoal: "price",
  marketWizard: freshMarketWizard(),
  marketProjects: [],
  marketDetail: null,
  marketDetailTab: "overview",
  marketRuleMode: "single",
  marketHolderChain: "",
  marketLabelChain: "",
  marketExpandedPoolChains: new Set(),
  marketHoldersExpanded: false,
  marketEventsExpanded: false,
  marketRecommendations: [],
  marketRecommendationUpdatedAt: "",
  marketRecommendationLoading: false,
  marketRecommendationError: "",
  marketEvents: [],
  marketEventsNextBeforeId: null,
  marketEventFilters: freshMarketEventFilters(),
};

function freshMarketWizard() {
  return {
    step: 1,
    mode: "name",
    searchResult: null,
    selectedAsset: null,
    selectedChains: new Set(),
    manualRows: [{ chainKey: "bsc", contractAddress: "" }],
    manualResult: null,
    verification: null,
    poolResult: null,
    poolSelections: {},
    recommendations: [],
    recommendationUpdatedAt: "",
    recommendationLoading: false,
    recommendationError: "",
    searchRequest: 0,
    busy: false,
  };
}

function freshMarketEventFilters() {
  return {
    chainKey: "",
    ruleType: "",
    poolId: "",
    address: "",
  };
}

const $ = (id) => document.getElementById(id);
const I18N = window.H5_I18N;
const TIME = window.H5_TIME;
let marketSearchTimer = 0;
const MOBILE_SHELL_QUERY = "(max-width: 768px)";
const MOBILE_VIEW_STORAGE_KEY = "debox_asset_alert_mobile_view";
const MOBILE_VIEWS = new Set(["overview", "monitoring", "market", "address", "account"]);
const mobileScrollPositions = Object.create(null);
let mobileShellMedia = null;
let mobileActionFrame = 0;
let mobileMarketStep = "";
let eventDrawerReturnFocus = null;

function notificationIDFromLocation() {
  return (new URLSearchParams(window.location.search).get("notification_id") || "").trim();
}

function storedMobileView() {
  return "account";
}

function isMobileShell() {
  return Boolean(mobileShellMedia?.matches);
}

function mobileScreenForElement(element) {
  const screen = element?.closest?.("[data-mobile-screen]")?.dataset.mobileScreen;
  return MOBILE_VIEWS.has(screen) ? screen : "";
}

function mobileHashTarget() {
  const rawID = window.location.hash.replace(/^#/, "");
  if (!rawID) return null;
  try {
    return document.getElementById(decodeURIComponent(rawID));
  } catch (_) {
    return document.getElementById(rawID);
  }
}

function updateMobileNavigation(view) {
  document.querySelectorAll("[data-mobile-tab]").forEach((button) => {
    const active = button.dataset.mobileTab === view;
    button.classList.toggle("active", active);
    if (active) {
      button.setAttribute("aria-current", "page");
    } else {
      button.removeAttribute("aria-current");
    }
  });
}

function setMobileView(view, options = {}) {
  const nextView = MOBILE_VIEWS.has(view) ? view : "account";
  const { restoreScroll = true, target = null } = options;
  const previousView = document.body.dataset.mobileView;
  if (isMobileShell() && previousView && previousView !== nextView) {
    mobileScrollPositions[previousView] = window.scrollY;
    const focusedScreen = mobileScreenForElement(document.activeElement);
    if (focusedScreen && focusedScreen !== nextView) document.activeElement.blur();
  }

  document.body.dataset.mobileView = nextView;
  updateMobileNavigation(nextView);

  document.querySelectorAll("[data-mobile-screen]").forEach((section) => {
    const active = section.dataset.mobileScreen === nextView;
    section.classList.toggle("mobile-screen-active", active);
    if (isMobileShell()) {
      section.inert = !active;
      section.setAttribute("aria-hidden", String(!active));
    } else {
      section.inert = false;
      section.removeAttribute("aria-hidden");
    }
  });

  if (isMobileShell()) {
    try {
      sessionStorage.setItem(MOBILE_VIEW_STORAGE_KEY, nextView);
    } catch (_) {
      // Navigation still works when embedded browsers disable storage.
    }
    requestAnimationFrame(() => {
      if (target?.isConnected) {
        target.scrollIntoView({ block: "start" });
      } else if (restoreScroll) {
        window.scrollTo(0, mobileScrollPositions[nextView] || 0);
      } else {
        window.scrollTo(0, 0);
      }
      keepMobileMarketStepVisible();
      scheduleMobileActionBarUpdate();
    });
  } else {
    scheduleMobileActionBarUpdate();
  }
}

function openMobileHashTarget() {
  if (!isMobileShell()) return;
  const target = mobileHashTarget();
  const view = mobileScreenForElement(target);
  if (view) setMobileView(view, { restoreScroll: false, target });
}

function keepMobileMarketStepVisible() {
  if (!isMobileShell() || document.body.dataset.mobileView !== "market") return;
  const guide = document.querySelector(".market-guide");
  const active = guide?.querySelector(".market-guide-step.active");
  if (!guide || !active) return;
  const step = active.dataset.marketWizardStep || "";
  if (step === mobileMarketStep && active.offsetLeft >= guide.scrollLeft &&
      active.offsetLeft + active.offsetWidth <= guide.scrollLeft + guide.clientWidth) {
    return;
  }
  mobileMarketStep = step;
  const left = Math.max(0, active.offsetLeft - (guide.clientWidth - active.offsetWidth) / 2);
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  guide.scrollTo({ left, behavior: reducedMotion ? "auto" : "smooth" });
}

function updateMobileKeyboardState() {
  const viewport = window.visualViewport;
  const keyboardOpen = Boolean(
    isMobileShell() &&
    viewport &&
    window.innerHeight - viewport.height > 150,
  );
  document.body.classList.toggle("mobile-keyboard-open", keyboardOpen);
  scheduleMobileActionBarUpdate();
}

function isIOSDeBoxWebView() {
  const userAgent = navigator.userAgent || "";
  const platform = navigator.platform || "";
  const isIOS = /iPad|iPhone|iPod/i.test(userAgent) ||
    (platform === "MacIntel" && navigator.maxTouchPoints > 1);
  return isIOS && (/DeBox/i.test(userAgent) || Boolean(window.deboxWallet));
}

function isTextEntryControl(element) {
  if (!(element instanceof HTMLElement)) return false;
  if (element.matches("textarea, select")) return !element.hasAttribute("disabled");
  if (!(element instanceof HTMLInputElement)) return false;
  return !element.disabled && !element.readOnly && ![
    "button",
    "checkbox",
    "color",
    "file",
    "hidden",
    "image",
    "radio",
    "range",
    "reset",
    "submit",
  ].includes(element.type);
}

function viewportWithMaximumScale(content) {
  const directives = String(content || "")
    .split(",")
    .map((directive) => directive.trim())
    .filter((directive) => directive && !/^maximum-scale\s*=/i.test(directive));
  directives.push("maximum-scale=1");
  return directives.join(", ");
}

function initIOSDeBoxInputZoomGuard() {
  if (!isIOSDeBoxWebView()) return;
  const viewportMeta = document.querySelector('meta[name="viewport"]');
  if (!viewportMeta) return;

  document.documentElement.classList.add("ios-debox-webview");
  const defaultViewport = viewportMeta.getAttribute("content") || "width=device-width, initial-scale=1";
  const focusedViewport = viewportWithMaximumScale(defaultViewport);
  let restoreTimer = 0;

  const lockViewport = (event) => {
    if (!isTextEntryControl(event.target)) return;
    window.clearTimeout(restoreTimer);
    viewportMeta.setAttribute("content", focusedViewport);
  };
  const restoreViewport = () => {
    window.clearTimeout(restoreTimer);
    restoreTimer = window.setTimeout(() => {
      if (isTextEntryControl(document.activeElement)) return;
      viewportMeta.setAttribute("content", defaultViewport);
    }, 250);
  };

  document.addEventListener("touchstart", lockViewport, { capture: true, passive: true });
  document.addEventListener("pointerdown", lockViewport, true);
  document.addEventListener("focusin", lockViewport, true);
  document.addEventListener("focusout", restoreViewport, true);
}

function updateMobileActionBar() {
  mobileActionFrame = 0;
  const scopes = [...document.querySelectorAll("[data-mobile-action-scope]")];
  scopes.forEach((scope) => scope.classList.remove("mobile-action-scope-active"));
  document.querySelectorAll("[data-mobile-action-bar]").forEach((bar) => {
    bar.classList.remove("mobile-action-active");
  });
  document.body.classList.remove("has-mobile-action-bar");

  if (!isMobileShell() || document.body.classList.contains("mobile-keyboard-open")) return;

  const viewportTop = Math.max(0, document.querySelector(".app-header")?.getBoundingClientRect().bottom || 0);
  const viewportBottom = window.visualViewport?.height || window.innerHeight;
  const viewportCenter = viewportTop + (viewportBottom - viewportTop) / 2;
  let best = null;

  scopes.forEach((scope) => {
    if (scope.hidden || scope.offsetParent === null) return;
    const screen = mobileScreenForElement(scope);
    if (screen && screen !== document.body.dataset.mobileView) return;
    const rect = scope.getBoundingClientRect();
    const visibleTop = Math.max(viewportTop, rect.top);
    const visibleBottom = Math.min(viewportBottom, rect.bottom);
    if (visibleBottom - visibleTop < 72) return;
    const nearestPoint = Math.max(rect.top, Math.min(viewportCenter, rect.bottom));
    const score = Math.abs(nearestPoint - viewportCenter);
    if (!best || score < best.score) best = { scope, score };
  });

  const bar = best?.scope.querySelector("[data-mobile-action-bar]");
  if (!bar || bar.offsetParent === null) return;
  best.scope.classList.add("mobile-action-scope-active");
  bar.classList.add("mobile-action-active");
  document.body.classList.add("has-mobile-action-bar");
}

function scheduleMobileActionBarUpdate() {
  if (mobileActionFrame) return;
  mobileActionFrame = requestAnimationFrame(updateMobileActionBar);
}

function syncMobileShellMode() {
  const nav = $("mobileBottomNav");
  document.body.classList.add("mobile-shell-ready");
  nav.setAttribute("aria-hidden", String(!isMobileShell()));

  if (!isMobileShell()) {
    document.querySelectorAll("[data-mobile-screen]").forEach((section) => {
      section.inert = false;
      section.removeAttribute("aria-hidden");
    });
    document.body.classList.remove("mobile-keyboard-open");
    scheduleMobileActionBarUpdate();
    return;
  }

  const hashTarget = mobileHashTarget();
  const hashView = mobileScreenForElement(hashTarget);
  setMobileView(hashView || document.body.dataset.mobileView || storedMobileView(), {
    restoreScroll: !hashView,
    target: hashView ? hashTarget : null,
  });
  updateMobileKeyboardState();
}

function initMobileShell() {
  mobileShellMedia = window.matchMedia(MOBILE_SHELL_QUERY);
  document.querySelectorAll("[data-mobile-tab]").forEach((button) => {
    button.addEventListener("click", () => {
      if (!isMobileShell()) return;
      setMobileView(button.dataset.mobileTab);
    });
  });
  if (mobileShellMedia.addEventListener) {
    mobileShellMedia.addEventListener("change", syncMobileShellMode);
  } else {
    mobileShellMedia.addListener(syncMobileShellMode);
  }
  window.addEventListener("hashchange", openMobileHashTarget);
  window.addEventListener("scroll", scheduleMobileActionBarUpdate, { passive: true });
  window.addEventListener("resize", scheduleMobileActionBarUpdate, { passive: true });
  window.visualViewport?.addEventListener("resize", updateMobileKeyboardState);
  window.visualViewport?.addEventListener("scroll", updateMobileKeyboardState);
  const observer = new MutationObserver(scheduleMobileActionBarUpdate);
  document.querySelectorAll("[data-mobile-action-scope]").forEach((scope) => {
    observer.observe(scope, { attributes: true, attributeFilter: ["hidden"] });
  });
  syncMobileShellMode();
}

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

const SUMMARY_TIMEZONE_OPTIONS = [
  ["Asia/Shanghai", "timezoneShanghai"],
  ["Asia/Tokyo", "timezoneTokyo"],
  ["Asia/Bangkok", "timezoneBangkok"],
  ["Asia/Kolkata", "timezoneKolkata"],
  ["Europe/Berlin", "timezoneBerlin"],
  ["Europe/London", "timezoneLondon"],
  ["America/New_York", "timezoneNewYork"],
  ["America/Los_Angeles", "timezoneLosAngeles"],
  ["UTC", "timezoneUtc"],
];
const SUMMARY_TIMEZONES = new Set(SUMMARY_TIMEZONE_OPTIONS.map(([timezone]) => timezone));

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

async function copyText(value) {
  const text = String(value || "");
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch (_) {
      // Embedded webviews can expose Clipboard API while denying access.
    }
  }
  const input = document.createElement("textarea");
  input.value = text;
  input.setAttribute("readonly", "");
  input.style.position = "fixed";
  input.style.opacity = "0";
  document.body.appendChild(input);
  input.select();
  const copied = document.execCommand("copy");
  input.remove();
  if (!copied) throw new Error(t("requestFailed"));
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
  renderSummaryTargetOptions(selectedSummaryTargets());
  renderSummaryCapability();
  renderGroups();
  renderRules();
  renderAggregateEvents();
  renderPaymentStatus();
  renderTokenInfo();
  renderBalanceInfo();
  renderBalanceInfo("combination");
  renderMarket();
  renderNotificationDetailPage();
  renderEventDetailDrawer();
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

function notificationDetailOptions() {
  return {
    t,
    language: state.uiLanguage,
    ruleLabel: notificationRuleLabel,
    chainName: aggregateChainName,
    eventLabel: marketEventLabel,
  };
}

function notificationRuleLabel(code) {
  return marketRuleName(marketRuleDefinition(code)) || ruleLabel(code);
}

function bindNotificationDetailActions() {
  const content = $("notificationDetailContent");
  if (!content || !state.notificationDetail) return;
  const values = Array.isArray(state.notificationDetail.copy_values)
    ? state.notificationDetail.copy_values
    : [];
  content.querySelectorAll("[data-notification-copy]").forEach((button) => {
    button.addEventListener("click", guardAsync(async () => {
      const item = values[Number(button.dataset.notificationCopy)];
      if (!item) return;
      await copyText(item.value);
      toast(t("copied"));
    }));
  });
}

function notificationDetailErrorKey(error) {
  if (error?.status === 410) return "notificationDetailExpired";
  if (error?.status === 404) return "notificationDetailNotFound";
  if (error?.status === 400) return "notificationDetailInvalid";
  return "notificationDetailLoadFailed";
}

function renderNotificationDetailPage() {
  const page = $("notificationDetailPage");
  if (!page) return;
  const active = Boolean(state.notificationDetailID);
  page.hidden = !active;
  document.body.classList.toggle("notification-detail-route", active);
  if (!active) return;

  const status = $("notificationDetailStatus");
  const content = $("notificationDetailContent");
  content.innerHTML = "";
  status.hidden = false;
  status.className = "notification-detail-status";

  if (!NOTIFICATION_ID_PATTERN.test(state.notificationDetailID)) {
    status.classList.add("error");
    status.innerHTML = `
      <div class="notification-detail-state-card">
        <span aria-hidden="true">!</span>
        <div><strong>${escapeHtml(t("notificationDetailInvalid"))}</strong><p>${escapeHtml(t("notificationDetailInvalidHint"))}</p></div>
      </div>
    `;
    return;
  }

  if (!state.deboxUserId) {
    status.innerHTML = `
      <div class="notification-detail-state-card">
        <span aria-hidden="true">🔒</span>
        <div><strong>${escapeHtml(t("notificationDetailSignInTitle"))}</strong><p>${escapeHtml(t("notificationDetailSignIn"))}</p></div>
        <button class="primary" type="button" data-notification-detail-connect>${escapeHtml(t("connectWallet"))}</button>
      </div>
    `;
    status.querySelector("[data-notification-detail-connect]")
      ?.addEventListener("click", guardAsync(toggleWalletConnection));
    return;
  }
  if (state.notificationDetailLoading) {
    status.innerHTML = `<div class="notification-detail-state-card loading"><span class="notification-detail-spinner" aria-hidden="true"></span><p>${escapeHtml(t("notificationDetailLoading"))}</p></div>`;
    return;
  }
  if (state.notificationDetailError) {
    const key = notificationDetailErrorKey(state.notificationDetailError);
    const retry = ![400, 404, 410].includes(state.notificationDetailError.status);
    status.classList.add("error");
    status.innerHTML = `
      <div class="notification-detail-state-card">
        <span aria-hidden="true">!</span>
        <div><strong>${escapeHtml(t(key))}</strong><p>${escapeHtml(state.notificationDetailError.message || t(key))}</p></div>
        ${retry ? `<button class="secondary" type="button" data-notification-detail-retry>${escapeHtml(t("retry"))}</button>` : ""}
      </div>
    `;
    status.querySelector("[data-notification-detail-retry]")
      ?.addEventListener("click", guardAsync(loadNotificationDetail));
    return;
  }
  if (!state.notificationDetail) {
    status.innerHTML = `<div class="notification-detail-state-card"><p>${escapeHtml(t("notificationDetailWaiting"))}</p></div>`;
    return;
  }

  status.hidden = true;
  const renderer = window.H5_NOTIFICATION_DETAIL;
  if (!renderer?.render) {
    status.hidden = false;
    status.innerHTML = `<div class="notification-detail-state-card"><p>${escapeHtml(t("notificationDetailLoadFailed"))}</p></div>`;
    return;
  }
  content.innerHTML = renderer.render(state.notificationDetail, notificationDetailOptions());
  bindNotificationDetailActions();
}

async function loadNotificationDetail() {
  if (!state.notificationDetailID || !state.deboxUserId || state.notificationDetailLoading) return false;
  if (!NOTIFICATION_ID_PATTERN.test(state.notificationDetailID)) {
    state.notificationDetailError = { status: 400, message: t("notificationDetailInvalidHint") };
    renderNotificationDetailPage();
    return false;
  }
  state.notificationDetailLoading = true;
  state.notificationDetailError = "";
  renderNotificationDetailPage();
  try {
    state.notificationDetail = await api(`/api/notification-details/${encodeURIComponent(state.notificationDetailID)}`);
    return true;
  } catch (error) {
    state.notificationDetail = null;
    state.notificationDetailError = { status: error?.status || 0, message: localizedApiError(error?.message) };
    return false;
  } finally {
    state.notificationDetailLoading = false;
    renderNotificationDetailPage();
  }
}

function closeNotificationDetailPage() {
  const url = new URL(window.location.href);
  url.searchParams.delete("notification_id");
  window.history.replaceState({}, "", `${url.pathname}${url.search}${url.hash}`);
  state.notificationDetailID = "";
  state.notificationDetail = null;
  state.notificationDetailError = "";
  renderNotificationDetailPage();
  window.scrollTo({ top: 0, behavior: "smooth" });
}

function syncNotificationDetailRoute() {
  const nextID = notificationIDFromLocation();
  if (nextID !== state.notificationDetailID) {
    state.notificationDetailID = nextID;
    state.notificationDetail = null;
    state.notificationDetailError = "";
  }
  renderNotificationDetailPage();
  if (NOTIFICATION_ID_PATTERN.test(state.notificationDetailID) && state.deboxUserId && !state.notificationDetail) {
    guardAsync(loadNotificationDetail)();
  }
}

function guardAsync(handler) {
  return (...args) => {
    Promise.resolve(handler(...args)).catch((error) => {
      toast(localizedApiError(error?.message));
    });
  };
}

async function runManualRefresh(buttonId, handler) {
  const button = $(buttonId);
  if (!button || button.dataset.refreshLoading === "true") return;
  const originalI18n = button.dataset.i18n || "";
  const originalText = button.textContent;
  const originallyDisabled = button.disabled;
  button.dataset.refreshLoading = "true";
  button.classList.add("is-refreshing");
  button.setAttribute("aria-busy", "true");
  button.disabled = true;
  button.dataset.i18n = "refreshing";
  button.textContent = t("refreshing");
  try {
    const refreshed = await handler();
    if (refreshed !== false) toast(t("refreshSuccess"));
  } catch (error) {
    toast(localizedApiError(error?.message));
  } finally {
    delete button.dataset.refreshLoading;
    button.classList.remove("is-refreshing");
    button.setAttribute("aria-busy", "false");
    button.disabled = originallyDisabled;
    if (originalI18n) {
      button.dataset.i18n = originalI18n;
      button.textContent = t(originalI18n);
    } else {
      delete button.dataset.i18n;
      button.textContent = originalText;
    }
  }
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

function marketPoolDiscoveryAllowed() {
  return currentPlan()?.market_query === true;
}

function syncMarketPoolDiscoveryAccess() {
  const button = $("marketVerifyAndDiscoverBtn");
  if (!button) return;
  const allowed = marketPoolDiscoveryAllowed();
  button.disabled = state.marketWizard.busy || !allowed;
  button.title = allowed ? "" : t("marketPoolDiscoveryPaidOnly");
  if (!allowed && state.marketWizard.step === 2) {
    $("marketIdentityStatus").textContent = t("marketPoolDiscoveryPaidOnly");
  }
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

function closeMarketManualChainPickers() {
  document.querySelectorAll("[data-market-manual-chain-menu]").forEach((menu) => {
    menu.hidden = true;
  });
  document.querySelectorAll("[data-market-manual-chain-button]").forEach((button) => {
    button.setAttribute("aria-expanded", "false");
  });
}

function closeAllChainPickers() {
  closeChainPicker();
  closeChainPicker("combination");
  closeMarketManualChainPickers();
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
  state.notificationDetail = null;
  state.notificationDetailLoading = false;
  state.notificationDetailError = "";
  state.detailDrawer = null;
  state.marketCatalog = null;
  state.marketWizard = freshMarketWizard();
  state.marketProjects = [];
  state.marketDetail = null;
  state.marketDetailTab = "overview";
  state.marketRuleMode = "single";
  state.marketHolderChain = "";
  state.marketLabelChain = "";
  state.marketExpandedPoolChains = new Set();
  state.marketHoldersExpanded = false;
  state.marketEventsExpanded = false;
  state.marketRecommendations = [];
  state.marketRecommendationUpdatedAt = "";
  state.marketRecommendationLoading = false;
  state.marketRecommendationError = "";
  state.marketEvents = [];
  state.marketEventsNextBeforeId = null;
  state.marketEventFilters = freshMarketEventFilters();
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
  renderSummaryTargetOptions([]);
  renderSummaryStatus();
  renderPlans();
  updateConnectionButton();
  renderNotificationDetailPage();
  closeEventDetailDrawer();
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
  const permanent = Boolean(state.entitlement?.permanent);
  const currentPaidPlan = ["standard", "professional"].includes(currentPlan()?.code)
    ? currentPlan().code
    : "";
  $("plansGrid").innerHTML = state.plans
    .map((plan) => {
      const selectable = plan.code !== "free";
      const tag = selectable ? "button" : "div";
      const active = selectable && plan.code === state.selectedPlan ? " active" : "";
      const locked = selectable && Boolean(
        permanent || (currentPaidPlan && plan.code !== currentPaidPlan)
      );
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
  renderPurchaseSummary(permanent);
  $("payBtn").textContent = permanent
    ? t("permanentPlanButton")
    : t("payRenew");
  $("payBtn").disabled = permanent;
}

function renderPurchaseSummary(permanent = Boolean(state.entitlement?.permanent)) {
  const summary = $("purchaseSummary");
  const plan = state.plans.find((item) => item.code === state.selectedPlan);
  if (permanent || !plan || plan.price === "0") {
    summary.hidden = true;
    summary.textContent = "";
    return;
  }
  const option = selectedBillingOption(plan);
  summary.hidden = false;
  summary.textContent = t("purchaseSummary", {
    plan: localizedPlan(plan).name,
    cycle: t(state.selectedBillingCycle),
    price: option.price,
    asset: plan.asset || "USDT",
  });
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
  const isPermanent = Boolean(state.entitlement.permanent);
  const planText = localizedPlan(plan);
  const freeHint =
    state.entitlement.paid_history && state.entitlement.fallback_free
      ? t("freeRestoreHint")
      : t("freeUpgradeHint");
  box.innerHTML = `
    <div class="metric-row">
      <strong>${escapeHtml(planText.name)}</strong>
      <span>${escapeHtml(isFree || isPermanent ? t("permanent") : t("remainingDays", { days: state.entitlement.days_remaining }))}</span>
    </div>
    <div class="mini-grid">
      <span>${escapeHtml(t("walletMetric", { used: state.entitlement.wallet_count, limit: plan.wallet_limit }))}</span>
      <span>${escapeHtml(t("ruleMetric", { used: state.entitlement.rule_count, limit: plan.rule_limit }))}</span>
      <span>${escapeHtml(t("groupMetric", { used: state.entitlement.group_count, limit: plan.group_limit }))}</span>
      <span>${escapeHtml(t("marketProjectMetric", { used: state.entitlement.market_project_count, limit: plan.market_project_limit }))}</span>
    </div>
    <small class="muted">${escapeHtml(
      isFree
        ? freeHint
        : isPermanent
          ? t("permanentPlanActive")
          : t("expiresAt", { date: TIME.formatExpiryDate(sub.expires_at, state.uiLanguage) })
    )}</small>
  `;
  if (syncSummary) fillSummaryForm();
}

function renderGroups() {
  const draftSummaryTargets = selectedSummaryTargets();
  if (!state.deboxUserId) {
    $("groupTargetSelect").innerHTML = `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
    $("combinationGroupTargetSelect").innerHTML = `<option value="">${escapeHtml(t("noBoundGroups"))}</option>`;
    $("groupsList").innerHTML = "";
    renderSummaryTargetOptions([]);
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
  renderSummaryTargetOptions(draftSummaryTargets);
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
        <strong></strong>
        <span></span>
        <small></small>
      </div>
      <div class="aggregate-event-side">
        <strong></strong>
        <span></span>
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
  if (Number.isNaN(date.getTime())) return "";
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
  return event.note || event.target_label || "";
}

function aggregateChainName(chainKey) {
  return state.chains.find((chain) => chain.key === chainKey)?.name || chainKey || "";
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
  const current = Number(event.window_total_trigger_count || 0);
  const required = Math.max(1, Number(event.required_trigger_count || 1));
  const progressPercent = Math.min(100, Math.max(0, current / required * 100));
  return `
    <article class="aggregate-event aggregate-timeline-item">
      <span class="aggregate-timeline-dot" aria-hidden="true"></span>
      <div class="aggregate-event-main">
        <div class="aggregate-event-title-row">
          <span class="aggregate-kind">${escapeHtml(kind)}</span>
          <strong>${escapeHtml(title)}</strong>
        </div>
        <span>${escapeHtml(`${aggregateChainName(event.chain_key)} · ${shortAddress(event.wallet_address)}`)}</span>
        <p>${escapeHtml(aggregateEventValue(event) || t("eventDetails"))}</p>
        <div class="aggregate-progress" aria-label="${escapeHtml(progress)}">
          <span style="width: ${progressPercent}%"></span>
        </div>
        <small>${escapeHtml(progress)}</small>
      </div>
      <div class="aggregate-event-side">
        <strong class="${status.className}">${escapeHtml(status.text)}</strong>
        <time>${escapeHtml(formatAggregateEventTime(event.occurred_at || event.detected_at || event.created_at))}</time>
        <button class="secondary compact" type="button" data-aggregate-event-detail="${escapeHtml(event.id)}">${escapeHtml(t("viewDetails"))}</button>
      </div>
    </article>
  `;
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
    .map(([label, value]) => aggregateMetricHtml(label, showValues ? String(value ?? 0) : "", !showValues))
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
    list.querySelectorAll("[data-aggregate-event-detail]").forEach((button) => {
      button.addEventListener("click", () => openEventDetailDrawer("aggregate", button.dataset.aggregateEventDetail));
    });
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
  if (resetScroll) $("aggregateEventViewport").scrollTop = 0;
}

async function loadAggregateEvents({ append = false } = {}) {
  if (!state.deboxUserId || state.aggregateLoading || state.aggregateLoadingMore) return false;
  if (append && (!state.aggregateHasMore || !state.aggregateNextBeforeId)) return false;
  let loaded = false;
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
    }
    state.aggregateStats = page.stats || null;
    state.aggregateRetentionDays = Number(page.retention_days || 30);
    state.aggregateHasMore = Boolean(page.has_more);
    state.aggregateNextBeforeId = page.next_before_id || null;
    state.aggregateLoadError = "";
    loaded = true;
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
  return loaded;
}

function detailFactsHtml(items) {
  return `<dl class="event-detail-facts">${items
    .filter(([, value]) => value !== null && value !== undefined && String(value) !== "")
    .map(([label, value]) => `<div><dt>${escapeHtml(label)}</dt><dd>${escapeHtml(value)}</dd></div>`)
    .join("")}</dl>`;
}

function detailSectionHtml(title, content) {
  return content ? `<section class="event-detail-section"><h3>${escapeHtml(title)}</h3>${content}</section>` : "";
}

function detailCopyRow(label, value) {
  if (!value) return "";
  return `
    <div class="event-detail-copy-row">
      <div><span>${escapeHtml(label)}</span><code>${escapeHtml(value)}</code></div>
      <button class="secondary compact" type="button" data-event-detail-copy="${escapeHtml(value)}">${escapeHtml(t("copy"))}</button>
    </div>
  `;
}

function aggregateEventDetailHtml(event) {
  const combination = event.source_type === "combination";
  const current = Number(event.window_total_trigger_count || 0);
  const required = Math.max(1, Number(event.required_trigger_count || 1));
  const status = aggregateEventStatus(event);
  const ruleTitle = combination ? event.combination_note || t("combinationLabel") : ruleLabel(event.rule_type);
  const copyRows = [
    detailCopyRow(t("monitoredAddress"), event.wallet_address),
    detailCopyRow(t("tokenContract"), event.token_address),
    detailCopyRow(t("targetAddress"), event.target_address),
  ].join("");
  return `
    <div class="event-detail-summary">
      <span class="aggregate-kind">${escapeHtml(t(combination ? "combinationEvent" : "stageEvent"))}</span>
      <h3>${escapeHtml(ruleTitle)}</h3>
      <p>${escapeHtml(aggregateEventValue(event) || t("eventDetails"))}</p>
    </div>
    <div class="event-detail-metrics">
      <div><span>${escapeHtml(t("detailTriggerCount"))}</span><strong>${escapeHtml(current)}</strong></div>
      <div><span>${escapeHtml(t("detailRequiredCount"))}</span><strong>${escapeHtml(required)}</strong></div>
      <div><span>${escapeHtml(t("eventDetailDelivery"))}</span><strong class="${status.className}">${escapeHtml(status.text)}</strong></div>
    </div>
    ${detailSectionHtml(t("eventDetailRuleBasis"), detailFactsHtml([
      [t("ruleType"), ruleTitle],
      [t("chain"), aggregateChainName(event.chain_key)],
      [t("detailPeriod"), `${marketDate(event.window_starts_at)} — ${marketDate(event.window_ends_at)}`],
      [t("eventDetailCycle"), `${event.cycle_minutes || 0} ${t("minutes")}`],
    ]))}
    ${detailSectionHtml(t("eventDetailResult"), detailFactsHtml([
      [t("detailOccurredAt"), marketDate(event.occurred_at || event.detected_at || event.created_at)],
      [t("detailPreviousValue"), event.previous_value],
      [t("detailCurrentValue"), event.current_value],
      [t("note"), event.note || event.target_label],
      [t("notificationFailedReason"), event.notification_error],
    ]))}
    ${copyRows ? detailSectionHtml(t("detailCopyValues"), `<div class="event-detail-copy-list">${copyRows}</div>`) : ""}
  `;
}

function marketEventSummary(event) {
  const actual = marketRuleEventValue(event.current_value, event.threshold_unit);
  const thresholdless = ["market_new_pool", "market_four_meme_migration"].includes(event.rule_type);
  if (thresholdless) return `${marketEventLabel(event.event_type)} · ${marketChainName(event.chain_key)}`;
  return t("marketEventSummary", {
    event: marketEventLabel(event.event_type),
    actual,
    threshold: marketRuleEventValue(event.threshold_value, event.threshold_unit),
  });
}

function marketEventDetailHtml(event) {
  const pool = (state.marketDetail?.pools || []).find((item) => item.id === event.market_pool_id);
  const explorer = chainExplorerTransaction(event.chain_key, event.transaction_hash);
  const copyRows = [
    detailCopyRow(t("holderAddress"), event.wallet_address),
    detailCopyRow(t("eventDetailTransaction"), event.transaction_hash),
  ].join("");
  return `
    <div class="event-detail-summary">
      <span class="aggregate-kind">${escapeHtml(marketEventLabel(event.event_type))}</span>
      <h3>${escapeHtml(marketRuleDisplayName(event.rule_type))}</h3>
      <p>${escapeHtml(marketEventSummary(event))}</p>
    </div>
    <div class="event-detail-metrics">
      <div><span>${escapeHtml(t("detailActualValue"))}</span><strong>${escapeHtml(marketRuleEventValue(event.current_value, event.threshold_unit))}</strong></div>
      <div><span>${escapeHtml(t("threshold"))}</span><strong>${escapeHtml(marketRuleEventValue(event.threshold_value, event.threshold_unit))}</strong></div>
      <div><span>${escapeHtml(t("eventDetailDelivery"))}</span><strong class="${event.notification_successful ? "sent" : "failed"}">${escapeHtml(t(event.notification_successful ? "marketEventNotified" : "marketEventNotNotified"))}</strong></div>
    </div>
    ${detailSectionHtml(t("eventDetailRuleBasis"), detailFactsHtml([
      [t("ruleType"), marketRuleDisplayName(event.rule_type)],
      [t("marketEventType"), marketEventLabel(event.event_type)],
      [t("chain"), marketChainName(event.chain_key)],
      [t("marketPool"), pool ? `${pool.protocol} ${pool.protocol_version} · ${pool.token0_symbol}/${pool.token1_symbol}` : ""],
      [t("marketEventThreshold"), marketRuleEventValue(event.threshold_value, event.threshold_unit)],
    ]))}
    ${detailSectionHtml(t("eventDetailResult"), detailFactsHtml([
      [t("detailOccurredAt"), marketDate(event.occurred_at)],
      [t("detailCurrentValue"), marketRuleEventValue(event.current_value, event.threshold_unit)],
      [t("marketEventAddressLabel"), event.address_label],
      [t("notificationFailedReason"), event.notification_successful ? "" : marketRuleEventReason(event)],
    ]))}
    ${copyRows ? detailSectionHtml(t("detailCopyValues"), `<div class="event-detail-copy-list">${copyRows}</div>`) : ""}
    ${explorer ? `<a class="primary button-link event-detail-explorer" href="${escapeHtml(explorer)}" target="_blank" rel="noopener noreferrer">${escapeHtml(t("viewOnExplorer"))}</a>` : ""}
  `;
}

function renderEventDetailDrawer() {
  const backdrop = $("eventDetailBackdrop");
  if (!backdrop) return;
  const selection = state.detailDrawer;
  if (!selection) {
    backdrop.hidden = true;
    document.body.classList.remove("detail-drawer-open");
    document.querySelectorAll(".app-header, .layout, .usage-help-fab, .mobile-bottom-nav")
      .forEach((element) => { element.inert = false; });
    return;
  }
  const event = selection.kind === "aggregate"
    ? state.aggregateEvents.find((item) => String(item.id) === String(selection.id))
    : state.marketEvents.find((item) => String(item.id) === String(selection.id));
  if (!event) {
    state.detailDrawer = null;
    backdrop.hidden = true;
    document.body.classList.remove("detail-drawer-open");
    document.querySelectorAll(".app-header, .layout, .usage-help-fab, .mobile-bottom-nav")
      .forEach((element) => { element.inert = false; });
    return;
  }
  const aggregate = selection.kind === "aggregate";
  $("eventDetailDrawerEyebrow").textContent = aggregate ? t("addressEventEyebrow") : t("marketEventEyebrow");
  $("eventDetailDrawerTitle").textContent = aggregate ? t("addressEventDetails") : t("marketEventDetails");
  $("eventDetailDrawerContent").innerHTML = aggregate
    ? aggregateEventDetailHtml(event)
    : marketEventDetailHtml(event);
  $("eventDetailDrawerContent").querySelectorAll("[data-event-detail-copy]").forEach((button) => {
    button.addEventListener("click", guardAsync(async () => {
      await copyText(button.dataset.eventDetailCopy);
      toast(t("copied"));
    }));
  });
  backdrop.hidden = false;
  document.body.classList.add("detail-drawer-open");
  document.querySelectorAll(".app-header, .layout, .usage-help-fab, .mobile-bottom-nav")
    .forEach((element) => { element.inert = true; });
}

function openEventDetailDrawer(kind, id) {
  eventDrawerReturnFocus = document.activeElement;
  state.detailDrawer = { kind, id };
  renderEventDetailDrawer();
  $("closeEventDetailDrawerBtn").focus();
}

function closeEventDetailDrawer() {
  const wasOpen = Boolean(state.detailDrawer);
  state.detailDrawer = null;
  renderEventDetailDrawer();
  if (wasOpen && eventDrawerReturnFocus?.focus) eventDrawerReturnFocus.focus();
  eventDrawerReturnFocus = null;
}

function fillSummaryForm() {
  const settings = state.entitlement?.summary_settings || {};
  renderSummaryTargetOptions(summaryTargetsFromSettings(settings));
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

function summaryTargetKey(target) {
  return target?.chat_type === "private" ? "private" : `group:${target?.chat_id || ""}`;
}

function selectedSummaryTargets() {
  return [...document.querySelectorAll("[data-summary-target-card]")].map((card) => ({
    chat_type: card.dataset.chatType,
    chat_id: card.dataset.chatType === "private" ? state.deboxUserId : card.dataset.chatId,
    enabled: card.querySelector(".summary-target-enabled").checked,
    push_time: card.querySelector(".summary-target-time").value || "20:00",
    timezone: normalizeSummaryTimezone(card.querySelector(".summary-target-timezone").value),
    language: card.querySelector(".summary-target-language").value === "en" ? "en" : "zh",
    label: card.querySelector(".summary-target-label").value.trim(),
  }));
}

function summaryTargetSchedule(target = {}, settings = {}, configured = false) {
  const hasTargetEnabled = target.enabled !== undefined && target.enabled !== null;
  return {
    enabled: configured
      ? (hasTargetEnabled ? target.enabled === true || Number(target.enabled) === 1 : Boolean(settings.enabled))
      : false,
    push_time: target.push_time || settings.time || "20:00",
    timezone: normalizeSummaryTimezone(target.timezone || settings.timezone),
    language: (target.language || settings.language) === "en" ? "en" : "zh",
    label: target.label || (configured ? settings.label || "" : ""),
  };
}

function summaryTimezoneOptions(selectedTimezone) {
  const selected = normalizeSummaryTimezone(selectedTimezone);
  return SUMMARY_TIMEZONE_OPTIONS.map(
    ([timezone, label]) =>
      `<option value="${escapeHtml(timezone)}" ${timezone === selected ? "selected" : ""}>${escapeHtml(t(label))}</option>`
  ).join("");
}

function renderSummaryTargetOptions(draftTargets = null) {
  const plan = currentPlan();
  const available = Boolean(state.deboxUserId && plan?.daily_summary);
  const professional = plan?.code === "professional";
  const settings = state.entitlement?.summary_settings || {};
  const savedTargets = Array.isArray(draftTargets)
    ? draftTargets
    : summaryTargetsFromSettings(settings);
  const savedByKey = new Map(savedTargets.map((target) => [summaryTargetKey(target), target]));
  const candidates = [
    { chat_type: "private", chat_id: state.deboxUserId, name: t("privateSelf") },
  ];
  if (professional) {
    candidates.push(
      ...state.groups.map((group) => ({
        chat_type: "group",
        chat_id: group.gid,
        name: group.name || group.gid,
      }))
    );
  }

  const cards = candidates.map((candidate) => {
    const key = summaryTargetKey(candidate);
    const saved = savedByKey.get(key);
    const schedule = summaryTargetSchedule(saved, settings, Boolean(saved));
    return `
      <section
        class="summary-target-card"
        data-summary-target-card
        data-chat-type="${escapeHtml(candidate.chat_type)}"
        data-chat-id="${escapeHtml(candidate.chat_id)}"
      >
        <div class="summary-target-card-head">
          <div>
            <span class="summary-target-kind">${escapeHtml(candidate.chat_type === "private" ? t("privateTarget") : t("groupTarget"))}</span>
            <strong>${escapeHtml(candidate.name)}</strong>
          </div>
          <label class="switch-line summary-target-switch">
            <input class="summary-target-enabled" type="checkbox" ${schedule.enabled ? "checked" : ""} />
            ${escapeHtml(t("enableSummary"))}
          </label>
        </div>
        <div class="summary-target-fields">
          <label>
            ${escapeHtml(t("pushTime"))}
            <input class="summary-target-time" type="time" value="${escapeHtml(schedule.push_time)}" />
          </label>
          <label>
            ${escapeHtml(t("timezone"))}
            <select class="summary-target-timezone">${summaryTimezoneOptions(schedule.timezone)}</select>
          </label>
          <label>
            ${escapeHtml(t("summaryLanguage"))}
            <select class="summary-target-language">
              <option value="zh" ${schedule.language === "zh" ? "selected" : ""}>${escapeHtml(t("chinese"))}</option>
              <option value="en" ${schedule.language === "en" ? "selected" : ""}>English</option>
            </select>
          </label>
          <label>
            ${escapeHtml(t("note"))}
            <input
              class="summary-target-label"
              value="${escapeHtml(schedule.label)}"
              placeholder="${escapeHtml(t("summaryNotePlaceholder"))}"
              autocomplete="off"
            />
          </label>
        </div>
      </section>
    `;
  });
  if (professional && !state.groups.length) {
    cards.push(`<div class="notice muted">${escapeHtml(t("noBoundGroups"))}</div>`);
  }
  $("summaryTargetOptions").innerHTML = cards.join("");
  document.querySelectorAll(".summary-target-enabled").forEach((input) => {
    input.addEventListener("change", updateSummaryTargetCards);
  });
  updateSummaryTargetCards();
}

function renderSummaryStatus(settings = state.entitlement?.summary_settings || {}) {
  const plan = currentPlan();
  const available = Boolean(state.deboxUserId && plan?.daily_summary);
  const targets = summaryTargetsFromSettings(settings);
  const groupNames = new Map(state.groups.map((group) => [group.gid, group.name || group.gid]));
  const schedules = targets.map((target) => ({
    ...target,
    ...summaryTargetSchedule(target, settings, true),
    name: target.chat_type === "private"
      ? t("privateSelf")
      : groupNames.get(target.chat_id) || target.chat_id,
  }));
  const enabledCount = schedules.filter((target) => target.enabled).length;
  $("summaryStatusState").textContent = available
    ? t("summaryEnabledCount", { enabled: enabledCount, total: schedules.length })
    : "--";
  $("summaryStatusList").innerHTML = schedules.length
    ? schedules.map((target) => `
        <div class="summary-schedule-status-card ${target.enabled ? "" : "muted"}">
          <div>
            <strong>${escapeHtml(target.name)}</strong>
            <span>${escapeHtml(target.chat_type === "private" ? t("privateTarget") : t("groupTarget"))}</span>
          </div>
          <div>
            <strong>${escapeHtml(target.push_time)} · ${escapeHtml(target.timezone)}</strong>
            <span>${escapeHtml(target.language === "en" ? "English" : t("chinese"))}${target.label ? ` · ${escapeHtml(target.label)}` : ""}</span>
          </div>
          <span class="badge ${target.enabled ? "" : "muted"}">${escapeHtml(target.enabled ? t("summaryEnabledStatus") : t("summaryDisabledStatus"))}</span>
        </div>
      `).join("")
    : `<div class="notice muted">${escapeHtml(t("summaryNoTargets"))}</div>`;
  $("summaryEditBtn").disabled = !available;
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
  $("targetLabelInput").required = needsTarget;
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
  $("deliveryModeHint").hidden = !stage;
  $("deliveryModeHint").textContent = stage ? t("stageModeHint") : "";
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
  $("combinationTargetLabelInput").required = needsTarget;
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
  if (
    (rule.rule_type === "approval_change" || rule.rule_type === "address_interaction") &&
    !rule.target_label
  ) {
    throw new Error(t("targetNoteRequired"));
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
  let layout = "message";
  if (state.entitlement?.permanent) {
    status.textContent = t("permanentPlanActive");
  } else if (state.paymentError) {
    status.textContent = localizedApiError(state.paymentError);
  } else if (!config) {
    status.textContent = "";
    layout = "empty";
  } else if (config.mode !== "live") {
    status.textContent = t("previewMode");
  } else if (!config.ready) {
    status.textContent = t("paymentMissing", { items: config.missing.join(", ") });
  } else {
    layout = "asset";
    status.innerHTML = `
      <span class="payment-detail">
        <span class="payment-asset">
          <img class="asset-logo" src="/static/chains/bsc.png" alt="" aria-hidden="true">
          ${escapeHtml(config.chain_name)}
        </span>
      </span>
    `;
  }
  const actionLine = status.closest(".purchase-action-line");
  actionLine?.classList.toggle("payment-status-asset", layout === "asset");
  actionLine?.classList.toggle("payment-status-message", layout === "message");
  actionLine?.classList.toggle("payment-status-empty", layout === "empty");
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
    box.hidden = true;
    box.textContent = "";
    return;
  }
  box.hidden = false;
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
  $("summaryForm").querySelectorAll('button[type="submit"]').forEach((control) => {
    control.disabled = !available;
  });
  updateSummaryTargetCards();
}

function updateSummaryTargetCards() {
  const plan = currentPlan();
  const available = Boolean(state.deboxUserId && plan?.daily_summary);
  document.querySelectorAll("[data-summary-target-card]").forEach((card) => {
    const enabledInput = card.querySelector(".summary-target-enabled");
    enabledInput.disabled = !available;
    card.querySelectorAll(".summary-target-fields input, .summary-target-fields select").forEach((control) => {
      control.disabled = !available || !enabledInput.checked;
    });
    card.classList.toggle("disabled", !enabledInput.checked);
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
  await Promise.all([
    refreshAccount(),
    state.notificationDetailID ? loadNotificationDetail() : Promise.resolve(false),
  ]);
  renderNotificationDetailPage();
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
    await Promise.all([
      refreshAccount(),
      state.notificationDetailID ? loadNotificationDetail() : Promise.resolve(false),
    ]);
    renderNotificationDetailPage();
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
  if (!state.deboxUserId) return false;
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
  return true;
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
  if (state.entitlement?.permanent) {
    toast(t("permanentPlanActive"));
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
  if (isMobileShell()) {
    setMobileView("monitoring", { restoreScroll: false, target: $("activeRulesSection") });
  }
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
  if (isMobileShell()) {
    setMobileView("monitoring", { restoreScroll: false, target: $("activeRulesSection") });
  }
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
  await persistSummarySettings();
}

async function persistSummarySettings() {
  const targets = selectedSummaryTargets();
  if (!targets.length) {
    toast(t("summaryTargetRequired"));
    return false;
  }
  const legacyTarget = targets.find((target) => target.enabled) || targets[0];
  await api("/api/subscription/summary-settings", {
    method: "POST",
    body: JSON.stringify({
      enabled: targets.some((target) => target.enabled),
      push_time: legacyTarget.push_time,
      timezone: legacyTarget.timezone,
      targets,
      label: legacyTarget.label,
      language: legacyTarget.language,
    }),
  });
  await refreshAccount();
  toast(t("summarySaved"));
  return true;
}

function editSummary() {
  $("summaryForm").scrollIntoView({ behavior: "smooth", block: "center" });
  document.querySelector(".summary-target-enabled")?.focus({ preventScroll: true });
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

const MARKET_EVENT_ONLY_RULES = new Set([
  "market_new_pool",
  "market_four_meme_migration",
]);

const MARKET_REPEAT_WHILE_ACTIVE_RULES = new Set([
  "market_price_increase",
  "market_price_decrease",
  "market_liquidity_decrease",
  "market_volume_above",
  "market_volume_spike",
  "market_trade_imbalance",
]);

const MARKET_REPEAT_WHILE_ACTIVE_LABEL_KEYS = {
  market_price_increase: "repeatPriceIncreaseLabel",
  market_price_decrease: "repeatPriceDecreaseLabel",
  market_liquidity_decrease: "repeatLiquidityDecreaseLabel",
  market_volume_above: "repeatVolumeAboveLabel",
  market_volume_spike: "repeatVolumeSpikeLabel",
  market_trade_imbalance: "repeatTradeImbalanceLabel",
};

const MARKET_REPEAT_WHILE_ACTIVE_HELP_KEYS = {
  market_price_increase: "repeatPriceIncreaseHelp",
  market_price_decrease: "repeatPriceDecreaseHelp",
  market_liquidity_decrease: "repeatLiquidityDecreaseHelp",
  market_volume_above: "repeatVolumeAboveHelp",
  market_volume_spike: "repeatVolumeSpikeHelp",
  market_trade_imbalance: "repeatTradeImbalanceHelp",
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

const MARKET_THRESHOLD_HELP_KEYS = {
  market_price_above: "marketThresholdHelpPriceUsd",
  market_price_below: "marketThresholdHelpPriceUsd",
  market_price_increase: "marketThresholdHelpPriceChangePercent",
  market_price_decrease: "marketThresholdHelpPriceChangePercent",
  market_liquidity_below: "marketThresholdHelpLiquidityUsd",
  market_liquidity_decrease: "marketThresholdHelpLiquidityChangePercent",
  market_volume_above: "marketThresholdHelpVolumeUsd",
  market_volume_spike: "marketThresholdHelpVolumeRatio",
  market_trade_imbalance: "marketThresholdHelpImbalancePercent",
  market_large_buy: {
    usd: "marketThresholdHelpTradeUsd",
    token: "marketThresholdHelpTradeToken",
    percent: "marketThresholdHelpTradePercent",
  },
  market_large_sell: {
    usd: "marketThresholdHelpTradeUsd",
    token: "marketThresholdHelpTradeToken",
    percent: "marketThresholdHelpTradePercent",
  },
  market_consecutive_large_buy: {
    usd: "marketThresholdHelpTradeUsd",
    token: "marketThresholdHelpTradeToken",
    percent: "marketThresholdHelpTradePercent",
  },
  market_consecutive_large_sell: {
    usd: "marketThresholdHelpTradeUsd",
    token: "marketThresholdHelpTradeToken",
    percent: "marketThresholdHelpTradePercent",
  },
  market_liquidity_added: {
    usd: "marketThresholdHelpLiquidityEventUsd",
    percent: "marketThresholdHelpLiquidityEventPercent",
  },
  market_liquidity_removed: {
    usd: "marketThresholdHelpLiquidityEventUsd",
    percent: "marketThresholdHelpLiquidityEventPercent",
  },
  market_holder_increase: {
    usd: "marketThresholdHelpHolderUsd",
    token: "marketThresholdHelpHolderToken",
    percent: "marketThresholdHelpHolderPercent",
  },
  market_holder_decrease: {
    usd: "marketThresholdHelpHolderUsd",
    token: "marketThresholdHelpHolderToken",
    percent: "marketThresholdHelpHolderPercent",
  },
  market_holder_rank_entered: "marketThresholdHelpHolderRankEntered",
  market_holder_rank_exited: "marketThresholdHelpHolderRankExited",
  market_four_meme_large_trade: {
    usd: "marketThresholdHelpTradeUsd",
    token: "marketThresholdHelpTradeToken",
    percent: "marketThresholdHelpTradePercent",
  },
  market_four_meme_progress: "marketThresholdHelpFourMemeProgress",
};

const MARKET_THRESHOLD_UNIT_HELP_KEYS = {
  usd: "marketThresholdHelpGenericUsd",
  token: "marketThresholdHelpGenericToken",
  percent: "marketThresholdHelpGenericPercent",
  ratio: "marketThresholdHelpGenericRatio",
  count: "marketThresholdHelpGenericCount",
  progress: "marketThresholdHelpGenericProgress",
};

function marketThresholdHelpKey(ruleType, unit) {
  const configured = MARKET_THRESHOLD_HELP_KEYS[ruleType];
  if (typeof configured === "string") return configured;
  return configured?.[unit] || MARKET_THRESHOLD_UNIT_HELP_KEYS[unit] || "";
}

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

function marketRuleDisplayName(code) {
  return marketRuleName(marketRuleDefinition(code)) || t("marketUnknownRule");
}

const MARKET_PAUSE_REASON_KEYS = {
  project_archived: "marketPauseProjectArchived",
  subscription_expired: "marketPauseSubscriptionExpired",
  free_plan: "marketPauseFreePlan",
  user_archived: "marketPauseUserArchived",
};

function marketPauseReason(reason) {
  const normalized = String(reason || "").trim().toLowerCase();
  if (!normalized) return "";
  return t(MARKET_PAUSE_REASON_KEYS[normalized] || "marketPauseUnavailable");
}

function marketDeliveryModeLabel(mode) {
  const key = { realtime: "realtimeMode", stage: "stageMode" }[String(mode || "").toLowerCase()];
  return t(key || "marketDeliveryModeUnknown");
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
  return labels[type] || marketRuleDisplayName(type);
}

function marketStatusSuffix(status) {
  if (status === "active") return "Active";
  if (status === "archived") return "Archived";
  return "Paused";
}

function renderMarket() {
  renderMarketCapability();
  renderMarketWizard();
  renderMarketGoal();
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
  if ($("marketDetailGoalHint")) {
    $("marketDetailGoalHint").textContent = t(MARKET_GOAL_HINT_KEYS[state.marketGoal]);
  }
  renderMarketWizardRuleEditor();
  renderMarketRuleEditor();
}

function renderMarketWizard() {
  const wizard = state.marketWizard;
  document.querySelectorAll("[data-market-wizard-step]").forEach((button) => {
    const step = Number(button.dataset.marketWizardStep);
    button.classList.toggle("active", step === wizard.step);
    button.classList.toggle("complete", step < wizard.step);
    button.disabled = step > wizard.step;
  });
  for (let step = 1; step <= 4; step += 1) {
    $(`marketWizardStep${step}`).hidden = step !== wizard.step;
  }
  $("marketNameModeBtn").classList.toggle("active", wizard.mode === "name");
  $("marketNameModeBtn").setAttribute("aria-selected", String(wizard.mode === "name"));
  $("marketManualModeBtn").classList.toggle("active", wizard.mode === "manual");
  $("marketManualModeBtn").setAttribute("aria-selected", String(wizard.mode === "manual"));
  $("marketNameSearchPanel").hidden = wizard.mode !== "name";
  $("marketManualPanel").hidden = wizard.mode !== "manual";
  renderMarketAssetCandidates();
  renderMarketManualRows();
  renderMarketSelectedAsset();
  syncMarketPoolDiscoveryAccess();
  renderMarketWizardPools();
  renderMarketWizardRuleEditor();
  renderMarketWizardSummary();
  requestAnimationFrame(() => {
    keepMobileMarketStepVisible();
    scheduleMobileActionBarUpdate();
  });
}

function renderMarketAssetCandidates() {
  const result = state.marketWizard.searchResult;
  const list = $("marketAssetCandidates");
  if (!result) {
    list.innerHTML = "";
    return;
  }
  const candidates = result.candidates || [];
  if (!candidates.length) {
    list.innerHTML = `<div class="empty-state">${escapeHtml(t("marketNoAssetCandidates"))}</div>`;
    return;
  }
  list.innerHTML = candidates.map((candidate, index) => {
    const existingProject = existingMarketProjectForCandidate(candidate);
    const selected = state.marketWizard.selectedAsset?.canonical_asset_id ===
      candidate.canonical_asset_id;
    return `
      <button type="button" class="market-candidate-card${selected ? " selected" : ""}${existingProject ? " created" : ""}" data-market-candidate="${index}" ${existingProject ? "disabled" : ""}>
        <span class="market-token-logo">
          ${candidate.logo_url
            ? `<img src="${escapeHtml(candidate.logo_url)}" alt="" loading="lazy" />`
            : escapeHtml((candidate.symbol || "?").slice(0, 1))}
        </span>
        <span class="market-candidate-identity">
          <strong class="market-candidate-title">
            ${escapeHtml(candidate.name || candidate.symbol || "-")}
            <span class="market-candidate-symbol" data-symbol-tooltip="${escapeHtml(t("marketTokenSymbol"))}">${escapeHtml(candidate.symbol || "-")}</span>
          </strong>
          <span class="market-candidate-deployments">
            ${(candidate.deployments || []).map((deployment) => `
              <small><b>${escapeHtml(deployment.chain_name || marketChainName(deployment.chain_key))}</b> ${escapeHtml(deployment.contract_address)}</small>
            `).join("")}
          </span>
        </span>
        <span class="market-candidate-chains">${escapeHtml(existingProject
          ? t("marketAlreadyCreated")
          : t("marketChainsCount", { count: candidate.deployments?.length || 0 }))}</span>
      </button>
    `;
  }).join("");
  list.querySelectorAll("[data-market-candidate]").forEach((button) => {
    button.addEventListener("click", () => {
      if (button.dataset.marketSymbolLongPress === "true") {
        delete button.dataset.marketSymbolLongPress;
        return;
      }
      selectMarketCandidate(Number(button.dataset.marketCandidate));
    });
  });
}

function existingMarketProjectForCandidate(candidate) {
  if (!candidate) return null;
  const source = String(candidate.identity_source || "").toLowerCase();
  const canonicalID = String(candidate.canonical_asset_id || "").toLowerCase();
  const deployments = candidate.deployments || [];
  return state.marketProjects.find((project) => {
    if (source && canonicalID &&
        String(project.identity_source || "").toLowerCase() === source &&
        String(project.canonical_asset_id || "").toLowerCase() === canonicalID) {
      return true;
    }
    return deployments.some((deployment) =>
      String(project.chain_key || "").toLowerCase() ===
        String(deployment.chain_key || "").toLowerCase() &&
      String(project.token_address || "").toLowerCase() ===
        String(deployment.contract_address || "").toLowerCase()
    );
  }) || null;
}

function renderMarketManualRows() {
  const rows = state.marketWizard.manualRows;
  $("marketManualRows").innerHTML = rows.map((row, index) => {
    const usedByOtherRows = new Set(
      rows.filter((_, otherIndex) => otherIndex !== index).map((item) => item.chainKey),
    );
    const availableChains = state.chains.filter((chain) =>
      chain.key === row.chainKey || !usedByOtherRows.has(chain.key)
    );
    const selectedChain = availableChains.find((chain) => chain.key === row.chainKey);
    return `
      <div class="market-manual-row">
        <label>
          <span>${escapeHtml(t("chain"))}</span>
          <div class="chain-picker" data-market-manual-picker="${index}">
            <button class="chain-picker-button" type="button" aria-haspopup="listbox" aria-expanded="false" data-market-manual-chain-button="${index}">
              ${selectedChain ? chainOptionHtml(selectedChain) : escapeHtml(t("selectChain"))}
              <span class="chain-picker-arrow">&#8964;</span>
            </button>
            <div class="chain-picker-menu" role="listbox" data-market-manual-chain-menu="${index}" hidden>
              ${availableChains.map((chain) => `
                <button class="chain-picker-option${chain.key === row.chainKey ? " active" : ""}" type="button" role="option" aria-selected="${chain.key === row.chainKey}" data-market-manual-chain-option="${index}" data-chain="${escapeHtml(chain.key)}">
                  ${chainOptionHtml(chain)}
                </button>
              `).join("")}
            </div>
          </div>
        </label>
        <label>
          <span>${escapeHtml(t("marketTokenContract"))}</span>
          <input data-market-manual-contract="${index}" value="${escapeHtml(row.contractAddress)}" placeholder="0x..." autocomplete="off" />
        </label>
        <button type="button" class="secondary compact danger" data-remove-market-manual="${index}" ${rows.length === 1 ? "disabled" : ""}>${escapeHtml(t("remove"))}</button>
      </div>
    `;
  }).join("");
  $("addMarketManualRowBtn").disabled = rows.length >= state.chains.length;
  $("marketManualRows").querySelectorAll("[data-market-manual-chain-button]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      const menu = $("marketManualRows").querySelector(
        `[data-market-manual-chain-menu="${button.dataset.marketManualChainButton}"]`,
      );
      const willOpen = menu.hidden;
      closeAllChainPickers();
      menu.hidden = !willOpen;
      button.setAttribute("aria-expanded", String(willOpen));
      if (willOpen) menu.querySelector(".chain-picker-option.active")?.scrollIntoView({ block: "nearest" });
    });
  });
  $("marketManualRows").querySelectorAll("[data-market-manual-chain-option]").forEach((option) => {
    option.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      state.marketWizard.manualRows[Number(option.dataset.marketManualChainOption)].chainKey = option.dataset.chain;
      shieldTapThrough();
      renderMarketManualRows();
    });
  });
  $("marketManualRows").querySelectorAll("[data-market-manual-contract]").forEach((input) => {
    input.addEventListener("input", () => {
      state.marketWizard.manualRows[Number(input.dataset.marketManualContract)].contractAddress = input.value.trim();
    });
  });
  $("marketManualRows").querySelectorAll("[data-remove-market-manual]").forEach((button) => {
    button.addEventListener("click", () => {
      state.marketWizard.manualRows.splice(Number(button.dataset.removeMarketManual), 1);
      renderMarketManualRows();
    });
  });
}

function renderMarketSelectedAsset() {
  const asset = state.marketWizard.selectedAsset;
  const container = $("marketSelectedAsset");
  const deployments = $("marketWizardDeployments");
  if (!asset) {
    container.innerHTML = `<div class="empty-state">${escapeHtml(t("marketChooseAssetFirst"))}</div>`;
    deployments.innerHTML = "";
    return;
  }
  container.innerHTML = `
    <span class="market-token-logo large">
      ${asset.logo_url
        ? `<img src="${escapeHtml(asset.logo_url)}" alt="" />`
        : escapeHtml((asset.symbol || "?").slice(0, 1))}
    </span>
    <span>
      <strong>${escapeHtml(asset.name || asset.symbol || "-")} (${escapeHtml(asset.symbol || "-")})</strong>
    </span>
  `;
  deployments.innerHTML = (asset.deployments || []).map((deployment) => {
    const checked = state.marketWizard.selectedChains.has(deployment.chain_key);
    const logo = chainLogoSrc(deployment.chain_key);
    return `
      <label class="market-deployment-card${checked ? " selected" : ""}">
        <input type="checkbox" data-market-deployment-chain="${escapeHtml(deployment.chain_key)}" ${checked ? "checked" : ""} />
        ${logo ? `<img src="${escapeHtml(logo)}" alt="" />` : ""}
        <span>
          <strong>${escapeHtml(deployment.chain_name || marketChainName(deployment.chain_key))}</strong>
          <small>${escapeHtml(deployment.contract_address)}</small>
        </span>
        <b>${escapeHtml(checked ? t("selected") : t("notSelected"))}</b>
      </label>
    `;
  }).join("");
  deployments.querySelectorAll("[data-market-deployment-chain]").forEach((checkbox) => {
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) {
        state.marketWizard.selectedChains.add(checkbox.dataset.marketDeploymentChain);
      } else {
        state.marketWizard.selectedChains.delete(checkbox.dataset.marketDeploymentChain);
      }
      clearMarketWizardVerification();
      renderMarketSelectedAsset();
    });
  });
}

function renderMarketWizardPools() {
  const result = state.marketWizard.poolResult;
  const container = $("marketWizardPoolGroups");
  if (!result) {
    container.innerHTML = "";
    $("marketPoolsContinueBtn").disabled = true;
    return;
  }
  container.innerHTML = (result.groups || []).map((group) => {
    const selection = marketPoolSelection(group.chain_key);
    const fullPools = group.full_monitoring_pools || [];
    const quotePools = group.quote_only_pools || [];
    return `
      <article class="market-chain-pool-group">
        <div class="market-chain-pool-head">
          <span>
            ${chainLogoSrc(group.chain_key) ? `<img src="${escapeHtml(chainLogoSrc(group.chain_key))}" alt="" />` : ""}
            <strong>${escapeHtml(group.chain_name)}</strong>
          </span>
          <small>${escapeHtml(group.token?.symbol || "-")} · ${escapeHtml(shortAddress(group.token?.address || ""))}</small>
        </div>
        ${group.error ? `<div class="notice warning">${escapeHtml(group.error)}</div>` : ""}
        <div class="market-pool-section-label">${escapeHtml(t("marketFullMonitoring"))}</div>
        <div class="market-pool-list">
          ${fullPools.map((preview) => renderWizardPoolCard(group, preview, selection, false)).join("") ||
            `<div class="empty-state">${escapeHtml(t("marketNoFullPools"))}</div>`}
        </div>
        ${quotePools.length ? `
          <details class="market-quotes-only">
            <summary>${escapeHtml(t("marketQuotesOnlyCount", { count: quotePools.length }))}</summary>
            <div class="market-pool-list">
              ${quotePools.map((preview) => renderWizardPoolCard(group, preview, selection, true)).join("")}
            </div>
          </details>
        ` : ""}
      </article>
    `;
  }).join("");
  container.querySelectorAll("[data-market-wizard-pool]").forEach((checkbox) => {
    checkbox.addEventListener("change", () => updateWizardPoolSelection(checkbox));
  });
  container.querySelectorAll("[data-market-wizard-primary]").forEach((radio) => {
    radio.addEventListener("change", () => {
      const selection = marketPoolSelection(radio.dataset.marketWizardPrimary);
      if ((currentPlan()?.code || "free") === "standard") {
        selection.selected.clear();
      }
      selection.primary = radio.value;
      selection.selected.add(radio.value);
      renderMarketWizardPools();
    });
  });
  $("marketPoolsContinueBtn").disabled = state.marketWizard.busy || !marketWizardPoolsReady();
}

function renderWizardPoolCard(group, preview, selection, quoteOnly) {
  const pair = preview.pair || {};
  const address = pair.pairAddress || "";
  const selected = selection.selected.has(address);
  const primary = selection.primary === address;
  return `
    <div class="market-pool-card market-wizard-pool-card${quoteOnly ? " unsupported" : " has-primary-choice"}">
      <input type="checkbox" data-market-wizard-pool="${escapeHtml(address)}" data-chain="${escapeHtml(group.chain_key)}" ${selected ? "checked" : ""} ${quoteOnly ? "disabled" : ""} />
      <div class="market-wizard-pool-info">
        <strong>${escapeHtml(preview.protocol || pair.dexId || "-")} ${escapeHtml(preview.protocol_version || "")}</strong>
        <span>${escapeHtml(pair.baseToken?.symbol || "-")} / ${escapeHtml(pair.quoteToken?.symbol || "-")}</span>
        <small>${escapeHtml(shortAddress(address))}</small>
      </div>
      <div class="market-pool-values">
        <span class="market-pool-value-row">
          <small>${escapeHtml(t("marketMetricLiquidity"))}</small>
          <strong>${marketMoney(pair.liquidity?.usd)}</strong>
        </span>
        <span class="market-pool-value-row">
          <small>${escapeHtml(t("marketTokenPrice"))}</small>
          <strong>${marketMoney(pair.priceUsd)}</strong>
        </span>
      </div>
      ${quoteOnly ? "" : `
        <label class="market-primary-choice${primary ? " selected" : ""}">
          <input type="radio" name="market-primary-${escapeHtml(group.chain_key)}" data-market-wizard-primary="${escapeHtml(group.chain_key)}" value="${escapeHtml(address)}" ${primary ? "checked" : ""} />
          ${escapeHtml(t(primary ? "marketPoolPrimary" : "marketSetPrimary"))}
        </label>
      `}
    </div>
  `;
}

function initPersistentHorizontalScrollbars() {
  document.querySelectorAll("[data-persistent-horizontal-scroll]").forEach((scroller) => {
    const scrollbar = scroller.parentElement?.querySelector(".persistent-horizontal-scrollbar");
    const thumb = scrollbar?.querySelector("span");
    if (!scrollbar || !thumb) {
      return;
    }

    const update = () => {
      const viewportWidth = scroller.clientWidth;
      const contentWidth = scroller.scrollWidth;
      const trackWidth = scrollbar.clientWidth;
      if (!viewportWidth || !contentWidth || !trackWidth) {
        return;
      }
      const thumbWidth = Math.max(28, trackWidth * Math.min(1, viewportWidth / contentWidth));
      const maxScroll = Math.max(1, contentWidth - viewportWidth);
      const maxOffset = Math.max(0, trackWidth - thumbWidth);
      thumb.style.width = `${thumbWidth}px`;
      thumb.style.transform = `translateX(${maxOffset * (scroller.scrollLeft / maxScroll)}px)`;
    };

    scroller.addEventListener("scroll", update, { passive: true });
    if (typeof ResizeObserver === "function") {
      const observer = new ResizeObserver(update);
      observer.observe(scroller);
      Array.from(scroller.children).forEach((child) => observer.observe(child));
    }
    requestAnimationFrame(update);
  });
}

function marketPoolSelection(chainKey) {
  if (!state.marketWizard.poolSelections[chainKey]) {
    state.marketWizard.poolSelections[chainKey] = {
      selected: new Set(),
      primary: "",
    };
  }
  return state.marketWizard.poolSelections[chainKey];
}

function marketWizardPoolsReady() {
  const groups = state.marketWizard.poolResult?.groups || [];
  return groups.length > 0 && groups.every((group) => {
    const selection = marketPoolSelection(group.chain_key);
    return !group.error && selection.selected.size > 0 &&
      Boolean(selection.primary) && selection.selected.has(selection.primary);
  });
}

function updateWizardPoolSelection(checkbox) {
  const selection = marketPoolSelection(checkbox.dataset.chain);
  const planCode = currentPlan()?.code || "free";
  if (checkbox.checked && planCode === "standard") {
    selection.selected.clear();
  }
  if (checkbox.checked) {
    selection.selected.add(checkbox.dataset.marketWizardPool);
    if (!selection.primary || planCode === "standard") {
      selection.primary = checkbox.dataset.marketWizardPool;
    }
  } else {
    selection.selected.delete(checkbox.dataset.marketWizardPool);
    if (selection.primary === checkbox.dataset.marketWizardPool) {
      selection.primary = [...selection.selected][0] || "";
    }
  }
  renderMarketWizardPools();
}

function renderMarketWizardRuleEditor() {
  const select = $("marketWizardRuleTypeSelect");
  if (!select || !state.marketCatalog) return;
  const poolScopeSelect = $("marketWizardPoolScopeSelect");
  const previousPoolScope = poolScopeSelect.value;
  const multiPoolAllowed = currentPlan()?.market_pool_mode === "multiple";
  poolScopeSelect.innerHTML = `
    <option value="primary">${escapeHtml(t("marketPrimaryPoolsOnly"))}</option>
    <option value="all" ${multiPoolAllowed ? "" : "disabled"}>${escapeHtml(t("marketAllChosenPools"))} · ${escapeHtml(t("professional"))}</option>
  `;
  poolScopeSelect.value = multiPoolAllowed && previousPoolScope === "all"
    ? "all"
    : "primary";
  $("marketWizardPoolScopeDescription").textContent = multiPoolAllowed
    ? ""
    : t("marketMultiPoolProfessionalOnly");
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
    select.value = definitions.find((rule) => rule.allowed)?.code || "";
  }
  const definition = marketRuleDefinition(select.value);
  $("marketWizardPoolScopeWrap").hidden = definition?.code === "market_new_pool";
  $("marketWizardRuleDescription").textContent = definition
    ? marketRuleDescription(definition)
    : t("marketProfessionalOnly");
  const units = definition?.units || ["usd"];
  const oldUnit = $("marketWizardThresholdUnitSelect").value;
  $("marketWizardThresholdUnitSelect").innerHTML = units.map((unit) => `
    <option value="${escapeHtml(unit)}">${escapeHtml(t(MARKET_UNIT_KEYS[unit] || unit))}</option>
  `).join("");
  $("marketWizardThresholdUnitSelect").value = units.includes(oldUnit)
    ? oldUnit
    : (definition?.default_unit || units[0]);
  applyMarketRecommendationEditor("wizard", definition, true);
  const groupAllowed = currentPlan()?.code === "professional" && state.groups.length > 0;
  const groupOption = [...$("marketWizardTargetTypeSelect").options]
    .find((option) => option.value === "group");
  if (groupOption) groupOption.disabled = !groupAllowed;
  if (!groupAllowed && $("marketWizardTargetTypeSelect").value === "group") {
    $("marketWizardTargetTypeSelect").value = "private";
  }
  $("marketWizardGroupSelect").innerHTML = state.groups.map((group) => `
    <option value="${escapeHtml(group.gid)}">${escapeHtml(group.name || group.gid)}</option>
  `).join("");
  $("marketWizardGroupWrap").hidden = $("marketWizardTargetTypeSelect").value !== "group";
  const createButton = $("createMarketProjectBtn");
  createButton.disabled = currentPlan()?.code === "free" || !definition?.allowed ||
    state.marketWizard.busy;
  createButton.title = currentPlan()?.code === "free" ? t("marketFreeQueryOnly") : "";
}

function renderMarketWizardSummary() {
  const summary = $("marketWizardSummary");
  if (!summary) return;
  const asset = state.marketWizard.selectedAsset;
  const groups = state.marketWizard.poolResult?.groups || [];
  const selectedPools = groups.reduce(
    (total, group) => total + marketPoolSelection(group.chain_key).selected.size,
    0,
  );
  const definition = marketRuleDefinition($("marketWizardRuleTypeSelect")?.value);
  summary.innerHTML = `
    <h4>${escapeHtml(t("marketCreationSummary"))}</h4>
    <dl>
      <div><dt>${escapeHtml(t("marketProjectToken"))}</dt><dd>${escapeHtml(asset ? `${asset.name} (${asset.symbol})` : "-")}</dd></div>
      <div><dt>${escapeHtml(t("marketSelectedChains"))}</dt><dd>${escapeHtml(String(state.marketWizard.selectedChains.size))}</dd></div>
      <div><dt>${escapeHtml(t("marketSelectedPools"))}</dt><dd>${escapeHtml(String(selectedPools))}</dd></div>
      <div><dt>${escapeHtml(t("firstRule"))}</dt><dd>${escapeHtml(marketRuleName(definition) || "-")}</dd></div>
      <div><dt>${escapeHtml(t("notificationMethod"))}</dt><dd>${escapeHtml(t($("marketWizardTargetTypeSelect")?.value === "group" ? "groupNotification" : "privateNotification"))}</dd></div>
      <div><dt>${escapeHtml(t("currentPlan"))}</dt><dd>${escapeHtml(localizedPlan(currentPlan()).name)}</dd></div>
    </dl>
  `;
}

function marketChainName(chainKey) {
  return state.chains.find((chain) => chain.key === chainKey)?.name || chainKey;
}

function marketProjectIsExpiredFrozen(project = state.marketDetail?.project) {
  return project?.status === "paused"
    && project?.pause_reason === "subscription_expired"
    && Boolean(project?.frozen_at);
}

function marketProjectStatusLabel(project) {
  return t(marketProjectIsExpiredFrozen(project)
    ? "marketProjectStatusExpiredFrozen"
    : `marketProjectStatus${marketStatusSuffix(project?.status)}`);
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
    <button class="market-project-card${project.status === "archived" ? " archived" : ""}${marketProjectIsExpiredFrozen(project) ? " expired-frozen" : ""}" type="button" data-market-project="${project.id}">
      <div>
        <strong>${escapeHtml(project.token_name || project.token_symbol || "-")} (${escapeHtml(project.token_symbol || "-")})</strong>
        <span>${escapeHtml(shortAddress(project.token_address))}</span>
      </div>
      <div>
        <span class="badge">${escapeHtml(marketProjectStatusLabel(project))}</span>
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
  const frozen = marketProjectIsExpiredFrozen(project);
  wrap.classList.toggle("expired-frozen", frozen);
  const asset = detail.asset || {};
  const deployments = marketDetailDeployments();
  const chainNames = deployments.map((deployment) => marketChainName(deployment.chain_key));
  const chainLogos = deployments.map((deployment) => {
    const chainName = marketChainName(deployment.chain_key);
    const chainLogo = chainLogoSrc(deployment.chain_key);
    return chainLogo
      ? `<img src="${escapeHtml(chainLogo)}" alt="${escapeHtml(chainName)}" title="${escapeHtml(chainName)}" />`
      : "";
  }).filter(Boolean).join("");
  const logo = asset.logo_url || "";
  $("marketProjectHeader").innerHTML = `
    <div class="market-project-identity">
      ${logo ? `<img src="${escapeHtml(logo)}" alt="" />` : `<span class="market-token-fallback">${escapeHtml((asset.symbol || project.token_symbol || "?").slice(0, 1))}</span>`}
      <div>
        <p class="market-project-chain-logos" aria-label="${escapeHtml(chainNames.join(", ") || marketChainName(project.chain_key))}">
          ${chainLogos}
        </p>
        <h3>${escapeHtml(asset.canonical_name || project.token_name || project.token_symbol)} (${escapeHtml(asset.symbol || project.token_symbol)})</h3>
        <span>${escapeHtml(t("marketProjectChainsAndPools", {
          chains: deployments.length || 1,
          pools: (detail.pools || []).filter((pool) => Number(pool.selected) === 1).length,
        }))}</span>
      </div>
    </div>
    <span class="badge">${escapeHtml(marketProjectStatusLabel(project))}</span>
  `;
  $("archiveMarketProjectBtn").hidden = frozen;
  $("archiveMarketProjectBtn").textContent = t(
    project.status === "archived" ? "restoreMarketProject" : "archiveMarketProject",
  );
  $("archiveMarketProjectBtn").classList.toggle("danger", project.status !== "archived");
  $("deleteMarketProjectBtn").hidden = project.status !== "archived" && !frozen;
  const snapshots = detail.snapshots?.length
    ? detail.snapshots
    : (detail.latest_snapshot ? [detail.latest_snapshot] : []);
  const snapshot = snapshots[0] || {};
  const totalLiquidity = snapshots.reduce((total, item) => total + Number(item.liquidity_usd || 0), 0);
  const totalVolume = snapshots.reduce((total, item) => total + Number(item.volume_24h_usd || 0), 0);
  const activeRules = (detail.rules || []).filter(marketRuleIsActive);
  const activeCombinations = (detail.combinations || []).filter(marketCombinationIsActive);
  const metrics = [
    [t("marketMetricPrice"), marketMoney(snapshot.price_usd)],
    [t("marketMetricLiquidity"), snapshots.length ? marketMoney(totalLiquidity) : "-"],
    [t("marketMetricVolume24h"), snapshots.length ? marketMoney(totalVolume) : "-"],
    [t("marketMonitoredChains"), String(deployments.length || 1)],
    [t("marketMonitoredPools"), String((detail.pools || []).filter((pool) => Number(pool.selected) === 1).length)],
    [t("marketRunningRulesCount"), String(activeRules.length + activeCombinations.length)],
  ];
  $("marketMetricGrid").innerHTML = metrics.map(([label, value]) => `
    <div><span>${escapeHtml(label)}</span><strong>${escapeHtml(String(value))}</strong></div>
  `).join("");
  const chainKeys = new Set(deployments.map((deployment) => deployment.chain_key));
  const providerHealth = (detail.provider_health || []).filter(
    (item) => !chainKeys.size || !item.chain_key || chainKeys.has(item.chain_key),
  );
  const degraded = providerHealth.some((item) => item.status !== "healthy");
  const providerStatusKey = frozen
    ? "marketProviderExpiredFrozen"
    : providerHealth.length
    ? (degraded ? "marketProviderDegraded" : "marketProviderHealthy")
    : "marketProviderPending";
  $("marketProviderStatus").textContent = t(providerStatusKey);
  $("marketProviderStatus").classList.toggle("warning", degraded);
  renderMarketDetailTabs();
  renderMarketOverview();
  renderMarketProjectPools();
  renderMarketRuleEditor();
  renderMarketRules();
  renderMarketCombinations();
  renderMarketHolders();
  renderMarketEventFilters();
  renderMarketEvents();
}

function marketDetailDeployments() {
  const detail = state.marketDetail;
  if (detail?.deployments?.length) return detail.deployments;
  if (!detail?.project) return [];
  return [{
    id: 0,
    chain_key: detail.project.chain_key,
    chain_id: detail.project.chain_id,
    token_address: detail.project.token_address,
    token_name: detail.project.token_name,
    token_symbol: detail.project.token_symbol,
    status: detail.project.status,
  }];
}

function marketRuleIsActive(rule) {
  return Number(rule?.enabled) === 1 && rule?.run_status === "active";
}

function marketCombinationIsActive(rule) {
  return Number(rule?.enabled) === 1 && rule?.run_status === "active";
}

function setMarketDetailTab(tab) {
  const allowed = new Set(["overview", "rules", "pools", "holders", "events"]);
  state.marketDetailTab = allowed.has(tab) ? tab : "overview";
  renderMarketDetailTabs();
}

function renderMarketDetailTabs() {
  document.querySelectorAll("[data-market-detail-tab]").forEach((button) => {
    const active = button.dataset.marketDetailTab === state.marketDetailTab;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", String(active));
  });
  document.querySelectorAll("[data-market-detail-panel]").forEach((panel) => {
    panel.hidden = panel.dataset.marketDetailPanel !== state.marketDetailTab;
  });
}

function chainExplorerAddress(chainKey, address) {
  const roots = {
    bsc: "https://bscscan.com/address/",
    ethereum: "https://etherscan.io/address/",
    base: "https://basescan.org/address/",
    polygon: "https://polygonscan.com/address/",
    arbitrum: "https://arbiscan.io/address/",
    optimism: "https://optimistic.etherscan.io/address/",
  };
  return `${roots[chainKey] || ""}${address}`;
}

function chainExplorerTransaction(chainKey, transactionHash) {
  const roots = {
    bsc: "https://bscscan.com/tx/",
    ethereum: "https://etherscan.io/tx/",
    base: "https://basescan.org/tx/",
    polygon: "https://polygonscan.com/tx/",
    arbitrum: "https://arbiscan.io/tx/",
    optimism: "https://optimistic.etherscan.io/tx/",
  };
  const hash = String(transactionHash || "");
  return roots[chainKey] && /^0x[a-fA-F0-9]{64}$/.test(hash) ? `${roots[chainKey]}${hash}` : "";
}

function renderMarketOverview() {
  const detail = state.marketDetail;
  if (!detail) return;
  const frozen = marketProjectIsExpiredFrozen(detail.project);
  const deployments = marketDetailDeployments();
  $("marketOverviewContracts").innerHTML = deployments.map((deployment) => `
    <div class="market-contract-row">
      <span class="market-chain-identity">
        ${chainLogoSrc(deployment.chain_key) ? `<img src="${escapeHtml(chainLogoSrc(deployment.chain_key))}" alt="" />` : ""}
        <span><strong>${escapeHtml(marketChainName(deployment.chain_key))}</strong><small>${escapeHtml(t(`marketProjectStatus${marketStatusSuffix(deployment.status || "active")}`))}</small></span>
      </span>
      <code title="${escapeHtml(deployment.token_address)}">${escapeHtml(deployment.token_address)}</code>
      <span class="market-inline-actions"${frozen ? " hidden" : ""}>
        <button class="secondary compact" type="button" data-copy-market-address="${escapeHtml(deployment.token_address)}">${escapeHtml(t("copy"))}</button>
        <a class="secondary compact button-link" href="${escapeHtml(chainExplorerAddress(deployment.chain_key, deployment.token_address))}" target="_blank" rel="noopener">${escapeHtml(t("viewOnExplorer"))}</a>
      </span>
    </div>
  `).join("");
  $("marketOverviewContracts").querySelectorAll("[data-copy-market-address]").forEach((button) => {
    button.addEventListener("click", guardAsync(async () => {
      await copyText(button.dataset.copyMarketAddress);
      toast(t("copied"));
    }));
  });
  $("openMarketRulesTabBtn").hidden = frozen;

  const snapshots = new Map((detail.snapshots || []).map((snapshot) => [snapshot.chain_key, snapshot]));
  $("marketOverviewChains").innerHTML = deployments.map((deployment) => {
    const snapshot = snapshots.get(deployment.chain_key);
    return `
      <div class="market-chain-summary">
        <div class="market-chain-pool-head">
          <span>
            ${chainLogoSrc(deployment.chain_key) ? `<img src="${escapeHtml(chainLogoSrc(deployment.chain_key))}" alt="" />` : ""}
            <strong>${escapeHtml(marketChainName(deployment.chain_key))}</strong>
          </span>
          <small>${escapeHtml(snapshot ? marketDate(snapshot.captured_at) : t("marketWaitingForData"))}</small>
        </div>
        <dl>
          <div><dt>${escapeHtml(t("marketMetricPrice"))}</dt><dd>${marketMoney(snapshot?.price_usd)}</dd></div>
          <div><dt>${escapeHtml(t("marketMetricLiquidity"))}</dt><dd>${marketMoney(snapshot?.liquidity_usd)}</dd></div>
          <div><dt>${escapeHtml(t("marketMetricVolume24h"))}</dt><dd>${marketMoney(snapshot?.volume_24h_usd)}</dd></div>
        </dl>
      </div>
    `;
  }).join("");

  const activeRules = (detail.rules || []).filter(marketRuleIsActive);
  const activeCombinations = (detail.combinations || []).filter(marketCombinationIsActive);
  const items = [
    ...activeRules.map((rule) => ({
      name: marketRuleDisplayName(rule.rule_type),
      note: rule.last_triggered_at
        ? t("marketLastTriggeredAt", { time: marketDate(rule.last_triggered_at) })
        : t("marketNotTriggeredYet"),
      kind: t("marketSingleRule"),
    })),
    ...activeCombinations.map((combination) => ({
      name: combination.note || t("marketCombinationRule"),
      note: t("marketCombinationMembersCount", { count: combination.members?.length || 0 }),
      kind: t("marketCombinationRule"),
    })),
  ];
  $("marketOverviewRules").innerHTML = items.length ? items.map((item) => `
    <div class="market-overview-rule">
      <div><strong>${escapeHtml(item.name)}</strong><small>${escapeHtml(item.note)}</small></div>
      <span class="badge">${escapeHtml(item.kind)}</span>
    </div>
  `).join("") : `<div class="empty-state">${escapeHtml(t("marketNoRunningRules"))}</div>`;
}

function renderMarketProjectPools() {
  const detail = state.marketDetail;
  if (!detail) return;
  const editable = detail.project?.status === "active";
  const groups = new Map();
  (detail.pools || []).forEach((pool) => {
    if (!groups.has(pool.chain_key)) groups.set(pool.chain_key, []);
    groups.get(pool.chain_key).push(pool);
  });
  $("marketProjectPools").innerHTML = [...groups.entries()].map(([chainKey, pools]) => {
    const expanded = state.marketExpandedPoolChains.has(chainKey);
    const visiblePools = expanded ? pools : pools.slice(0, 1);
    return `
    <section class="market-managed-chain">
      <div class="market-chain-pool-head">
        <span>
          ${chainLogoSrc(chainKey) ? `<img src="${escapeHtml(chainLogoSrc(chainKey))}" alt="" />` : ""}
          <strong>${escapeHtml(marketChainName(chainKey))}</strong>
        </span>
        <small>${escapeHtml(t("marketManagedPoolCount", { count: pools.length }))}</small>
      </div>
      <div class="market-pool-list">
        ${visiblePools.map((pool) => {
    const selected = Number(pool.selected) === 1;
    const primary = Number(pool.is_primary) === 1;
    const supported = Number(pool.supports_event_parsing) === 1;
    const statusKey = !supported
      ? "marketPoolQuotesOnlyStatus"
      : selected
        ? "marketPoolMonitoringStatus"
        : "marketPoolAvailableStatus";
    return `
      <div class="market-pool-card${supported ? "" : " unsupported"}${selected ? " selected" : ""}">
        <div>
          <strong>${escapeHtml(pool.protocol)} ${escapeHtml(pool.protocol_version)}</strong>
          <span>${escapeHtml(pool.token0_symbol)} / ${escapeHtml(pool.token1_symbol)}</span>
          <small>${escapeHtml(shortAddress(pool.pool_address || pool.pool_key))}</small>
        </div>
        <div class="market-pool-values">
          <strong>${marketMoney(pool.liquidity_usd)}</strong>
          <span class="market-pool-status ${supported ? (selected ? "monitoring" : "available") : "quotes"}">${escapeHtml(t(statusKey))}</span>
          ${primary ? `<small>${escapeHtml(t("marketDefaultQuotePool"))}</small>` : ""}
        </div>
        ${supported && editable ? `
          <div class="market-pool-actions">
            ${!primary ? `<button type="button" class="secondary compact" data-market-toggle-pool="${pool.id}" data-selected="${selected}">${escapeHtml(t(selected ? "marketStopMonitoringPool" : "marketAddMonitoringPool"))}</button>` : ""}
            ${!primary ? `<button type="button" class="secondary compact" data-market-primary="${pool.id}">${escapeHtml(t("marketSetDefaultQuotePool"))}</button>` : ""}
          </div>
        ` : ""}
        ${!supported ? `<p class="market-pool-explanation">${escapeHtml(t("marketPoolQuotesExplanation"))}</p>` : ""}
      </div>
    `;
        }).join("")}
        ${pools.length > 1 ? `
          <button type="button" class="market-list-toggle" data-market-pool-list-toggle="${escapeHtml(chainKey)}" aria-expanded="${expanded}">
            <span>${escapeHtml(expanded ? t("collapseList") : t("expandRemainingItems", { count: pools.length - 1 }))}</span>
            <span class="market-list-toggle-chevron" aria-hidden="true">${expanded ? "⌃" : "⌄"}</span>
          </button>
        ` : ""}
      </div>
    </section>
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
  $("marketProjectPools").querySelectorAll("[data-market-pool-list-toggle]").forEach((button) => {
    button.addEventListener("click", () => {
      const chainKey = button.dataset.marketPoolListToggle;
      if (state.marketExpandedPoolChains.has(chainKey)) {
        state.marketExpandedPoolChains.delete(chainKey);
      } else {
        state.marketExpandedPoolChains.add(chainKey);
      }
      renderMarketProjectPools();
    });
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
  $("marketRulePoolWrap").hidden = definition?.code === "market_new_pool";
  $("marketRuleDescription").textContent = definition
    ? marketRuleDescription(definition)
    : t("marketProfessionalOnly");
  const units = definition?.units || ["usd"];
  const oldUnit = $("marketThresholdUnitSelect").value;
  $("marketThresholdUnitSelect").innerHTML = units.map((unit) => `
    <option value="${escapeHtml(unit)}">${escapeHtml(t(MARKET_UNIT_KEYS[unit] || unit))}</option>
  `).join("");
  $("marketThresholdUnitSelect").value = units.includes(oldUnit) ? oldUnit : (definition?.default_unit || units[0]);
  const pools = (state.marketDetail?.pools || []).filter((pool) => Number(pool.selected) === 1);
  $("marketRulePoolSelect").innerHTML = `
    <option value="">${escapeHtml(t("marketAllSelectedPools"))}</option>
    ${pools.map((pool) => `<option value="${pool.id}">${escapeHtml(marketChainName(pool.chain_key))} · ${escapeHtml(pool.protocol)} ${escapeHtml(pool.protocol_version)} · ${escapeHtml(pool.token0_symbol)}/${escapeHtml(pool.token1_symbol)}</option>`).join("")}
  `;
  const editable = state.marketDetail?.project?.status === "active";
  const frozen = marketProjectIsExpiredFrozen();
  document.querySelector(".market-detail-goals").hidden = frozen;
  $("marketDetailGoalHint").hidden = frozen;
  document.querySelector(".market-rule-section-head").hidden = frozen;
  $("marketRuleForm").hidden = frozen;
  document.querySelector("#marketCombinationRulePanel > .panel-intro").hidden = frozen;
  $("marketCombinationEntitlementNotice").hidden = frozen
    ? true
    : currentPlan()?.code === "professional";
  $("marketCombinationForm").hidden = frozen;
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
  applyMarketRecommendationEditor("detail", definition, editable && Boolean(definition?.allowed));
  updateMarketDeliveryFields();
  renderMarketGroupOptions();
  renderMarketRuleMode();
  renderMarketCombinationEditor();
}

function setMarketRuleMode(mode) {
  state.marketRuleMode = mode === "combination" ? "combination" : "single";
  renderMarketRuleMode();
}

function renderMarketRuleMode() {
  const combination = state.marketRuleMode === "combination";
  $("marketSingleRulePanel").hidden = combination;
  $("marketCombinationRulePanel").hidden = !combination;
  $("marketSingleRuleModeBtn").classList.toggle("active", !combination);
  $("marketCombinationRuleModeBtn").classList.toggle("active", combination);
  $("marketSingleRuleModeBtn").setAttribute("aria-selected", String(!combination));
  $("marketCombinationRuleModeBtn").setAttribute("aria-selected", String(combination));
}

function renderMarketCombinationEditor() {
  const rules = (state.marketDetail?.rules || []).filter((rule) => Number(rule.enabled) === 1);
  const professional = currentPlan()?.code === "professional";
  $("marketCombinationEntitlementNotice").hidden = professional || marketProjectIsExpiredFrozen();
  $("marketCombinationMemberOptions").innerHTML = rules.length ? rules.map((rule) => `
    <label class="market-combination-member-option">
      <input type="checkbox" value="${rule.id}" data-market-combination-member />
      <span>
        <strong>${escapeHtml(marketRuleDisplayName(rule.rule_type))}</strong>
        <small>${escapeHtml(rule.threshold_value)} ${escapeHtml(t(MARKET_UNIT_KEYS[rule.threshold_unit] || rule.threshold_unit))}</small>
      </span>
      <span class="market-trigger-count">
        <span>${escapeHtml(t("triggerCount"))}</span>
        <input type="number" min="1" step="1" value="1" data-market-combination-count="${rule.id}" />
      </span>
    </label>
  `).join("") : `<div class="empty-state">${escapeHtml(t("marketCombinationNeedsRules"))}</div>`;
  $("marketCombinationGroupSelect").innerHTML = state.groups.map((group) => `
    <option value="${escapeHtml(group.gid)}">${escapeHtml(group.name || group.gid)}</option>
  `).join("");
  const groupAllowed = professional && state.groups.length > 0;
  const groupOption = [...$("marketCombinationTargetTypeSelect").options]
    .find((option) => option.value === "group");
  if (groupOption) groupOption.disabled = !groupAllowed;
  if (!groupAllowed && $("marketCombinationTargetTypeSelect").value === "group") {
    $("marketCombinationTargetTypeSelect").value = "private";
  }
  $("marketCombinationGroupWrap").hidden =
    $("marketCombinationTargetTypeSelect").value !== "group";
  $("marketCombinationForm").querySelectorAll("input, select, button").forEach((control) => {
    control.disabled = !professional || state.marketDetail?.project?.status !== "active";
  });
}

function selectedMarketRecommendation(ruleType, sensitivity, recommendations = state.marketRecommendations) {
  return recommendations.find(
    (item) => item.rule_type === ruleType && item.sensitivity === sensitivity,
  ) || null;
}

function marketRecommendationControls(mode) {
  const wizard = mode === "wizard";
  return {
    sensitivityWrap: $(wizard ? "marketWizardSensitivityWrap" : "marketSensitivityWrap"),
    sensitivity: $(wizard ? "marketWizardSensitivitySelect" : "marketSensitivitySelect"),
    refresh: $(wizard ? "marketWizardRecommendationRefreshBtn" : "marketRecommendationRefreshBtn"),
    status: $(wizard ? "marketWizardRecommendationStatus" : "marketRecommendationStatus"),
    thresholdWrap: $(wizard ? "marketWizardThresholdWrap" : "marketThresholdWrap"),
    threshold: $(wizard ? "marketWizardThresholdInput" : "marketThresholdInput"),
    unit: $(wizard ? "marketWizardThresholdUnitSelect" : "marketThresholdUnitSelect"),
    thresholdHelp: $(wizard ? "marketWizardThresholdHelp" : "marketThresholdHelp"),
    windowWrap: $(wizard ? "marketWizardWindowWrap" : "marketWindowWrap"),
    window: $(wizard ? "marketWizardWindowInput" : "marketWindowInput"),
    cooldownWrap: $(wizard ? "marketWizardCooldownWrap" : "marketCooldownWrap"),
    cooldown: $(wizard ? "marketWizardCooldownInput" : "marketCooldownInput"),
    repeatWrap: $(wizard ? "marketWizardRepeatWrap" : "marketRepeatWrap"),
    repeatLabel: $(wizard ? "marketWizardRepeatLabel" : "marketRepeatLabel"),
    repeat: $(wizard ? "marketWizardRepeatInput" : "marketRepeatInput"),
    repeatHelp: $(wizard ? "marketWizardRepeatHelpText" : "marketRepeatHelpText"),
  };
}

function updateMarketThresholdHelp(mode, definition = null) {
  const controls = marketRecommendationControls(mode);
  const rule = definition || marketRuleDefinition(
    $(mode === "wizard" ? "marketWizardRuleTypeSelect" : "marketRuleTypeSelect").value,
  );
  controls.thresholdHelp.textContent = !rule || MARKET_EVENT_ONLY_RULES.has(rule.code)
    ? ""
    : t(marketThresholdHelpKey(rule.code, controls.unit.value));
}

function marketRecommendationState(mode) {
  if (mode === "wizard") {
    return {
      recommendations: state.marketWizard.recommendations,
      updatedAt: state.marketWizard.recommendationUpdatedAt,
      loading: state.marketWizard.recommendationLoading,
      error: state.marketWizard.recommendationError,
    };
  }
  return {
    recommendations: state.marketRecommendations,
    updatedAt: state.marketRecommendationUpdatedAt,
    loading: state.marketRecommendationLoading,
    error: state.marketRecommendationError,
  };
}

function formatRecommendationTime(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(state.uiLanguage === "en" ? "en-GB" : "zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).format(date);
}

function applyMarketRecommendationEditor(mode, definition, editable) {
  const controls = marketRecommendationControls(mode);
  const recommendationState = marketRecommendationState(mode);
  const eventOnly = MARKET_EVENT_ONLY_RULES.has(definition?.code);
  const repeatSupported = MARKET_REPEAT_WHILE_ACTIVE_RULES.has(definition?.code);
  const custom = controls.sensitivity.value === "custom";
  const systemRecommended = !eventOnly && !custom;

  controls.sensitivityWrap.hidden = eventOnly;
  controls.thresholdWrap.hidden = eventOnly;
  controls.cooldownWrap.hidden = eventOnly;
  controls.windowWrap.hidden = eventOnly || !Number(definition?.default_window_minutes);
  controls.repeatWrap.hidden = !repeatSupported;
  if (!repeatSupported) controls.repeat.checked = false;
  controls.repeat.disabled = !repeatSupported || !editable;
  controls.repeatLabel.textContent = t(
    MARKET_REPEAT_WHILE_ACTIVE_LABEL_KEYS[definition?.code] || "repeatWhileActive",
  );
  controls.repeatHelp.textContent = t(
    MARKET_REPEAT_WHILE_ACTIVE_HELP_KEYS[definition?.code] || "repeatWhileActiveHelpLabel",
  );
  controls.refresh.hidden = !systemRecommended;
  controls.refresh.disabled = !editable || recommendationState.loading ||
    (mode === "wizard" && state.marketWizard.busy);
  controls.refresh.classList.toggle("is-loading", recommendationState.loading);
  controls.refresh.setAttribute("aria-busy", String(recommendationState.loading));
  controls.refresh.dataset.i18n = recommendationState.loading
    ? "refreshingRecommendation"
    : "refreshRecommendation";
  controls.refresh.textContent = t(controls.refresh.dataset.i18n);

  if (eventOnly && definition) {
    controls.threshold.value = definition.default_threshold;
    controls.unit.value = definition.default_unit || definition.units?.[0] || "count";
    controls.window.value = definition.default_window_minutes || 60;
    controls.cooldown.value = 300;
  } else if (systemRecommended && definition) {
    const recommendation = selectedMarketRecommendation(
      definition.code,
      controls.sensitivity.value,
      recommendationState.recommendations,
    );
    controls.threshold.value = recommendation?.threshold || definition.default_threshold;
    const recommendedUnit = recommendation?.threshold_unit ||
      definition.default_unit || definition.units?.[0];
    if (recommendedUnit &&
      [...controls.unit.options].some((option) => option.value === recommendedUnit)) {
      controls.unit.value = recommendedUnit;
    }
    controls.window.value = recommendation?.window_minutes ||
      definition.default_window_minutes || 60;
    controls.cooldown.value = recommendation?.cooldown_seconds || 300;
  }

  const lockRecommended = systemRecommended || !editable;
  controls.threshold.disabled = lockRecommended;
  controls.unit.disabled = lockRecommended;
  controls.window.disabled = lockRecommended;
  controls.cooldown.disabled = lockRecommended;
  updateMarketThresholdHelp(mode, definition);

  if (recommendationState.error) {
    controls.status.textContent = t("recommendationRefreshFailed");
  } else {
    const updatedTime = formatRecommendationTime(recommendationState.updatedAt);
    controls.status.textContent = systemRecommended && updatedTime
      ? t("recommendationUpdatedAt", { time: updatedTime })
      : "";
  }
}

function marketWizardRecommendationInput() {
  return selectedWizardDeployments().map((deployment) => {
    const selection = marketPoolSelection(deployment.chain_key);
    return {
      chain_key: deployment.chain_key,
      token_address: deployment.contract_address,
      pool_addresses: selection.primary ? [selection.primary] : [],
    };
  }).filter((deployment) => deployment.pool_addresses.length > 0);
}

function marketDetailRecommendationInput() {
  const detail = state.marketDetail;
  if (!detail) return [];
  const primaryByChain = new Map(
    (detail.pools || [])
      .filter((pool) => Number(pool.selected) === 1 && Number(pool.is_primary) === 1 && pool.pool_address)
      .map((pool) => [pool.chain_key, pool.pool_address]),
  );
  const deployments = (detail.deployments || []).map((deployment) => ({
    chain_key: deployment.chain_key,
    token_address: deployment.token_address,
    pool_addresses: primaryByChain.has(deployment.chain_key)
      ? [primaryByChain.get(deployment.chain_key)]
      : [],
  })).filter((deployment) => deployment.pool_addresses.length > 0);
  if (deployments.length > 0) return deployments;
  const project = detail.project;
  const primary = (detail.pools || []).find(
    (pool) => Number(pool.is_primary) === 1 && pool.pool_address,
  );
  return project?.chain_key && project?.token_address && primary
    ? [{
        chain_key: project.chain_key,
        token_address: project.token_address,
        pool_addresses: [primary.pool_address],
      }]
    : [];
}

async function refreshMarketRecommendations(mode) {
  const wizard = mode === "wizard";
  const deployments = wizard
    ? marketWizardRecommendationInput()
    : marketDetailRecommendationInput();
  if (!deployments.length) {
    toast(t("marketNotLoaded"));
    return false;
  }
  if (wizard) {
    state.marketWizard.recommendationLoading = true;
    state.marketWizard.recommendationError = "";
    renderMarketWizardRuleEditor();
  } else {
    state.marketRecommendationLoading = true;
    state.marketRecommendationError = "";
    renderMarketRuleEditor();
  }
  try {
    const result = await api("/api/market/recommendations/preview", {
      method: "POST",
      body: JSON.stringify({ deployments }),
    });
    if (!Array.isArray(result.recommendations) || !result.recommendations.length) {
      throw new Error(t("recommendationRefreshFailed"));
    }
    if (wizard) {
      state.marketWizard.recommendations = result.recommendations;
      state.marketWizard.recommendationUpdatedAt = result.generated_at || new Date().toISOString();
      state.marketWizard.recommendationError = "";
    } else {
      state.marketRecommendations = result.recommendations;
      state.marketRecommendationUpdatedAt = result.generated_at || new Date().toISOString();
      state.marketRecommendationError = "";
    }
    return true;
  } catch (_) {
    if (wizard) {
      state.marketWizard.recommendationError = "refresh_failed";
      if (!state.marketWizard.recommendations.length) {
        $("marketWizardSensitivitySelect").value = "custom";
      }
    } else {
      state.marketRecommendationError = "refresh_failed";
      if (!state.marketRecommendations.length) {
        $("marketSensitivitySelect").value = "custom";
      }
    }
    toast(t("recommendationRefreshFailed"));
    return false;
  } finally {
    if (wizard) {
      state.marketWizard.recommendationLoading = false;
      renderMarketWizardRuleEditor();
      renderMarketWizardSummary();
    } else {
      state.marketRecommendationLoading = false;
      renderMarketRuleEditor();
    }
  }
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
  const frozen = marketProjectIsExpiredFrozen();
  $("marketRulesList").innerHTML = rules.length ? rules.map((rule) => {
    const definition = marketRuleDefinition(rule.rule_type);
    const active = Number(rule.enabled) === 1 && rule.run_status === "active";
    const pauseReason = active ? "" : marketPauseReason(rule.pause_reason);
    return `
      <div class="list-item">
        <div>
          <strong>${escapeHtml(marketRuleName(definition) || marketRuleDisplayName(rule.rule_type))}</strong>
          <span>${escapeHtml(rule.threshold_value)} ${escapeHtml(t(MARKET_UNIT_KEYS[rule.threshold_unit] || rule.threshold_unit))} · ${escapeHtml(t(rule.deployment_scope === "all" ? "marketAllChains" : "marketSelectedChainsScope"))} · ${escapeHtml(marketDeliveryModeLabel(rule.delivery_mode))}</span>
          <small>${escapeHtml(active ? t("marketRuleStatusActive") : t("marketRuleStatusPaused"))}${pauseReason ? ` · ${escapeHtml(pauseReason)}` : ""}</small>
          <small>${escapeHtml(rule.last_triggered_at ? t("marketLastTriggeredAt", { time: marketDate(rule.last_triggered_at) }) : t("marketNotTriggeredYet"))}</small>
        </div>
        <div class="list-item-actions"${frozen ? " hidden" : ""}>
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

function renderMarketCombinations() {
  const combinations = state.marketDetail?.combinations || [];
  const projectActive = state.marketDetail?.project?.status === "active";
  const frozen = marketProjectIsExpiredFrozen();
  $("marketCombinationsList").innerHTML = combinations.length
    ? combinations.map((combination) => {
      const active = marketCombinationIsActive(combination);
      const pauseReason = active ? "" : marketPauseReason(combination.pause_reason);
      const memberNames = (combination.members || []).map((member) => {
        const rule = (state.marketDetail?.rules || []).find(
          (item) => item.id === member.market_rule_id,
        );
        return `${rule?.rule_type ? marketRuleDisplayName(rule.rule_type) : "-"} × ${member.required_trigger_count}`;
      });
      return `
        <div class="list-item market-combination-item">
          <div>
            <strong>${escapeHtml(combination.note || t("marketCombinationRule"))}</strong>
            <span>${escapeHtml(memberNames.join(" + "))}</span>
            <small>${escapeHtml(t("marketCombinationCycle", { minutes: combination.cycle_minutes }))}</small>
            <small>${escapeHtml(active ? t("marketRuleStatusActive") : t("marketRuleStatusPaused"))}${pauseReason ? ` · ${escapeHtml(pauseReason)}` : ""}</small>
          </div>
          <div class="list-item-actions"${frozen ? " hidden" : ""}>
            ${!active && projectActive ? `<button type="button" class="secondary compact" data-restore-market-combination="${combination.id}">${escapeHtml(t("restoreMonitor"))}</button>` : ""}
            ${active ? `<button type="button" class="secondary compact danger" data-delete-market-combination="${combination.id}">${escapeHtml(t("pause"))}</button>` : ""}
            ${!active ? `<button type="button" class="secondary compact danger" data-permanently-delete-market-combination="${combination.id}">${escapeHtml(t("delete"))}</button>` : ""}
          </div>
        </div>
      `;
    }).join("")
    : `<div class="empty-state">${escapeHtml(t("marketNoCombinations"))}</div>`;
  $("marketCombinationsList").querySelectorAll("[data-delete-market-combination]").forEach((button) => {
    button.addEventListener("click", guardAsync(() =>
      archiveMarketCombination(Number(button.dataset.deleteMarketCombination))));
  });
  $("marketCombinationsList").querySelectorAll("[data-restore-market-combination]").forEach((button) => {
    button.addEventListener("click", guardAsync(() =>
      restoreMarketCombination(Number(button.dataset.restoreMarketCombination))));
  });
  $("marketCombinationsList").querySelectorAll("[data-permanently-delete-market-combination]").forEach((button) => {
    button.addEventListener("click", guardAsync(() =>
      deletePausedMarketCombination(Number(button.dataset.permanentlyDeleteMarketCombination))));
  });
}

function renderMarketHolders() {
  const detail = state.marketDetail;
  if (!detail) return;
  const frozen = marketProjectIsExpiredFrozen(detail.project);
  const deployments = marketDetailDeployments();
  const availableChains = new Set(deployments.map((deployment) => deployment.chain_key));
  if (!availableChains.has(state.marketLabelChain)) {
    state.marketLabelChain = deployments[0]?.chain_key || "";
  }
  if (state.marketHolderChain && !availableChains.has(state.marketHolderChain)) {
    state.marketHolderChain = "";
  }
  const selectedChain = state.marketHolderChain;
  $("marketLabelChainSelect").innerHTML = deployments.map((deployment) =>
    `<option value="${escapeHtml(deployment.chain_key)}">${escapeHtml(marketChainName(deployment.chain_key))}</option>`
  ).join("");
  $("marketLabelChainSelect").value = state.marketLabelChain;
  $("marketHolderChainFilter").innerHTML = `
    <option value="">${escapeHtml(t("allChains"))}</option>
    ${deployments.map((deployment) => `<option value="${escapeHtml(deployment.chain_key)}">${escapeHtml(marketChainName(deployment.chain_key))}</option>`).join("")}
  `;
  $("marketHolderChainFilter").value = selectedChain;
  $("marketLabelForm").querySelectorAll("input, select, button").forEach((control) => {
    control.disabled = detail.project?.status !== "active";
  });
  document.querySelector("#marketDetailHoldersPanel .market-advanced-card").hidden = frozen;
  $("marketLabelsList").hidden = frozen;
  $("marketLabelsList").innerHTML = (detail.labels || []).map((label) => `
    <div class="market-label-chip">
      <span>
        <strong>${escapeHtml(label.label || t("holderLabelNone"))}</strong>
        <small>${escapeHtml(marketChainName(label.chain_key))} · ${escapeHtml(shortAddress(label.address))}${Number(label.excluded) ? ` · ${escapeHtml(t("holderLabelExcluded"))}` : ""}</small>
      </span>
      <span class="market-label-actions">
        <button type="button" data-edit-market-label="${label.id}">${escapeHtml(t("holderLabelEdit"))}</button>
        ${Number(label.excluded) ? `<button type="button" data-restore-market-label="${label.id}">${escapeHtml(t("holderLabelRestore"))}</button>` : ""}
        <button type="button" data-delete-market-label="${label.id}">${escapeHtml(t("holderLabelDeleteSetting"))}</button>
      </span>
    </div>
  `).join("");
  $("marketLabelsList").querySelectorAll("[data-edit-market-label]").forEach((button) => {
    button.addEventListener("click", () => editMarketLabel(Number(button.dataset.editMarketLabel)));
  });
  $("marketLabelsList").querySelectorAll("[data-restore-market-label]").forEach((button) => {
    button.addEventListener("click", guardAsync(() =>
      restoreMarketLabel(Number(button.dataset.restoreMarketLabel))));
  });
  $("marketLabelsList").querySelectorAll("[data-delete-market-label]").forEach((button) => {
    button.addEventListener("click", guardAsync(() => deleteMarketLabel(Number(button.dataset.deleteMarketLabel))));
  });
  const labels = new Map((detail.labels || []).map((label) => [
    marketAddressLabelKey(label.chain_key, label.address),
    label,
  ]));
  const holders = (detail.holders || []).filter(
    (holder) => {
      if (selectedChain && holder.chain_key !== selectedChain) return false;
      const label = labels.get(marketAddressLabelKey(holder.chain_key, holder.holder_address));
      return !Number(holder.excluded) && !Number(label?.excluded);
    },
  );
  const visibleHolders = state.marketHoldersExpanded ? holders : holders.slice(0, 1);
  $("marketHoldersList").innerHTML = visibleHolders.map((holder) => {
    const label = labels.get(marketAddressLabelKey(holder.chain_key, holder.holder_address));
    const changeKey = {
      increased: "marketHolderIncreased",
      decreased: "marketHolderDecreased",
      entered: "marketHolderEntered",
      exited: "marketHolderExited",
      unchanged: "marketHolderUnchanged",
    }[holder.change_type] || "marketHolderUnchanged";
    return `
      <div class="market-holder-row">
        <div>
          <strong>#${holder.rank ?? "-"} · ${escapeHtml(shortAddress(holder.holder_address))}${label?.label ? ` <span class="market-address-label">${escapeHtml(label.label)}</span>` : ""}</strong>
          <small>${escapeHtml(marketChainName(holder.chain_key))} · ${escapeHtml(holder.address_kind || "wallet")}</small>
        </div>
        <span>${escapeHtml(holder.balance)} ${escapeHtml(detail.asset?.symbol || detail.project.token_symbol)} · ${escapeHtml(holder.supply_percent || "-")}%</span>
        <span class="market-holder-change ${escapeHtml(holder.change_type || "unchanged")}">${escapeHtml(t(changeKey))}</span>
      </div>
    `;
  }).join("") || `<div class="empty-state">${escapeHtml(t("marketNoHolders"))}</div>`;
  if (holders.length > 1) {
    $("marketHoldersList").insertAdjacentHTML("beforeend", `
      <button type="button" class="market-list-toggle" data-market-holders-toggle aria-expanded="${state.marketHoldersExpanded}">
        <span>${escapeHtml(state.marketHoldersExpanded ? t("collapseList") : t("expandRemainingItems", { count: holders.length - 1 }))}</span>
        <span class="market-list-toggle-chevron" aria-hidden="true">${state.marketHoldersExpanded ? "⌃" : "⌄"}</span>
      </button>
    `);
    $("marketHoldersList").querySelector("[data-market-holders-toggle]").addEventListener("click", () => {
      state.marketHoldersExpanded = !state.marketHoldersExpanded;
      renderMarketHolders();
    });
  }
}

function renderMarketEventFilters() {
  const filters = state.marketEventFilters;
  const frozen = marketProjectIsExpiredFrozen();
  const deployments = marketDetailDeployments();
  const pools = state.marketDetail?.pools || [];
  $("marketEventChainFilter").innerHTML = `
    <option value="">${escapeHtml(t("allChains"))}</option>
    ${deployments.map((deployment) => `<option value="${escapeHtml(deployment.chain_key)}">${escapeHtml(marketChainName(deployment.chain_key))}</option>`).join("")}
  `;
  $("marketEventChainFilter").value = filters.chainKey;
  const ruleTypes = state.marketCatalog?.rules || [];
  $("marketEventTypeFilter").innerHTML = `
    <option value="">${escapeHtml(t("allEventTypes"))}</option>
    ${ruleTypes.map((rule) => `<option value="${escapeHtml(rule.code)}">${escapeHtml(marketRuleName(rule) || marketRuleDisplayName(rule.code))}</option>`).join("")}
  `;
  $("marketEventTypeFilter").value = filters.ruleType;
  const monitorablePools = pools.filter((pool) => Number(pool.supports_event_parsing) === 1);
  const filteredPools = filters.chainKey
    ? monitorablePools.filter((pool) => pool.chain_key === filters.chainKey)
    : monitorablePools;
  $("marketEventPoolFilter").innerHTML = `
    <option value="">${escapeHtml(t("allPools"))}</option>
    ${filteredPools.map((pool) => `<option value="${pool.id}">${escapeHtml(marketChainName(pool.chain_key))} · ${escapeHtml(pool.protocol)} ${escapeHtml(pool.protocol_version)} · ${escapeHtml(pool.token0_symbol)}/${escapeHtml(pool.token1_symbol)}</option>`).join("")}
  `;
  $("marketEventPoolFilter").value = String(filters.poolId || "");
  $("marketEventAddressFilter").value = filters.address;
  const activeCount = Object.values(filters).filter(Boolean).length;
  $("marketEventFilterStatus").textContent = activeCount
    ? t("marketFiltersApplied", { count: activeCount })
    : t("marketNoFilters");
  $("marketEventFiltersForm").hidden = frozen;
  $("marketEventFilterStatus").hidden = frozen;
  $("refreshMarketEventsBtn").hidden = frozen;
}

function marketRuleEventValue(value, unit) {
  if (value === null || value === undefined || value === "") return "-";
  if (unit === "usd") return marketMoney(value);
  if (unit === "percent" || unit === "progress") return `${marketMoney(value, false)}%`;
  if (unit === "ratio") return `${marketMoney(value, false)}×`;
  const unitKey = unit === "count" ? "unitCount" : "unitToken";
  return `${marketMoney(value, false)} ${t(unitKey)}`;
}

function marketRuleEventReason(event) {
  if (event.notification_successful) return "";
  if (event.notification_error === "cooldown_active") return t("marketEventReasonCooldown");
  if (event.notification_error === "holder_excluded") return t("marketEventReasonHolderExcluded");
  return t({
    pending: "marketEventReasonPending",
    sending: "marketEventReasonPending",
    staged: "marketEventReasonStage",
    combined: "marketEventReasonCombination",
    failed: "marketEventReasonFailed",
    skipped: "marketEventReasonSkipped",
  }[event.notification_status] || "marketEventReasonPending");
}

function renderMarketEvents() {
  const list = $("marketEventsList");
  const pools = new Map((state.marketDetail?.pools || []).map((pool) => [pool.id, pool]));
  const visibleEvents = state.marketEventsExpanded
    ? state.marketEvents
    : state.marketEvents.slice(0, 1);
  list.innerHTML = visibleEvents.length ? visibleEvents.map((event) => {
    const definition = marketRuleDefinition(event.rule_type);
    const pool = event.market_pool_id ? pools.get(event.market_pool_id) : null;
    return `
      <article class="market-event-row">
        <div class="market-event-main">
          <span class="market-event-title">
            <strong>${escapeHtml(marketRuleName(definition) || marketRuleDisplayName(event.rule_type))}</strong>
            <span class="badge">${escapeHtml(marketChainName(event.chain_key))}</span>
            <span class="market-notification-status ${event.notification_successful ? "notified" : "not-notified"}">
              ${escapeHtml(t(event.notification_successful ? "marketEventNotified" : "marketEventNotNotified"))}
            </span>
            ${event.address_excluded ? `<span class="badge">${escapeHtml(t("marketEventExcluded"))}</span>` : ""}
          </span>
          <p class="market-event-summary">${escapeHtml(marketEventSummary(event))}</p>
          <div class="market-event-basis">
            <span><b>${escapeHtml(t("eventDetailRuleBasis"))}</b> ${escapeHtml(marketRuleDescription(definition) || t("marketRuleConfiguredBasis"))}</span>
            ${pool ? `<span><b>${escapeHtml(t("marketPool"))}</b> ${escapeHtml(`${pool.protocol} ${pool.protocol_version} · ${pool.token0_symbol}/${pool.token1_symbol}`)}</span>` : ""}
          </div>
          ${Array.isArray(event.combination_notes) ? event.combination_notes.map((note) => `
            <small>${escapeHtml(t("marketEventCombinationNote"))}：${escapeHtml(note)}</small>
          `).join("") : ""}
          ${!event.notification_successful ? `<small class="market-notification-reason">${escapeHtml(marketRuleEventReason(event))}</small>` : ""}
        </div>
        <div class="market-event-side">
          <time>${escapeHtml(marketDate(event.occurred_at))}</time>
          ${event.transaction_hash ? `<small>${escapeHtml(shortAddress(event.transaction_hash))}</small>` : ""}
          <button class="secondary compact" type="button" data-market-event-detail="${escapeHtml(event.id)}">${escapeHtml(t("viewDetails"))}</button>
        </div>
      </article>
    `;
  }).join("") : `<div class="empty-state">${escapeHtml(t("marketNoEvents"))}</div>`;
  list.querySelectorAll("[data-market-event-detail]").forEach((button) => {
    button.addEventListener("click", () => openEventDetailDrawer("market", button.dataset.marketEventDetail));
  });
  if (state.marketEvents.length > 1) {
    list.insertAdjacentHTML("beforeend", `
      <button type="button" class="market-list-toggle" data-market-events-toggle aria-expanded="${state.marketEventsExpanded}">
        <span>${escapeHtml(state.marketEventsExpanded ? t("collapseList") : t("expandRemainingItems", { count: state.marketEvents.length - 1 }))}</span>
        <span class="market-list-toggle-chevron" aria-hidden="true">${state.marketEventsExpanded ? "⌃" : "⌄"}</span>
      </button>
    `);
    list.querySelector("[data-market-events-toggle]").addEventListener("click", () => {
      state.marketEventsExpanded = !state.marketEventsExpanded;
      renderMarketEvents();
    });
  }
  $("loadMoreMarketEventsBtn").hidden = marketProjectIsExpiredFrozen()
    || !state.marketEventsNextBeforeId;
}

async function loadMarketContext() {
  if (!state.deboxUserId) {
    renderMarket();
    return false;
  }
  const [catalog, projects] = await Promise.all([
    api("/api/market/catalog"),
    api("/api/market/projects?include_archived=true"),
  ]);
  state.marketCatalog = catalog;
  state.marketProjects = projects.projects || [];
  renderMarket();
  return true;
}

function setMarketWizardMode(mode) {
  state.marketWizard.mode = mode === "manual" ? "manual" : "name";
  renderMarketWizard();
}

function setMarketWizardBusy(busy) {
  state.marketWizard.busy = busy;
  [
    "marketAssetSearchBtn",
    "resolveMarketManualBtn",
    "marketVerifyAndDiscoverBtn",
    "marketPoolsContinueBtn",
    "marketWizardRecommendationRefreshBtn",
    "createMarketProjectBtn",
  ].forEach((id) => {
    const control = $(id);
    if (!control) return;
    if (id === "marketPoolsContinueBtn") {
      control.disabled = busy || !marketWizardPoolsReady();
    } else if (id === "marketVerifyAndDiscoverBtn") {
      control.disabled = busy || !marketPoolDiscoveryAllowed();
    } else {
      control.disabled = busy;
    }
  });
}

function setMarketVerifyLoading(loading) {
  const button = $("marketVerifyAndDiscoverBtn");
  button.classList.toggle("is-loading", loading);
  button.setAttribute("aria-busy", String(loading));
  button.dataset.i18n = loading ? "marketVerifyingAndDiscovering" : "marketVerifyAndDiscover";
  button.textContent = t(button.dataset.i18n);
}

function setMarketCreateLoading(loading) {
  const button = $("createMarketProjectBtn");
  button.classList.toggle("is-loading", loading);
  button.setAttribute("aria-busy", String(loading));
  button.dataset.i18n = loading ? "marketCreatingProject" : "marketCreateProjectAndRule";
  button.textContent = t(button.dataset.i18n);
}

function clearMarketWizardVerification() {
  state.marketWizard.verification = null;
  state.marketWizard.poolResult = null;
  state.marketWizard.poolSelections = {};
  $("marketIdentityStatus").textContent = "";
  $("marketPoolSelectionStatus").textContent = "";
}

function resetMarketWizard() {
  state.marketWizard = freshMarketWizard();
  state.marketGoal = "price";
  $("marketWizardRepeatInput").checked = false;
  $("marketAssetSearchInput").value = "";
  $("marketAssetSearchStatus").textContent = t("marketAssetSearchHint");
  $("marketManualStatus").textContent = t("marketManualHint");
  $("marketIdentityStatus").textContent = "";
  $("marketPoolSelectionStatus").textContent = "";
  $("marketWizardCreateStatus").textContent = t("marketReadyToCreate");
  $("marketWizardCreateStatus").hidden = false;
  renderMarketWizard();
}

async function searchMarketAssets(event) {
  event?.preventDefault();
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  const query = $("marketAssetSearchInput").value.trim();
  if (query.length < 2) {
    $("marketAssetSearchStatus").textContent = t("marketAssetSearchHint");
    return;
  }
  $("marketAssetSearchStatus").textContent = t("marketAssetSearching");
  const requestID = ++state.marketWizard.searchRequest;
  setMarketWizardBusy(true);
  try {
    const params = new URLSearchParams({ q: query, limit: "12" });
    const result = await api(`/api/market/assets/search?${params}`);
    if (requestID !== state.marketWizard.searchRequest) return;
    state.marketWizard.searchResult = result;
    $("marketAssetSearchStatus").textContent = result.degraded
      ? t("marketAssetSearchFallback")
      : t("marketAssetSearchFound", { count: result.candidates?.length || 0 });
  } catch (error) {
    if (requestID === state.marketWizard.searchRequest) {
      $("marketAssetSearchStatus").textContent = t("marketAssetSearchFailed");
    }
    throw error;
  } finally {
    if (requestID === state.marketWizard.searchRequest) {
      setMarketWizardBusy(false);
      renderMarketAssetCandidates();
    }
  }
}

function selectMarketCandidate(index) {
  const candidate = state.marketWizard.searchResult?.candidates?.[index];
  if (!candidate) return;
  if (existingMarketProjectForCandidate(candidate)) {
    toast(t("marketAlreadyCreatedDeleteFirst"));
    return;
  }
  state.marketWizard.selectedAsset = {
    ...candidate,
    deployments: (candidate.deployments || []).map((deployment) => ({ ...deployment })),
  };
  state.marketWizard.selectedChains = new Set(
    (candidate.deployments || []).map((deployment) => deployment.chain_key),
  );
  clearMarketWizardVerification();
  state.marketWizard.step = 2;
  renderMarketWizard();
  $("marketWizard").scrollIntoView({ behavior: "smooth", block: "start" });
}

function addMarketManualRow() {
  const used = new Set(state.marketWizard.manualRows.map((row) => row.chainKey));
  const nextChain = state.chains.find((chain) => !used.has(chain.key));
  if (!nextChain) return;
  state.marketWizard.manualRows.push({ chainKey: nextChain.key, contractAddress: "" });
  renderMarketManualRows();
}

async function resolveMarketManualContracts() {
  if (!state.deboxUserId) {
    toast(t("connectFirst"));
    return;
  }
  const contracts = state.marketWizard.manualRows.map((row) => ({
    chain_key: row.chainKey,
    contract_address: row.contractAddress.trim(),
  }));
  if (contracts.some((item) => !/^0x[0-9a-fA-F]{40}$/.test(item.contract_address))) {
    toast(t("marketInvalidContract"));
    return;
  }
  if (new Set(contracts.map((item) => item.chain_key)).size !== contracts.length) {
    toast(t("marketDuplicateChain"));
    return;
  }
  $("marketManualStatus").textContent = t("marketCheckingContracts");
  setMarketWizardBusy(true);
  try {
    const result = await api("/api/market/assets/manual-resolve", {
      method: "POST",
      body: JSON.stringify({ contracts }),
    });
    state.marketWizard.manualResult = result;
    if (contracts.length > 1 && !result.can_merge) {
      $("marketManualStatus").textContent = t("marketContractsNotSame");
      toast(t("marketContractsNotSame"));
      return;
    }
    const candidate = (result.candidates || []).find((item) =>
      item.canonical_asset_id === result.contracts?.[0]?.canonical_asset_id
    ) || result.candidates?.[0];
    if (!candidate) throw new Error(t("marketQueryFailed"));
    if (existingMarketProjectForCandidate(candidate)) {
      $("marketManualStatus").textContent = t("marketAlreadyCreatedDeleteFirst");
      toast(t("marketAlreadyCreatedDeleteFirst"));
      return;
    }
    const deploymentsByChain = new Map(
      (candidate.deployments || []).map((item) => [item.chain_key, item]),
    );
    state.marketWizard.selectedAsset = {
      ...candidate,
      deployments: result.contracts.map((item) => ({
        chain_key: item.chain_key,
        chain_id: item.chain_id,
        chain_name: item.chain_name,
        platform_id: item.platform_id,
        contract_address: item.contract_address,
        ...(deploymentsByChain.get(item.chain_key) || {}),
      })),
    };
    state.marketWizard.selectedChains = new Set(
      result.contracts.map((item) => item.chain_key),
    );
    clearMarketWizardVerification();
    state.marketWizard.step = 2;
    $("marketManualStatus").textContent = t("marketContractsResolved");
    renderMarketWizard();
  } catch (error) {
    $("marketManualStatus").textContent = t("marketManualFailed");
    throw error;
  } finally {
    setMarketWizardBusy(false);
  }
}

function selectedWizardDeployments() {
  const asset = state.marketWizard.selectedAsset;
  return (asset?.deployments || []).filter((deployment) =>
    state.marketWizard.selectedChains.has(deployment.chain_key)
  );
}

async function verifyAndDiscoverMarketPools() {
  if (!marketPoolDiscoveryAllowed()) {
    toast(t("marketPoolDiscoveryPaidOnly"));
    syncMarketPoolDiscoveryAccess();
    return;
  }
  const asset = state.marketWizard.selectedAsset;
  const deployments = selectedWizardDeployments();
  if (!asset || !deployments.length) {
    toast(t("marketSelectChainRequired"));
    return;
  }
  const contracts = deployments.map((deployment) => ({
    chain_key: deployment.chain_key,
    contract_address: deployment.contract_address,
  }));
  setMarketWizardBusy(true);
  setMarketVerifyLoading(true);
  $("marketIdentityStatus").textContent = deployments.length > 1
    ? t("marketVerifyingIdentity")
    : t("marketSingleChainVerification");
  try {
    if (deployments.length > 1) {
      state.marketWizard.verification = await api("/api/market/assets/verify-cross-chain", {
        method: "POST",
        body: JSON.stringify({
          canonical_asset_id: asset.canonical_asset_id,
          contracts,
        }),
      });
      $("marketIdentityStatus").textContent = t("marketIdentityVerified", {
        count: deployments.length,
      });
    } else {
      state.marketWizard.verification = {
        canonical_asset_id: asset.canonical_asset_id,
        verification_status: "single_chain",
      };
      $("marketIdentityStatus").textContent = t("marketSingleChainVerified");
    }
    const poolResult = await api("/api/market/pools/discover", {
      method: "POST",
      body: JSON.stringify({
        deployments: deployments.map((deployment) => ({
          chain_key: deployment.chain_key,
          token_address: deployment.contract_address,
        })),
      }),
    });
    state.marketWizard.poolResult = poolResult;
    state.marketWizard.poolSelections = {};
    (poolResult.groups || []).forEach((group) => {
      const first = group.full_monitoring_pools?.[0]?.pair?.pairAddress || "";
      const selection = marketPoolSelection(group.chain_key);
      if (first) {
        selection.selected.add(first);
        selection.primary = first;
      }
    });
    state.marketWizard.step = 3;
    $("marketPoolSelectionStatus").textContent = t("marketPoolsFound", {
      chains: poolResult.groups?.length || 0,
    });
    renderMarketWizard();
  } catch (error) {
    $("marketIdentityStatus").textContent = t("marketIdentityOrPoolsFailed");
    throw error;
  } finally {
    setMarketVerifyLoading(false);
    setMarketWizardBusy(false);
  }
}

function continueMarketWizardToRules() {
  if (!marketWizardPoolsReady()) {
    toast(t("marketSelectPoolEachChain"));
    return;
  }
  state.marketWizard.recommendations = [];
  state.marketWizard.recommendationUpdatedAt = "";
  state.marketWizard.recommendationError = "";
  state.marketWizard.step = 4;
  renderMarketWizard();
  $("marketWizard").scrollIntoView({ behavior: "smooth", block: "start" });
  guardAsync(() => refreshMarketRecommendations("wizard"))();
}

function setMarketWizardStep(step) {
  if (step < 1 || step > state.marketWizard.step) return;
  if (step < state.marketWizard.step && step <= 2) {
    clearMarketWizardVerification();
  }
  state.marketWizard.step = step;
  renderMarketWizard();
}

async function createMarketProject() {
  const wizard = state.marketWizard;
  const asset = wizard.selectedAsset;
  const definition = marketRuleDefinition($("marketWizardRuleTypeSelect").value);
  if (!asset || !wizard.poolResult || !definition?.allowed) {
    toast(t("marketNotLoaded"));
    return;
  }
  if (currentPlan()?.code === "free") {
    toast(t("marketFreeQueryOnly"));
    return;
  }
  const deploymentsByChain = new Map(
    selectedWizardDeployments().map((deployment) => [deployment.chain_key, deployment]),
  );
  const deployments = (wizard.poolResult.groups || []).map((group) => {
    const deployment = deploymentsByChain.get(group.chain_key);
    const selection = marketPoolSelection(group.chain_key);
    return {
      chain_key: group.chain_key,
      token_address: deployment.contract_address,
      pool_addresses: [...selection.selected],
      primary_pool_address: selection.primary,
    };
  });
  setMarketWizardBusy(true);
  setMarketCreateLoading(true);
  $("marketWizardCreateStatus").textContent = "";
  $("marketWizardCreateStatus").hidden = true;
  renderMarketWizardRuleEditor();
  let detail;
  try {
    detail = await api("/api/market/projects", {
      method: "POST",
      body: JSON.stringify({
        canonical_asset_id: asset.canonical_asset_id,
        identity_source: asset.identity_source,
        logo_url: asset.logo_url || "",
        deployments,
      }),
    });
    $("marketWizardCreateStatus").textContent = t("marketCreatingFirstRule");
    $("marketWizardCreateStatus").hidden = false;
    try {
      await api(`/api/market/projects/${detail.project.id}/rules`, {
        method: "POST",
        body: JSON.stringify({
          deployment_scope: "all",
          pool_scope: definition.code === "market_new_pool"
            ? "primary"
            : $("marketWizardPoolScopeSelect").value,
          cooldown_scope: "chain",
          rule_type: definition.code,
          threshold_value: $("marketWizardThresholdInput").value,
          threshold_unit: $("marketWizardThresholdUnitSelect").value,
          window_minutes: $("marketWizardWindowWrap").hidden
            ? null
            : Number($("marketWizardWindowInput").value),
          sensitivity: $("marketWizardSensitivitySelect").value,
          cooldown_seconds: Number($("marketWizardCooldownInput").value),
          repeat_while_active: !$("marketWizardRepeatWrap").hidden &&
            $("marketWizardRepeatInput").checked,
          delivery_mode: "realtime",
          cycle_type: "fixed",
          cycle_minutes: Number($("marketWizardWindowInput").value || 60),
          trigger_count_threshold: 1,
          notification_chat_type: $("marketWizardTargetTypeSelect").value,
          notification_chat_id: $("marketWizardTargetTypeSelect").value === "group"
            ? $("marketWizardGroupSelect").value
            : "",
          notification_language: $("marketWizardLanguageSelect").value,
        }),
      });
      toast(t("marketProjectAndRuleCreated"));
    } catch (ruleError) {
      toast(t("marketProjectCreatedRuleFailed"));
    }
    await loadMarketContext();
    state.marketDetail = await api(`/api/market/projects/${detail.project.id}`);
    state.marketDetailTab = "overview";
    state.marketRuleMode = "single";
    state.marketHolderChain = "";
    state.marketLabelChain = "";
    state.marketExpandedPoolChains = new Set();
    state.marketHoldersExpanded = false;
    state.marketEventsExpanded = false;
    state.marketEventFilters = freshMarketEventFilters();
    state.marketEvents = [];
    state.marketEventsNextBeforeId = null;
    await loadMarketProjectExtras(detail.project.id);
    resetMarketWizard();
    if (isMobileShell()) {
      setMobileView("monitoring", { restoreScroll: false, target: $("marketProjectDetail") });
    } else {
      $("marketProjectDetail").scrollIntoView({ behavior: "smooth", block: "start" });
    }
  } catch (error) {
    $("marketWizardCreateStatus").textContent = t("marketCreationFailed");
    $("marketWizardCreateStatus").hidden = false;
    throw error;
  } finally {
    wizard.busy = false;
    setMarketCreateLoading(false);
    setMarketWizardBusy(false);
    renderMarketWizardRuleEditor();
  }
}

async function openMarketProject(projectId) {
  state.marketDetail = await api(`/api/market/projects/${projectId}`);
  state.marketDetailTab = "overview";
  state.marketRuleMode = "single";
  state.marketHolderChain = "";
  state.marketLabelChain = "";
  state.marketExpandedPoolChains = new Set();
  state.marketHoldersExpanded = false;
  state.marketEventsExpanded = false;
  state.marketEventFilters = freshMarketEventFilters();
  state.marketEvents = [];
  state.marketEventsNextBeforeId = null;
  await loadMarketProjectExtras(projectId);
  $("marketProjectDetail").scrollIntoView({ behavior: "smooth", block: "start" });
}

async function loadMarketProjectExtras(projectId) {
  if (marketProjectIsExpiredFrozen()) {
    await loadMarketEvents(projectId);
    state.marketRecommendations = [];
    state.marketRecommendationUpdatedAt = "";
    state.marketRecommendationError = "";
    renderMarketDetail();
    return;
  }
  const [recommendations] = await Promise.all([
    api(`/api/market/projects/${projectId}/recommendations`),
    loadMarketEvents(projectId),
  ]);
  state.marketRecommendations = recommendations.recommendations || [];
  state.marketRecommendationUpdatedAt = recommendations.generated_at || "";
  state.marketRecommendationError = "";
  renderMarketDetail();
}

async function loadMarketEvents(projectId = state.marketDetail?.project?.id, append = false) {
  if (!projectId) return false;
  const query = new URLSearchParams({ limit: "50" });
  if (append && state.marketEventsNextBeforeId) {
    query.set("before_id", state.marketEventsNextBeforeId);
  }
  const filters = state.marketEventFilters;
  if (filters.chainKey) query.set("chain_key", filters.chainKey);
  if (filters.ruleType) query.set("rule_type", filters.ruleType);
  if (filters.poolId) query.set("pool_id", filters.poolId);
  if (filters.address) query.set("address", filters.address);
  const result = await api(`/api/market/projects/${projectId}/events?${query}`);
  state.marketEvents = append
    ? [...state.marketEvents, ...(result.events || [])]
    : (result.events || []);
  state.marketEventsNextBeforeId = result.next_before_id || null;
  renderMarketEvents();
  return true;
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

async function deleteMarketProject() {
  const project = state.marketDetail?.project;
  if (!project?.id || (project.status !== "archived" && !marketProjectIsExpiredFrozen(project))) return;
  if (!confirm(t("deleteMarketProjectConfirm"))) return;
  await api(`/api/market/projects/${project.id}/permanent`, { method: "DELETE" });
  state.marketDetail = null;
  await loadMarketContext();
  toast(t("marketProjectDeleted"));
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
      market_pool_id: definition.code === "market_new_pool"
        ? null
        : (poolValue ? Number(poolValue) : null),
      rule_type: definition.code,
      threshold_value: $("marketThresholdInput").value,
      threshold_unit: $("marketThresholdUnitSelect").value,
      window_minutes: $("marketWindowWrap").hidden ? null : Number($("marketWindowInput").value),
      sensitivity: $("marketSensitivitySelect").value,
      cooldown_seconds: Number($("marketCooldownInput").value),
      repeat_while_active: !$("marketRepeatWrap").hidden && $("marketRepeatInput").checked,
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

async function createMarketCombination(event) {
  event.preventDefault();
  const noteInput = $("marketCombinationNoteInput");
  const note = noteInput.value.trim();
  if (!note) {
    toast(t("marketCombinationNoteRequired"));
    noteInput.focus();
    return;
  }
  const members = [...document.querySelectorAll("[data-market-combination-member]:checked")]
    .map((checkbox) => ({
      source_type: "market",
      market_rule_id: Number(checkbox.value),
      required_trigger_count: Number(
        document.querySelector(`[data-market-combination-count="${checkbox.value}"]`)?.value || 1,
      ),
    }));
  if (members.length < 2) {
    toast(t("marketCombinationNeedsTwoRules"));
    return;
  }
  const targetType = $("marketCombinationTargetTypeSelect").value;
  await api("/api/market/combinations", {
    method: "POST",
    body: JSON.stringify({
      note,
      cycle_type: "fixed",
      cycle_minutes: Number($("marketCombinationCycleInput").value),
      notification_chat_type: targetType,
      notification_chat_id: targetType === "group"
        ? $("marketCombinationGroupSelect").value
        : "",
      notification_language: $("marketCombinationLanguageSelect").value,
      members,
    }),
  });
  $("marketCombinationNoteInput").value = "";
  await refreshMarketProject();
  state.marketRuleMode = "combination";
  renderMarketRuleMode();
  toast(t("marketCombinationCreated"));
}

async function archiveMarketCombination(combinationId) {
  await api(`/api/market/combinations/${combinationId}`, { method: "DELETE" });
  await refreshMarketProject();
  state.marketRuleMode = "combination";
  renderMarketRuleMode();
  toast(t("marketCombinationPaused"));
}

async function restoreMarketCombination(combinationId) {
  await api(`/api/market/combinations/${combinationId}/restore`, { method: "POST" });
  await refreshMarketProject();
  state.marketRuleMode = "combination";
  renderMarketRuleMode();
  toast(t("marketCombinationRestored"));
}

async function deletePausedMarketCombination(combinationId) {
  if (!confirm(t("marketCombinationDeleteConfirm"))) return;
  await api(`/api/market/combinations/${combinationId}/permanent`, { method: "DELETE" });
  await refreshMarketProject();
  state.marketRuleMode = "combination";
  renderMarketRuleMode();
  toast(t("marketCombinationDeleted"));
}

async function applyMarketEventFilters(event) {
  event.preventDefault();
  state.marketEventFilters = {
    chainKey: $("marketEventChainFilter").value,
    ruleType: $("marketEventTypeFilter").value,
    poolId: $("marketEventPoolFilter").value,
    address: $("marketEventAddressFilter").value.trim(),
  };
  state.marketEvents = [];
  state.marketEventsExpanded = false;
  state.marketEventsNextBeforeId = null;
  await loadMarketEvents();
  renderMarketEventFilters();
}

async function clearMarketEventFilters() {
  state.marketEventFilters = freshMarketEventFilters();
  state.marketEvents = [];
  state.marketEventsExpanded = false;
  state.marketEventsNextBeforeId = null;
  renderMarketEventFilters();
  await loadMarketEvents();
}

async function saveMarketLabel(event) {
  event.preventDefault();
  const projectId = state.marketDetail?.project?.id;
  if (!projectId) return;
  await api(`/api/market/projects/${projectId}/labels`, {
    method: "POST",
    body: JSON.stringify({
      chain_key: $("marketLabelChainSelect").value,
      address: $("marketLabelAddressInput").value.trim(),
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

function marketAddressLabelKey(chainKey, address) {
  return `${String(chainKey || "").toLowerCase()}:${String(address || "").toLowerCase()}`;
}

function marketLabelById(labelId) {
  return (state.marketDetail?.labels || []).find(
    (label) => Number(label.id) === Number(labelId),
  );
}

function editMarketLabel(labelId) {
  const label = marketLabelById(labelId);
  if (!label) return;
  state.marketLabelChain = label.chain_key;
  renderMarketHolders();
  $("marketLabelAddressInput").value = label.address;
  $("marketLabelInput").value = label.label || "";
  $("marketLabelExcludedInput").checked = Number(label.excluded) === 1;
  $("marketLabelForm").closest("details").open = true;
  $("marketLabelAddressInput").focus();
}

async function updateMarketLabel(label, changes) {
  const projectId = state.marketDetail?.project?.id;
  if (!projectId || !label) return;
  await api(`/api/market/projects/${projectId}/labels`, {
    method: "POST",
    body: JSON.stringify({
      chain_key: label.chain_key,
      address: label.address,
      label: changes.label ?? label.label ?? "",
      excluded: changes.excluded ?? Boolean(Number(label.excluded)),
    }),
  });
  await refreshMarketProject();
}

async function restoreMarketLabel(labelId) {
  const label = marketLabelById(labelId);
  if (!label) return;
  if (label.label) {
    await updateMarketLabel(label, { excluded: false });
  } else {
    await api(`/api/market/labels/${label.id}`, { method: "DELETE" });
    await refreshMarketProject();
  }
  toast(t("holderLabelRestored"));
}

async function deleteMarketLabel(labelId) {
  await api(`/api/market/labels/${labelId}`, { method: "DELETE" });
  await refreshMarketProject();
  toast(t("holderLabelDeleted"));
}

function bindMarketSymbolTooltips() {
  const list = $("marketAssetCandidates");
  let pressTimer = null;
  let pressTarget = null;
  let pressStartX = 0;
  let pressStartY = 0;

  const clearPress = () => {
    clearTimeout(pressTimer);
    pressTimer = null;
    pressTarget?.classList.remove("is-tooltip-visible");
    pressTarget = null;
  };

  list.addEventListener("pointerdown", (event) => {
    const target = event.target.closest(".market-candidate-symbol");
    if (!target || event.pointerType === "mouse" || event.button !== 0) return;
    clearPress();
    pressTarget = target;
    pressStartX = event.clientX;
    pressStartY = event.clientY;
    pressTimer = setTimeout(() => {
      if (!pressTarget) return;
      pressTarget.classList.add("is-tooltip-visible");
      const card = pressTarget.closest("[data-market-candidate]");
      card.dataset.marketSymbolLongPress = "true";
      setTimeout(() => {
        delete card.dataset.marketSymbolLongPress;
      }, 800);
    }, 300);
  });

  list.addEventListener("pointermove", (event) => {
    if (!pressTarget) return;
    if (Math.hypot(event.clientX - pressStartX, event.clientY - pressStartY) > 8) {
      clearPress();
    }
  });
  list.addEventListener("pointerup", clearPress);
  list.addEventListener("pointercancel", clearPress);
  list.addEventListener("pointerleave", clearPress);
  list.addEventListener("contextmenu", (event) => {
    if (event.target.closest(".market-candidate-symbol")) event.preventDefault();
  });
}

function bindEvents() {
  $("languageToggleBtn").addEventListener("click", toggleUiLanguage);
  $("connectWalletBtn").addEventListener("click", guardAsync(toggleWalletConnection));
  $("closeNotificationDetailPageBtn").addEventListener("click", closeNotificationDetailPage);
  $("closeEventDetailDrawerBtn").addEventListener("click", closeEventDetailDrawer);
  $("eventDetailBackdrop").addEventListener("click", (event) => {
    if (event.target === event.currentTarget) closeEventDetailDrawer();
  });
  window.addEventListener("popstate", syncNotificationDetailRoute);
  $("payBtn").addEventListener("click", guardAsync(payOrRenew));
  document.querySelectorAll("[data-billing-cycle]").forEach((button) => {
    button.addEventListener("click", () => {
      state.selectedBillingCycle = button.dataset.billingCycle;
      renderPlans();
      loadPaymentConfig();
    });
  });
  $("deletePausedRulesBtn").addEventListener("click", guardAsync(deletePausedRules));
  $("refreshRulesBtn").addEventListener(
    "click",
    () => runManualRefresh("refreshRulesBtn", refreshAccount),
  );
  $("refreshAggregateEventsBtn").addEventListener(
    "click",
    () => runManualRefresh("refreshAggregateEventsBtn", async () => {
      const loaded = await loadAggregateEvents();
      if (!loaded) throw new Error(state.aggregateLoadError || t("refreshFailed"));
      return true;
    }),
  );
  $("loadMoreAggregateEventsBtn").addEventListener("click", guardAsync(() => loadAggregateEvents({ append: true })));
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
  $("marketAssetSearchForm").addEventListener("submit", guardAsync(searchMarketAssets));
  bindMarketSymbolTooltips();
  $("marketAssetSearchInput").addEventListener("input", () => {
    clearTimeout(marketSearchTimer);
    const query = $("marketAssetSearchInput").value.trim();
    if (query.length < 2) {
      $("marketAssetSearchStatus").textContent = t("marketAssetSearchHint");
      return;
    }
    marketSearchTimer = setTimeout(() => {
      guardAsync(searchMarketAssets)();
    }, 420);
  });
  $("marketNameModeBtn").addEventListener("click", () => setMarketWizardMode("name"));
  $("marketManualModeBtn").addEventListener("click", () => setMarketWizardMode("manual"));
  $("addMarketManualRowBtn").addEventListener("click", addMarketManualRow);
  $("resolveMarketManualBtn").addEventListener("click", guardAsync(resolveMarketManualContracts));
  $("marketStep2BackBtn").addEventListener("click", () => setMarketWizardStep(1));
  $("marketVerifyAndDiscoverBtn").addEventListener("click", guardAsync(verifyAndDiscoverMarketPools));
  $("marketStep3BackBtn").addEventListener("click", () => setMarketWizardStep(2));
  $("marketPoolsContinueBtn").addEventListener("click", continueMarketWizardToRules);
  $("marketStep4BackBtn").addEventListener("click", () => setMarketWizardStep(3));
  $("createMarketProjectBtn").addEventListener("click", guardAsync(createMarketProject));
  document.querySelectorAll("[data-market-wizard-step]").forEach((button) => {
    button.addEventListener("click", () => setMarketWizardStep(Number(button.dataset.marketWizardStep)));
  });
  $("marketWizardRuleTypeSelect").addEventListener("change", () => {
    renderMarketWizardRuleEditor();
    renderMarketWizardSummary();
  });
  $("marketWizardSensitivitySelect").addEventListener("change", renderMarketWizardRuleEditor);
  $("marketWizardRecommendationRefreshBtn").addEventListener(
    "click",
    () => runManualRefresh(
      "marketWizardRecommendationRefreshBtn",
      () => refreshMarketRecommendations("wizard"),
    ),
  );
  $("marketWizardThresholdInput").addEventListener("input", () => {
    $("marketWizardSensitivitySelect").value = "custom";
  });
  $("marketWizardThresholdUnitSelect").addEventListener(
    "change",
    () => updateMarketThresholdHelp("wizard"),
  );
  $("marketWizardTargetTypeSelect").addEventListener("change", () => {
    renderMarketWizardRuleEditor();
    renderMarketWizardSummary();
  });
  $("marketWizardPoolScopeSelect").addEventListener("change", renderMarketWizardSummary);
  $("refreshMarketProjectsBtn").addEventListener(
    "click",
    () => runManualRefresh("refreshMarketProjectsBtn", loadMarketContext),
  );
  $("closeMarketDetailBtn").addEventListener("click", () => {
    state.marketDetail = null;
    state.marketRecommendations = [];
    state.marketRecommendationUpdatedAt = "";
    state.marketRecommendationLoading = false;
    state.marketRecommendationError = "";
    state.marketEvents = [];
    state.marketEventsNextBeforeId = null;
    state.marketDetailTab = "overview";
    state.marketRuleMode = "single";
    state.marketHolderChain = "";
    state.marketLabelChain = "";
    state.marketExpandedPoolChains = new Set();
    state.marketHoldersExpanded = false;
    state.marketEventsExpanded = false;
    state.marketEventFilters = freshMarketEventFilters();
    renderMarketDetail();
    $("marketProjectsList").scrollIntoView({ behavior: "smooth", block: "center" });
  });
  $("archiveMarketProjectBtn").addEventListener("click", guardAsync(archiveMarketProject));
  $("deleteMarketProjectBtn").addEventListener("click", guardAsync(deleteMarketProject));
  document.querySelectorAll("[data-market-detail-tab]").forEach((button) => {
    button.addEventListener("click", () => setMarketDetailTab(button.dataset.marketDetailTab));
  });
  $("openMarketRulesTabBtn").addEventListener("click", () => setMarketDetailTab("rules"));
  $("marketSingleRuleModeBtn").addEventListener("click", () => setMarketRuleMode("single"));
  $("marketCombinationRuleModeBtn").addEventListener("click", () => setMarketRuleMode("combination"));
  $("marketRuleForm").addEventListener("submit", guardAsync(createMarketRule));
  $("marketCombinationForm").addEventListener("submit", guardAsync(createMarketCombination));
  $("marketCombinationTargetTypeSelect").addEventListener("change", () => {
    $("marketCombinationGroupWrap").hidden =
      $("marketCombinationTargetTypeSelect").value !== "group";
  });
  $("marketLabelForm").addEventListener("submit", guardAsync(saveMarketLabel));
  $("marketLabelChainSelect").addEventListener("change", () => {
    state.marketLabelChain = $("marketLabelChainSelect").value;
  });
  $("marketHolderChainFilter").addEventListener("change", () => {
    state.marketHolderChain = $("marketHolderChainFilter").value;
    state.marketHoldersExpanded = false;
    renderMarketHolders();
  });
  $("marketEventFiltersForm").addEventListener("submit", guardAsync(applyMarketEventFilters));
  $("clearMarketEventFiltersBtn").addEventListener("click", guardAsync(clearMarketEventFilters));
  $("marketEventChainFilter").addEventListener("change", () => {
    const chainKey = $("marketEventChainFilter").value;
    const poolsByID = new Map(
      (state.marketDetail?.pools || []).map((pool) => [String(pool.id), pool]),
    );
    [...$("marketEventPoolFilter").options].forEach((option) => {
      if (!option.value) return;
      const visible = !chainKey || poolsByID.get(option.value)?.chain_key === chainKey;
      option.hidden = !visible;
      option.disabled = !visible;
    });
    if ($("marketEventPoolFilter").selectedOptions[0]?.disabled) {
      $("marketEventPoolFilter").value = "";
    }
  });
  $("refreshMarketEventsBtn").addEventListener(
    "click",
    () => runManualRefresh("refreshMarketEventsBtn", () => loadMarketEvents()),
  );
  $("loadMoreMarketEventsBtn").addEventListener("click", guardAsync(() => loadMarketEvents(undefined, true)));
  $("marketRuleTypeSelect").addEventListener("change", renderMarketRuleEditor);
  $("marketSensitivitySelect").addEventListener("change", renderMarketRuleEditor);
  $("marketRecommendationRefreshBtn").addEventListener(
    "click",
    () => runManualRefresh(
      "marketRecommendationRefreshBtn",
      () => refreshMarketRecommendations("detail"),
    ),
  );
  $("marketDeliveryModeSelect").addEventListener("change", updateMarketDeliveryFields);
  $("marketTargetTypeSelect").addEventListener("change", renderMarketGroupOptions);
  $("marketThresholdInput").addEventListener("input", () => {
    $("marketSensitivitySelect").value = "custom";
  });
  $("marketThresholdUnitSelect").addEventListener(
    "change",
    () => updateMarketThresholdHelp("detail"),
  );
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
  const repeatHelpButtonIds = ["marketWizardRepeatHelpBtn", "marketRepeatHelpBtn"];
  const helpButtonIds = [
    "balanceHelpBtn",
    "combinationBalanceHelpBtn",
    ...repeatHelpButtonIds,
  ];
  repeatHelpButtonIds.forEach((id) => {
    const button = $(id);
    let pressTimer = null;
    const clearPressTimer = () => {
      clearTimeout(pressTimer);
      pressTimer = null;
    };
    button.addEventListener("pointerdown", (event) => {
      if (event.pointerType === "mouse" || event.button !== 0) return;
      clearPressTimer();
      pressTimer = setTimeout(() => {
        const control = button.closest(".help-control");
        control.classList.add("open");
        button.setAttribute("aria-expanded", "true");
        button.dataset.longPressOpen = "true";
      }, 280);
    });
    ["pointerup", "pointercancel", "pointerleave"].forEach((eventName) => {
      button.addEventListener(eventName, clearPressTimer);
    });
    button.addEventListener("contextmenu", (event) => event.preventDefault());
  });
  helpButtonIds.forEach((id) => {
    $(id).addEventListener("click", (event) => {
      event.stopPropagation();
      if (event.currentTarget.dataset.longPressOpen === "true") {
        delete event.currentTarget.dataset.longPressOpen;
        return;
      }
      const control = event.currentTarget.closest(".help-control");
      const open = !control.classList.contains("open");
      control.classList.toggle("open", open);
      event.currentTarget.setAttribute("aria-expanded", String(open));
    });
  });
  document.addEventListener("click", (event) => {
    if (!$("chainPicker").contains(event.target)) closeChainPicker();
    if (!$("combinationChainPicker").contains(event.target)) closeChainPicker("combination");
    if (!event.target.closest("[data-market-manual-picker]")) closeMarketManualChainPickers();
    if (!event.target.closest(".help-control")) {
      document.querySelectorAll(".help-control.open").forEach((control) => control.classList.remove("open"));
      helpButtonIds.forEach((id) => {
        $(id).setAttribute("aria-expanded", "false");
        $(id).blur();
      });
    }
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closeEventDetailDrawer();
      closeAllChainPickers();
      document.querySelectorAll(".help-control.open").forEach((control) => control.classList.remove("open"));
      helpButtonIds.forEach((id) => {
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
  renderNotificationDetailPage();
  initIOSDeBoxInputZoomGuard();
  initMobileShell();
  initPersistentHorizontalScrollbars();
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
