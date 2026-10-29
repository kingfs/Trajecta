import React, { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./ui/dialog";
import { NavIcon } from "./NavIcon";
import { navGroups } from "../lib/nav";
import { useI18n } from "../lib/i18n";
import { applyTheme, currentTheme, THEME_KEY } from "../lib/theme";

/*
 * ⌘K / Ctrl K quick navigation.
 *
 * A console with ten destinations and a tab strip on half of them is still a
 * console where "where is the routing page?" costs a rail scan. The palette is
 * the standard answer for that, and for this product it also has to be the
 * action surface: refreshing a page's data or switching the theme should not
 * require finding the one control that owns it.
 *
 * The list is generated from lib/nav, so it cannot drift from the rail. Only
 * the three actions are local. There is no separate index to maintain, and a
 * destination added to the rail appears here on the next render.
 *
 * The component is mounted once, above the routes. Escape, focus trapping, the
 * portal and focus return are Radix Dialog's; the arrow-key model is ours
 * because focus stays in the input, which is what makes typing and moving at
 * the same time work.
 */
export function CommandPalette({ open, onOpenChange, user }) {
  const { t, language, setLanguage } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const listRef = useRef(null);

  const items = useMemo(() => {
    const destinations = navGroups(user).flatMap((group) =>
      group.items.map((item) => ({
        id: item.to,
        kind: "destination",
        group: t(group.labelKey),
        label: t(item.labelKey),
        detail: item.to,
        icon: item.icon,
        to: item.to,
      })),
    );
    return [
      ...destinations,
      {
        id: "action-refresh",
        kind: "action",
        group: t("palette.actions"),
        label: t("palette.refresh"),
        detail: "",
        icon: "refresh",
        run: () => queryClient.invalidateQueries(),
      },
      {
        id: "action-theme",
        kind: "action",
        group: t("palette.actions"),
        label: t("palette.toggleTheme"),
        detail: "",
        icon: "theme",
        run: () => {
          const next = currentTheme() === "dark" ? "light" : "dark";
          window.localStorage.setItem(THEME_KEY, next);
          applyTheme(next);
        },
      },
      {
        id: "action-language",
        kind: "action",
        group: t("palette.actions"),
        label: t("palette.toggleLanguage"),
        detail: "",
        icon: "language",
        run: () => setLanguage(language === "zh-CN" ? "en" : "zh-CN"),
      },
    ];
  }, [language, queryClient, setLanguage, t, user]);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) {
      return items;
    }
    return items.filter((item) =>
      `${item.label} ${item.group} ${item.detail}`.toLowerCase().includes(needle),
    );
  }, [items, query]);

  // A query change is a new list; the old row index would highlight whatever
  // happened to land in that slot.
  useEffect(() => {
    setActive(0);
  }, [query]);

  useEffect(() => {
    if (!open) {
      return;
    }
    setQuery("");
    setActive(0);
  }, [open]);

  // Keep the highlighted row visible when the list is longer than its box.
  useEffect(() => {
    const list = listRef.current;
    if (!list) {
      return;
    }
    const selected = list.querySelector('[data-selected="true"]');
    selected?.scrollIntoView({ block: "nearest" });
  }, [active, filtered.length]);

  // A language switch rebuilds the list and can shrink it under the old
  // index; render and Enter both read this clamped value.
  const activeIndex = filtered.length ? Math.min(active, filtered.length - 1) : 0;
  const close = () => onOpenChange(false);

  const choose = (item) => {
    if (!item) {
      return;
    }
    close();
    if (item.kind === "destination") {
      navigate(item.to);
      return;
    }
    item.run();
  };

  const move = (delta) => {
    if (!filtered.length) {
      return;
    }
    setActive((current) => (Math.min(current, filtered.length - 1) + delta + filtered.length) % filtered.length);
  };

  const onKeyDown = (event) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      move(1);
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      move(-1);
      return;
    }
    if (event.key === "Home") {
      event.preventDefault();
      setActive(0);
      return;
    }
    if (event.key === "End") {
      event.preventDefault();
      setActive(Math.max(0, filtered.length - 1));
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      choose(filtered[activeIndex]);
    }
  };

  // One group heading when the group changes. Filtering keeps the source order,
  // so the headings stay in the same place as the rail's groups do.
  let previousGroup = null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="command-dialog"
        aria-label={t("palette.title")}
        style={{ top: "12vh", translate: "-50% 0", width: "min(640px, calc(100vw - 32px))", maxWidth: "none", padding: 0 }}
      >
        <DialogTitle className="sr-only">{t("palette.title")}</DialogTitle>
        <DialogDescription className="sr-only">{t("palette.placeholder")}</DialogDescription>
        <div className="command-search">
          <span className="command-search-icon" aria-hidden="true">
            <Search size={16} />
          </span>
          <input
            autoFocus
            type="text"
            role="combobox"
            aria-expanded="true"
            aria-autocomplete="list"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder={t("palette.placeholder")}
            aria-label={t("palette.placeholder")}
            aria-controls="command-list"
            aria-activedescendant={filtered[activeIndex] ? `command-${filtered[activeIndex].id}` : undefined}
            autoComplete="off"
            spellCheck="false"
          />
          <kbd className="command-key">esc</kbd>
        </div>

        <div className="command-list" id="command-list" role="listbox" aria-label={t("palette.title")} ref={listRef}>
          {filtered.length ? (
            filtered.map((item, index) => {
              const heading = item.group !== previousGroup ? item.group : null;
              previousGroup = item.group;
              return (
                <React.Fragment key={item.id}>
                  {heading ? <div className="command-group-label">{heading}</div> : null}
                  <button
                    id={`command-${item.id}`}
                    type="button"
                    role="option"
                    aria-selected={index === activeIndex}
                    data-selected={index === activeIndex ? "true" : "false"}
                    className="command-item"
                    tabIndex={-1}
                    onMouseEnter={() => setActive(index)}
                    onClick={() => choose(item)}
                  >
                    <span className="command-item-icon" aria-hidden="true">
                      <NavIcon name={item.icon} size={16} />
                    </span>
                    <span className="command-item-label">{item.label}</span>
                    {item.detail ? <span className="command-item-path">{item.detail}</span> : null}
                  </button>
                </React.Fragment>
              );
            })
          ) : (
            <p className="command-empty">{t("palette.empty")}</p>
          )}
        </div>

        <div className="command-foot" aria-hidden="true">
          <kbd className="command-key">↑</kbd>
          <kbd className="command-key">↓</kbd>
          <span>{t("palette.move")}</span>
          <kbd className="command-key">↵</kbd>
          <span>{t("palette.openHint")}</span>
        </div>
      </DialogContent>
    </Dialog>
  );
}
