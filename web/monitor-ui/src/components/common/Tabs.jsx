import React from "react";
import { MONITOR_WINDOW_OPTIONS } from "../../lib/monitor";
import { useI18n } from "../../lib/i18n";
import { TabsContent, TabsList, TabsTrigger, Tabs } from "../ui/tabs";

/*
 * TabStrip is the page-level section switch: one row of underline tabs bound to
 * the `?tab=` query parameter by TabbedPage, so every panel of a page is a
 * linkable URL rather than a piece of component state.
 *
 * It is a thin adapter over the Radix Tabs primitive now. The hand-written
 * version had the right ARIA attributes but none of the keyboard behaviour they
 * imply: with only the active tab focusable and no arrow-key handling, every
 * other tab was unreachable from the keyboard. Radix implements the pattern.
 *
 * The controlled `value`/`onValueChange` pair is what keeps the URL the source
 * of truth; `Tabs` holds no state of its own here.
 */
export function TabStrip({ tabs, activeId, onSelect, label }) {
  return (
    <TabsList aria-label={label}>
      {tabs.map((tab) => (
        <TabsTrigger
          key={tab.id}
          value={tab.id}
          active={tab.id === activeId}
          onClick={() => onSelect(tab.id)}
        >
          <span>{tab.label}</span>
          {tab.count === undefined || tab.count === null ? null : (
            <span className="rounded-pill bg-accent px-1.5 text-label leading-4 font-medium text-muted-foreground">
              {tab.count}
            </span>
          )}
        </TabsTrigger>
      ))}
    </TabsList>
  );
}

export { Tabs, TabsContent };

// SegmentedControl is the in-card sibling of TabStrip: filters, window pickers
// and small mutually exclusive choices that should not look like navigation.
//
// It stays a hand-written group of buttons on purpose. Radix's ToggleGroup is
// the equivalent primitive, but these are `aria-pressed` buttons rather than
// tabs, they carry no keyboard pattern beyond Tab and Enter, and the pill
// styling is already shared with the legacy `.view-toggle` in four pages.
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
