(() => {
  function escapeHtml(value) {
    return String(value ?? "")
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#039;");
  }

  function object(value) {
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  }

  function list(value) {
    return Array.isArray(value) ? value : [];
  }

  function present(value) {
    return value !== null && value !== undefined && String(value).trim() !== "";
  }

  function humanCode(value) {
    return String(value || "")
      .replace(/^market_/, "")
      .replaceAll("_", " ")
      .replace(/\b\w/g, (character) => character.toUpperCase());
  }

  function plainNotificationText(value) {
    const withLines = String(value || "")
      .replace(/<br\s*\/?\s*>/gi, "\n")
      .replace(/<\/p\s*>/gi, "\n");
    const withoutTags = withLines.replace(/<[^>]*>/g, "");
    const decoder = document.createElement("textarea");
    decoder.innerHTML = withoutTags;
    return (decoder.value || decoder.textContent || "").replace(/\n{3,}/g, "\n\n").trim();
  }

  function dateText(value, language) {
    if (!present(value)) return "";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return String(value);
    return new Intl.DateTimeFormat(language === "en" ? "en" : "zh-CN", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    }).format(date);
  }

  function section(title, content, className = "") {
    if (!content) return "";
    return `
      <section class="notification-detail-section ${escapeHtml(className)}">
        <h3>${escapeHtml(title)}</h3>
        ${content}
      </section>
    `;
  }

  function metrics(items) {
    const visible = items.filter(([, value]) => present(value));
    if (!visible.length) return "";
    return `<div class="notification-detail-metrics">${visible.map(([label, value, tone = ""]) => `
      <div class="notification-detail-metric ${escapeHtml(tone)}">
        <span>${escapeHtml(label)}</span>
        <strong>${escapeHtml(value)}</strong>
      </div>
    `).join("")}</div>`;
  }

  function facts(items) {
    const visible = items.filter(([, value]) => present(value));
    if (!visible.length) return "";
    return `<dl class="notification-detail-facts">${visible.map(([label, value]) => `
      <div><dt>${escapeHtml(label)}</dt><dd>${escapeHtml(value)}</dd></div>
    `).join("")}</dl>`;
  }

  function timeline(items, emptyText, language) {
    if (!items.length) return `<p class="notification-detail-empty">${escapeHtml(emptyText)}</p>`;
    return `<ol class="notification-timeline">${items.map((item) => `
      <li>
        <span class="notification-timeline-dot" aria-hidden="true"></span>
        <div>
          <strong>${escapeHtml(item.title)}</strong>
          ${present(item.description) ? `<p>${escapeHtml(item.description)}</p>` : ""}
          ${present(item.time) ? `<time>${escapeHtml(dateText(item.time, language))}</time>` : ""}
        </div>
      </li>
    `).join("")}</ol>`;
  }

  function ruleName(detail, options) {
    const rule = object(detail.rule);
    return rule.name || options.ruleLabel?.(rule.type) || humanCode(rule.type);
  }

  function friendlyThreshold(value, options) {
    if (!present(value)) return "";
    return String(value).split(";").map((raw) => {
      const part = raw.trim();
      const sendAfter = part.match(/^send_after=(\d+)$/);
      if (sendAfter) return options.t("detailSendAfter", { count: sendAfter[1] });
      const member = part.match(/^([a-z0-9_]+)>=(\d+)(?:@(.+))?$/i);
      if (!member) return localizedValue(part, options);
      const name = options.ruleLabel?.(member[1]) || humanCode(member[1]);
      const minimum = options.t("detailAtLeastTimes", { count: member[2] });
      return `${name}: ${minimum}${member[3] ? ` · ${localizedValue(member[3], options)}` : ""}`;
    }).join(" · ");
  }

  function formattedNumber(value, language) {
    const number = Number(String(value).replaceAll(",", ""));
    if (!Number.isFinite(number)) return "";
    return new Intl.NumberFormat(language === "en" ? "en-US" : "zh-CN", {
      maximumFractionDigits: 8,
    }).format(number);
  }

  function localizedValue(value, options, explicitUnit = "") {
    if (!present(value)) return "";
    const raw = String(value).trim();
    if (/^(unlimited|infinite)$/i.test(raw)) return options.t("detailUnlimited");
    const triggers = raw.match(/^([\d,.]+)\s+triggers?$/i);
    if (triggers) return options.t("detailTimes", { count: formattedNumber(triggers[1], options.language) });

    const numeric = raw.match(/^\$?\s*(-?[\d,.]+)\s*(usd|percent|%)?$/i);
    if (!numeric) return raw;
    const number = formattedNumber(numeric[1], options.language);
    if (!number) return raw;
    const unit = String(explicitUnit || numeric[2] || (raw.startsWith("$") ? "usd" : "")).toLowerCase();
    if (unit === "usd") return `$${number}`;
    if (unit === "percent" || unit === "%") return `${number}%`;
    if (unit === "ratio") return `${number}×`;
    if (unit === "count") return options.t("detailTimes", { count: number });
    return number;
  }

  function combinationMembers(detail) {
    const data = object(detail.data);
    if (detail.notification_kind === "address_combination") {
      return list(object(data.combination).member_progress);
    }
    if (detail.notification_kind === "market_combination") {
      return list(object(data.delivery).combination_members);
    }
    return [];
  }

  function memberRule(member) {
    return object(member.rule || member.market_rule || member.watch_rule);
  }

  function memberName(member, options) {
    const embedded = memberRule(member);
    const type = member.rule_type || embedded.rule_type;
    return options.ruleLabel?.(type) || humanCode(type);
  }

  function memberCondition(member, options) {
    const embedded = memberRule(member);
    const raw = embedded.threshold_value ?? embedded.threshold;
    if (!present(raw)) return "";
    return localizedValue(raw, options, embedded.threshold_unit);
  }

  function friendlyActualValue(detail, options) {
    const data = object(detail.data);
    if (detail.notification_kind === "address_stage") {
      return options.t("detailTimes", { count: object(data.stage).total_trigger_count ?? 0 });
    }
    if (detail.notification_kind === "market_stage") {
      return options.t("detailTimes", { count: object(data.delivery).trigger_count ?? 0 });
    }
    const members = combinationMembers(detail);
    if (members.length) {
      return options.t("detailAllMembersReached", { count: members.length });
    }
    if (detail.domain === "market" || String(detail.notification_kind).startsWith("market_")) {
      const delivery = object(data.delivery);
      return localizedValue(
        delivery.current_value ?? detail.actual_value,
        options,
        object(delivery.rule).threshold_unit,
      );
    }
    return localizedValue(detail.actual_value, options);
  }

  function ruleBasis(detail, options) {
    const t = options.t;
    const rule = object(detail.rule);
    const members = combinationMembers(detail);
    const content = facts([
      [t("ruleType"), ruleName(detail, options)],
      [t("threshold"), members.length
        ? t("detailMemberRulesBelow", { count: members.length })
        : friendlyThreshold(rule.threshold, options)],
      [t("detailActualValue"), friendlyActualValue(detail, options)],
      [t("notificationMethod"), detail.access_scope === "group" ? t("groupTarget") : t("privateTarget")],
      [t("notificationLanguage"), detail.language === "en" ? "English" : t("chinese")],
    ]);
    return section(t("detailRuleBasis"), content);
  }

  function addressRealtime(detail, options) {
    const t = options.t;
    const data = object(detail.data);
    const rule = object(data.rule);
    const event = object(data.alert_event);
    const time = data.occurred_at || event.created_at;
    return [
      metrics([
        [t("detailPreviousValue"), localizedValue(event.previous_value, options)],
        [t("detailCurrentValue"), localizedValue(event.current_value || detail.actual_value, options), "accent"],
        [t("marketToken"), data.token_symbol],
      ]),
      section(t("detailContext"), facts([
        [t("chain"), options.chainName?.(rule.chain_key) || rule.chain_key],
        [t("deliveryMode"), t("realtimeMode")],
        [t("detailOccurredAt"), dateText(time, options.language)],
      ])),
      section(t("detailTimeline"), timeline([{
        title: ruleName(detail, options) || t("addressRealtimeDetail"),
        description: data.note || (present(event.previous_value) && present(event.current_value)
          ? `${localizedValue(event.previous_value, options)} → ${localizedValue(event.current_value, options)}`
          : friendlyActualValue(detail, options)),
        time,
      }], t("detailNoEvents"), options.language)),
    ].join("");
  }

  function addressStage(detail, options) {
    const t = options.t;
    const stage = object(object(detail.data).stage);
    const events = list(stage.events);
    return [
      metrics([
        [t("detailTriggerCount"), stage.total_trigger_count, "accent"],
        [t("detailRequiredCount"), stage.trigger_count_threshold],
        [t("detailPeriod"), present(stage.window_starts_at) && present(stage.window_ends_at)
          ? `${dateText(stage.window_starts_at, options.language)} – ${dateText(stage.window_ends_at, options.language)}`
          : ""],
      ]),
      section(t("detailTimeline"), timeline(events.map((event, index) => ({
        title: `${t("stageEvent")} ${index + 1}`,
        description: event.note || (present(event.previous_value) && present(event.current_value)
          ? `${localizedValue(event.previous_value, options)} → ${localizedValue(event.current_value, options)}`
          : localizedValue(event.current_value, options)),
        time: event.occurred_at,
      })), t("detailNoEvents"), options.language)),
    ].join("");
  }

  function memberCards(members, options) {
    const t = options.t;
    if (!members.length) return `<p class="notification-detail-empty">${escapeHtml(t("detailNoMembers"))}</p>`;
    return `<div class="notification-member-list">${members.map((member, index) => {
      const embeddedRule = memberRule(member);
      const type = member.rule_type || embeddedRule.rule_type;
      const current = member.trigger_count ?? member.total_trigger_count;
      const required = member.required_trigger_count ?? embeddedRule.trigger_count_threshold;
      const reached = Number(current || 0) >= Number(required || 1);
      const condition = memberCondition(member, options);
      return `
        <article class="notification-member-card ${reached ? "reached" : ""}">
          <span>${escapeHtml(`${index + 1}`)}</span>
          <div>
            <strong>${escapeHtml(options.ruleLabel?.(type) || humanCode(type) || `${t("detailMember")} ${index + 1}`)}</strong>
            <small>${escapeHtml(t("detailMemberProgress", { current: current ?? 0, required: required ?? 0 }))}</small>
            ${condition ? `<small>${escapeHtml(t("detailRuleCondition", { value: condition }))}</small>` : ""}
          </div>
          <b>${escapeHtml(reached ? t("detailReached") : t("detailWaiting"))}</b>
        </article>
      `;
    }).join("")}</div>`;
  }

  function addressCombination(detail, options) {
    const t = options.t;
    const combination = object(object(detail.data).combination);
    const members = list(combination.member_progress);
    const events = members.flatMap((member) => list(member.events).map((event) => ({
      title: options.ruleLabel?.(member.rule_type) || humanCode(member.rule_type),
      description: event.note || (present(event.previous_value) && present(event.current_value)
        ? `${localizedValue(event.previous_value, options)} → ${localizedValue(event.current_value, options)}`
        : localizedValue(event.current_value, options)),
      time: event.occurred_at || member.reached_at,
    })));
    return [
      metrics([
        [t("detailMemberCount"), members.length, "accent"],
        [t("detailPeriod"), present(combination.window_starts_at) && present(combination.window_ends_at)
          ? `${dateText(combination.window_starts_at, options.language)} – ${dateText(combination.window_ends_at, options.language)}`
          : ""],
      ]),
      section(t("detailMembers"), memberCards(members, options)),
      section(t("detailTriggerOrder"), timeline(events, t("detailNoEvents"), options.language)),
    ].join("");
  }

  function marketEventDescription(event, delivery) {
    return event.note || delivery.note || [event.token_amount, event.usd_value].filter(present).join(" · ");
  }

  function marketTimeline(events, delivery, options) {
    const t = options.t;
    return timeline(events.map((entry) => {
      const container = object(entry);
      const event = object(container.event || entry);
      return {
        title: options.eventLabel?.(event.event_type) || humanCode(event.event_type) || t("marketEventHistory"),
        description: marketEventDescription(event, container),
        time: event.occurred_at || container.occurred_at,
      };
    }), t("detailNoEvents"), options.language);
  }

  function marketBase(detail, options, mode) {
    const t = options.t;
    const delivery = object(object(detail.data).delivery);
    const project = object(delivery.project);
    const event = object(delivery.event);
    const pool = object(delivery.pool);
    const snapshot = object(delivery.snapshot);
    const currentMetricValue = localizedValue(
      delivery.current_value ?? detail.actual_value,
      options,
      object(delivery.rule).threshold_unit,
    );
    const events = mode === "stage"
      ? (list(delivery.stage_events).length ? list(delivery.stage_events) : list(delivery.recent_events))
      : [event].filter((item) => Object.keys(item).length);
    return [
      metrics([
        [t("detailActualValue"), currentMetricValue, "accent"],
        [t("threshold"), friendlyThreshold(detail.rule?.threshold, options)],
        ...(mode === "stage" ? [
          [t("detailTriggerCount"), delivery.trigger_count],
          [t("detailRequiredCount"), object(delivery.rule).trigger_count_threshold],
        ] : []),
        [t("marketPrice"), event.price_usd || snapshot.price_usd],
        [t("marketLiquidity"), snapshot.liquidity_usd || pool.liquidity_usd],
      ]),
      section(t("detailContext"), facts([
        [t("marketToken"), [project.token_name, project.token_symbol].filter(present).join(" · ")],
        [t("chain"), options.chainName?.(event.chain_key || project.chain_key) || event.chain_key || project.chain_key],
        [t("marketEventType"), options.eventLabel?.(event.event_type) || humanCode(event.event_type)],
        [t("marketPool"), [pool.protocol, pool.protocol_version].filter(present).join(" ")],
        [t("detailPeriod"), present(delivery.starts_at) && present(delivery.ends_at)
          ? `${dateText(delivery.starts_at, options.language)} – ${dateText(delivery.ends_at, options.language)}`
          : ""],
      ])),
      section(t("detailTimeline"), marketTimeline(events, delivery, options)),
    ].join("");
  }

  function marketCombination(detail, options) {
    const t = options.t;
    const delivery = object(object(detail.data).delivery);
    const members = list(delivery.combination_members);
    const events = members.flatMap((member) => {
      const memberTitle = options.ruleLabel?.(member.rule_type) || humanCode(member.rule_type);
      const marketEvents = [...list(member.market_events), ...list(member.recent_events)].map((entry) => {
        const event = object(object(entry).event || entry);
        return {
          title: `${memberTitle} · ${options.eventLabel?.(event.event_type) || humanCode(event.event_type)}`,
          description: marketEventDescription(event, object(entry)),
          time: event.occurred_at,
        };
      });
      const watchEvents = list(member.watch_events).map((event) => ({
        title: memberTitle,
        description: event.note || (present(event.previous_value) && present(event.current_value)
          ? `${localizedValue(event.previous_value, options)} → ${localizedValue(event.current_value, options)}`
          : localizedValue(event.current_value, options)),
        time: event.occurred_at,
      }));
      return [...marketEvents, ...watchEvents];
    }).sort((left, right) => new Date(left.time || 0) - new Date(right.time || 0));
    return [
      metrics([
        [t("detailMemberCount"), members.length, "accent"],
        [t("detailPeriod"), present(delivery.starts_at) && present(delivery.ends_at)
          ? `${dateText(delivery.starts_at, options.language)} – ${dateText(delivery.ends_at, options.language)}`
          : ""],
      ]),
      section(t("detailMembers"), memberCards(members, options)),
      section(t("detailTriggerOrder"), timeline(events, t("detailNoEvents"), options.language)),
    ].join("");
  }

  function dailySummary(detail, options) {
    const t = options.t;
    const data = object(detail.data);
    const statistics = object(data.statistics);
    const metricDefinitions = [
      ["event_count", t("detailAddressEvents")],
      ["market_event_count", t("detailMarketEvents")],
      ["address_risk_event_count", t("detailRiskEvents")],
      ["market_anomaly_count", t("detailMarketAnomalies")],
      ["failed_notification_count", t("notificationFailed")],
      ["market_failed_notification_count", t("detailMarketNotificationFailures")],
    ];
    const summaryMetrics = metricDefinitions.map(([key, label]) => {
      const value = Number(statistics[key] ?? 0);
      const warning = value > 0 && (key.includes("risk") || key.includes("anomaly") || key.includes("failed"));
      return [label, value, warning ? "warning" : ""];
    });
    const addressEvents = list(data.address_events).map((event) => ({
      title: options.ruleLabel?.(event.rule_type) || humanCode(event.rule_type),
      description: present(event.previous_value) && present(event.current_value)
        ? `${localizedValue(event.previous_value, options)} → ${localizedValue(event.current_value, options)}`
        : localizedValue(event.current_value, options),
      time: event.created_at,
    }));
    const marketEvents = list(data.market_events).map((event) => ({
      title: `${event.token_symbol || t("marketToken")} · ${options.eventLabel?.(event.event_type) || humanCode(event.event_type)}`,
      description: [event.token_amount, event.usd_value].filter(present).join(" · "),
      time: event.occurred_at,
    }));
    const marketSummaries = list(data.market_project_chain_summaries);
    const marketSummaryContent = marketSummaries.length ? `<div class="notification-market-summary-list">${marketSummaries.map((summary) => `
      <article class="notification-market-summary-card">
        <div><strong>${escapeHtml([summary.token_name, summary.token_symbol].filter(present).join(" · "))}</strong><span>${escapeHtml(options.chainName?.(summary.chain_key) || summary.chain_key)}</span></div>
        ${facts([
          [t("detailPriceRange"), present(summary.start_price_usd) || present(summary.end_price_usd)
            ? `${summary.start_price_usd || "-"} → ${summary.end_price_usd || "-"}` : ""],
          [t("detailTradeVolume"), summary.trade_volume_usd],
          [t("detailBuySellCount"), `${summary.buy_count || 0} / ${summary.sell_count || 0}`],
          [t("detailLargeTrades"), summary.large_trade_count],
        ])}
      </article>
    `).join("")}</div>` : `<p class="notification-detail-empty">${escapeHtml(t("detailNoMarketSummary"))}</p>`;
    return [
      metrics([
        [t("detailPeriodStart"), dateText(data.period_start, options.language)],
        [t("detailPeriodEnd"), dateText(data.period_end, options.language)],
      ]),
      section(t("detailSummaryNumbers"), metrics(summaryMetrics), "nested"),
      section(t("detailMarketOverview"), marketSummaryContent),
      section(t("detailImportantEvents"), timeline(
        [...addressEvents, ...marketEvents]
          .sort((left, right) => new Date(right.time || 0) - new Date(left.time || 0))
          .slice(0, 10),
        t("detailNoEvents"),
        options.language,
      )),
    ].join("");
  }

  function copyValues(detail, options) {
    const values = list(detail.copy_values);
    if (!values.length) return "";
    return section(options.t("detailCopyValues"), `<div class="notification-copy-list">${values.map((item, index) => `
      <div class="notification-copy-row">
        <div><span>${escapeHtml(item.label)}</span><code>${escapeHtml(item.value)}</code></div>
        <button class="secondary compact" type="button" data-notification-copy="${index}">${escapeHtml(options.t("copy"))}</button>
      </div>
    `).join("")}</div>`);
  }

  function actionLinks(detail, options) {
    const links = list(detail.links).filter((link) => /^https?:\/\//i.test(link.url) || /^\/(?!\/)/.test(link.url));
    if (!links.length) return "";
    return section(options.t("detailActions"), `<div class="notification-detail-actions">${links.map((link) => `
      <a class="${link.kind === "transaction" ? "secondary" : "primary"}" href="${escapeHtml(link.url)}" ${link.kind === "transaction" ? 'target="_blank" rel="noopener noreferrer"' : ""}>${escapeHtml(link.label)}</a>
    `).join("")}</div>`);
  }

  function kindLabel(detail, t) {
    return t({
      address_realtime: "addressRealtimeDetail",
      address_stage: "addressStageDetail",
      address_combination: "addressCombinationDetail",
      market_realtime: "marketRealtimeDetail",
      market_stage: "marketStageDetail",
      market_combination: "marketCombinationDetail",
      daily_summary: "dailySummaryDetail",
    }[detail.notification_kind] || "notificationDetail");
  }

  function body(detail, options) {
    switch (detail.notification_kind) {
      case "address_realtime": return addressRealtime(detail, options);
      case "address_stage": return addressStage(detail, options);
      case "address_combination": return addressCombination(detail, options);
      case "market_realtime": return marketBase(detail, options, "realtime");
      case "market_stage": return marketBase(detail, options, "stage");
      case "market_combination": return marketCombination(detail, options);
      case "daily_summary": return dailySummary(detail, options);
      default: return `<p class="notification-detail-empty">${escapeHtml(options.t("detailUnsupported"))}</p>`;
    }
  }

  function render(detail, options) {
    const t = options.t;
    const notificationText = plainNotificationText(detail.notification_text);
    return `
      <article class="notification-detail-hero">
        <div>
          <span class="notification-detail-kind">${escapeHtml(kindLabel(detail, t))}</span>
          <h1>${escapeHtml(detail.label || ruleName(detail, options) || kindLabel(detail, t))}</h1>
          <p>${escapeHtml(t(detail.access_scope === "group" ? "groupDetailScope" : "privateDetailScope"))}</p>
        </div>
        <div class="notification-detail-dates">
          <span>${escapeHtml(t("detailCreatedAt"))}</span>
          <strong>${escapeHtml(dateText(detail.created_at, options.language))}</strong>
          <span>${escapeHtml(t("detailExpiresAt"))}</span>
          <strong>${escapeHtml(dateText(detail.expires_at, options.language))}</strong>
        </div>
      </article>
      ${notificationText ? section(t("detailNotificationText"), `<p class="notification-message-copy">${escapeHtml(notificationText)}</p>`) : ""}
      ${detail.notification_kind === "daily_summary" ? "" : ruleBasis(detail, options)}
      ${body(detail, options)}
      ${copyValues(detail, options)}
      ${actionLinks(detail, options)}
    `;
  }

  window.H5_NOTIFICATION_DETAIL = { render, plainNotificationText };
})();
