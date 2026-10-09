import { useEffect, useState } from "react";
import { Toaster as SonnerToaster } from "sonner";
import { resolveTheme, THEME_EVENT, type ResolvedTheme } from "../../lib/theme";

/*
 * Toasts, on sonner.
 *
 * Every panel used to grow its own feedback state: a `status` or `error` string
 * rendered into the page, which pushed the layout around, survived navigation,
 * and had to be cleared by hand before the next attempt. Four of the pages
 * rendered a byte-identical notice block for the same analysis-job result. A
 * toast is the shape that feedback actually has: it belongs to the action, not
 * to the page.
 *
 * The palette comes from the design tokens rather than sonner's defaults, and it
 * is passed as inline custom properties because sonner injects its stylesheet at
 * runtime: its rules land after this app's stylesheet and win by source order at
 * equal specificity, so overriding the variables is the reliable seam. Anything
 * set here is a token, so both themes come out of the same declaration.
 */
const TOAST_TOKENS = {
  "--normal-bg": "var(--surface-1)",
  "--normal-text": "var(--text-primary)",
  "--normal-border": "var(--border-subtle)",
  "--normal-bg-hover": "var(--surface-2)",
  "--normal-border-hover": "var(--border-strong)",
  "--success-bg": "var(--surface-1)",
  "--success-text": "var(--success)",
  "--success-border": "var(--border-subtle)",
  "--error-bg": "var(--surface-1)",
  "--error-text": "var(--danger)",
  "--error-border": "var(--danger-border)",
  "--info-bg": "var(--surface-1)",
  "--info-text": "var(--info)",
  "--info-border": "var(--border-subtle)",
  "--warning-bg": "var(--surface-1)",
  "--warning-text": "var(--warning)",
  "--warning-border": "var(--warning-border)",
  "--border-radius": "var(--radius-lg)",
} as React.CSSProperties;

/**
 * The theme in use, followed through the event `applyTheme` publishes. Reading
 * `data-theme` alone would be a snapshot: the account panel's theme picker is not
 * an ancestor of the toaster, so nothing would re-render it.
 */
function useResolvedTheme(): ResolvedTheme {
  const [theme, setTheme] = useState<ResolvedTheme>(() => resolveTheme());
  useEffect(() => {
    const sync = (event: Event) => setTheme((event as CustomEvent<ResolvedTheme>).detail);
    window.addEventListener(THEME_EVENT, sync);
    return () => window.removeEventListener(THEME_EVENT, sync);
  }, []);
  return theme;
}

export function Toaster() {
  const theme = useResolvedTheme();
  return (
    <SonnerToaster
      theme={theme}
      position="bottom-right"
      // Long enough to read a job result, short enough not to sit on the page.
      duration={5000}
      closeButton
      style={TOAST_TOKENS}
      toastOptions={{ className: "font-sans text-sm" }}
    />
  );
}
