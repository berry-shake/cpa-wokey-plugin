import { useEffect, useRef } from "react";
import type { T } from "../../i18n";
import { LOG_RANGES } from "../../lib/logs";
import { store, type LogsState } from "../../state/store";

/** The time range choices (same as ClinePassBridge) and the custom bounds. */
export function RangeTools({ t, logs, focusStart }: { t: T; logs: LogsState; focusStart: number }) {
  const custom = logs.range === "custom";
  const startInput = useRef<HTMLInputElement>(null);

  // Opening the custom range moves focus to its start once it is visible.
  useEffect(() => {
    if (focusStart) startInput.current?.focus();
  }, [focusStart]);

  return (
    <form
      className="range-tools"
      id="logs-range-form"
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        store.applyLogRange();
      }}
    >
      <div className="range-segments" id="logs-range" role="group" aria-label={t("logs.range.label")} title={t("logs.range.hint")}>
        {LOG_RANGES.map((range) => (
          <button
            key={range}
            className="range-segment"
            type="button"
            data-range={range}
            aria-pressed={range === logs.range ? "true" : "false"}
            aria-controls={range === "custom" ? "logs-range-custom" : undefined}
            aria-expanded={range === "custom" ? (custom ? "true" : "false") : undefined}
            onClick={() => store.selectLogRange(range)}
          >
            {t("logs.range." + range)}
          </button>
        ))}
      </div>
      <div className="range-custom" id="logs-range-custom" hidden={!custom}>
        <label htmlFor="logs-range-start">
          <span>{t("logs.range.from")}</span>{" "}
          <input
            ref={startInput}
            className="input input-sm"
            id="logs-range-start"
            type="datetime-local"
            step="60"
            value={logs.draftStart}
            onChange={(event) => store.setLogsDraft("start", event.target.value)}
          />
        </label>
        <label htmlFor="logs-range-end">
          <span>{t("logs.range.to")}</span>{" "}
          <input
            className="input input-sm"
            id="logs-range-end"
            type="datetime-local"
            step="60"
            value={logs.draftEnd}
            onChange={(event) => store.setLogsDraft("end", event.target.value)}
          />
        </label>
        <button className="btn btn-secondary btn-sm" type="submit">{t("logs.range.apply")}</button>
      </div>
      <span className="range-error" id="logs-range-error" role="status" hidden={!logs.rangeError}>
        {logs.rangeError ? t(logs.rangeError) : ""}
      </span>
    </form>
  );
}
