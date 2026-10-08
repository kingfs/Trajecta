import React from "react";
import { MONITOR_WINDOW_OPTIONS } from "../../lib/monitor";
import { useI18n } from "../../lib/i18n";

// TabStrip is the page-level section switch: one row of underline tabs bound to
// the `?tab=` query parameter by TabbedPage, so every panel of a page is a
// linkable URL rather than a piece of component state.
export function TabStrip({ tabs, activeId, onSelect, label }) {
  return (
    <div className="tab-strip" role="tablist" aria-label={label}>
      {tabs.map((tab) => {
        const selected = tab.id === activeId;
        return (
          <button
            key={tab.id}
            type="button"
            role="tab"
            id={`tab-${tab.id}`}
            aria-selected={selected}
            aria-controls={`tabpanel-${tab.id}`}
            tabIndex={selected ? 0 : -1}
            className={selected ? "tab-strip-item tab-strip-item-active" : "tab-strip-item"}
            onClick={() => onSelect(tab.id)}
          >
            <span>{tab.label}</span>
            {tab.count === undefined || tab.count === null ? null : <span className="tab-strip-count">{tab.count}</span>}
          </button>
        );
      })}
    </div>
  );
}

// SegmentedControl is the in-card sibling of TabStrip: filters, window pickers
// and small mutually exclusive choices that should not look like navigation.
export function SegmentedControl({ options = [], value, onChange, label }) {
  if (!options.length) {
    return null;
  }
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((option) => {
        const selected = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={selected}
            title={option.title || undefined}
            className={selected ? "segmented-item segmented-item-active" : "segmented-item"}
            onClick={() => onChange?.(option.value)}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}

// WindowToggle renders the shared time-window picker. Every analytics page used
// to print the raw option value ("today", "7d"), which meant the same control
// read differently from the rest of the Chinese UI.
export function WindowToggle({ value, onChange, label }) {
  const { t } = useI18n();
  return (
    <SegmentedControl
      label={label || t("common.windowLabel")}
      value={value}
      onChange={onChange}
      options={MONITOR_WINDOW_OPTIONS.map((option) => ({ value: option, label: t(`common.window.${option}`) }))}
    />
  );
}
