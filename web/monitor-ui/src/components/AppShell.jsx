import React, { useEffect, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import { PrimaryNav } from "./PrimaryNav";
import { useI18n } from "../lib/i18n";

const SIDEBAR_COLLAPSED_KEY = "trajecta.monitor.sidebar.collapsed";

export function AppShell({ children, user, onLogout }) {
  const { t } = useI18n();
  const location = useLocation();
  const [collapsed, setCollapsed] = useState(() => window.localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "true");
  const mainRef = useRef(null);
  // The first render is not a navigation: focusing the content then would drop a
  // focus ring on the page before the reader has done anything.
  const navigated = useRef(false);

  useEffect(() => {
    window.localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed ? "true" : "false");
  }, [collapsed]);

  // A route change is a new page, and a single-page app never tells a screen
  // reader that. Moving focus to the content is what turns "the router swapped
  // the DOM" into "a new page is here", and it is the same element the skip
  // link targets.
  useEffect(() => {
    if (!navigated.current) {
      navigated.current = true;
      return;
    }
    mainRef.current?.focus({ preventScroll: false });
  }, [location.pathname]);

  // A sticky header covers the top of the viewport, which is exactly where the
  // browser parks a control it scrolls into view when focus travels upwards -
  // the case WCAG 2.2 calls "focus not obscured". `scroll-padding-top` on the
  // scrolling element is the standard fix, and it has to be measured rather than
  // guessed: a list page keeps a one-line header while a trace detail page keeps
  // a title block, its actions and a tab strip, and either grows taller when the
  // text wraps. The content column is what gets observed, not the header, so a
  // page that renders its chrome after its first response is measured too.
  //
  // A page may pin more than its header: the trace detail page pins its tab strip
  // under the title block, because landing on the newest turn of a long
  // conversation scrolls the tabs out of reach. Every element marked
  // `data-sticky-chrome` is measured into the same total, and is handed the
  // offset it pins at - in DOM order, under the header and then under each other
  // - through `--sticky-offset`.
  useEffect(() => {
    const main = mainRef.current;
    if (!main) {
      return undefined;
    }
    const root = document.documentElement;
    let frame = 0;
    let pinned = [];
    const apply = () => {
      frame = 0;
      const head = main.querySelector(".page-header, .topbar");
      let offset = head ? Math.ceil(head.getBoundingClientRect().height) : 0;
      const extras = Array.from(main.querySelectorAll("[data-sticky-chrome]"));
      for (const element of extras) {
        element.style.setProperty("--sticky-offset", `${offset}px`);
        offset += Math.ceil(element.getBoundingClientRect().height);
      }
      pinned = extras;
      root.style.setProperty("--sticky-chrome-h", `${offset}px`);
    };
    const schedule = () => {
      if (frame) {
        return;
      }
      frame = window.requestAnimationFrame(apply);
    };
    schedule();
    const observer = new ResizeObserver(schedule);
    observer.observe(main);
    return () => {
      if (frame) {
        window.cancelAnimationFrame(frame);
      }
      observer.disconnect();
      for (const element of pinned) {
        element.style.removeProperty("--sticky-offset");
      }
      root.style.removeProperty("--sticky-chrome-h");
    };
  }, [location.pathname]);

  return (
    <div className={collapsed ? "app-shell app-shell-collapsed" : "app-shell"}>
      {/* First in the document on purpose: nine destinations and an account menu
          sit above the content on every page. */}
      <a className="skip-link" href="#main-content">
        {t("nav.skipToContent")}
      </a>
      <aside className="app-sidebar">
        <PrimaryNav user={user} onLogout={onLogout} collapsed={collapsed} onToggleCollapsed={() => setCollapsed((value) => !value)} />
      </aside>
      <main className="app-main" id="main-content" ref={mainRef} tabIndex={-1}>
        {children}
      </main>
    </div>
  );
}
