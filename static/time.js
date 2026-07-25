(function installTimeFormatting(global) {
  "use strict";

  function pad(value) {
    return String(value).padStart(2, "0");
  }

  function formatUtc(date, language) {
    const value = [
      date.getUTCFullYear(),
      "-",
      pad(date.getUTCMonth() + 1),
      "-",
      pad(date.getUTCDate()),
      " ",
      pad(date.getUTCHours()),
      ":",
      pad(date.getUTCMinutes()),
      ":",
      pad(date.getUTCSeconds()),
    ].join("");
    return language === "en" ? `${value} (UTC)` : `${value}（UTC）`;
  }

  function formatWithTimeZone(date, language, timeZone, timeZoneName) {
    const formatter = new global.Intl.DateTimeFormat("en-CA", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
      timeZone,
      timeZoneName,
    });
    const parts = Object.fromEntries(
      formatter
        .formatToParts(date)
        .filter((part) => part.type !== "literal")
        .map((part) => [part.type, part.value]),
    );
    if (!parts.year || !parts.month || !parts.day || !parts.hour || !parts.minute || !parts.second || !parts.timeZoneName) {
      throw new Error("Incomplete local time format");
    }
    const value = `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`;
    return language === "en"
      ? `${value} (${parts.timeZoneName})`
      : `${value}（${parts.timeZoneName}）`;
  }

  function formatExpiryDate(value, language = "zh", timeZone) {
    const date = new global.Date(value);
    if (Number.isNaN(date.getTime())) return "-";
    if (!global.Intl?.DateTimeFormat) return formatUtc(date, language);

    for (const timeZoneName of ["shortOffset", "short"]) {
      try {
        return formatWithTimeZone(date, language, timeZone, timeZoneName);
      } catch (_) {
        // Older embedded browsers may not support the requested time-zone format.
      }
    }
    return formatUtc(date, language);
  }

  global.H5_TIME = Object.freeze({ formatExpiryDate });
})(window);
