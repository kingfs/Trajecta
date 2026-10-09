export const THEME_KEY = "trajecta.monitor.theme";

export type ThemePreference = "system" | "dark" | "light";
export type ResolvedTheme = "dark" | "light";

export const themeOptions = [
  { value: "system", label: "System", short: "S" },
  { value: "dark", label: "Dark", short: "D" },
  { value: "light", label: "Light", short: "L" },
];

const PREFERENCES: readonly string[] = themeOptions.map((option) => option.value);

/** The stored preference, which may be "system". */
export function currentTheme(): ThemePreference {
  const stored = window.localStorage.getItem(THEME_KEY);
  return (stored && PREFERENCES.includes(stored) ? stored : "system") as ThemePreference;
}

/**
 * The theme a preference actually means. "system" is resolved here rather than
 * in a `prefers-color-scheme` media query so that `data-theme` always names the
 * theme in use: the stylesheet then has one block per theme instead of one per
 * theme plus a third copy inside the media query, and `dark:` variants need a
 * single selector.
 */
export function resolveTheme(preference: ThemePreference = currentTheme()): ResolvedTheme {
  if (preference === "dark" || preference === "light") return preference;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

/**
 * Fired whenever `data-theme` changes, so a component can follow the theme
 * without owning the preference. The toaster needs it: it is not a descendant of
 * the picker, and its own theming has to agree with the surface behind it.
 */
export const THEME_EVENT = "trajecta:theme";

/** Point the document at a preference and return the theme that was applied. */
export function applyTheme(theme: ThemePreference = currentTheme()): ResolvedTheme {
  const normalized = (PREFERENCES.includes(theme) ? theme : "system") as ThemePreference;
  const resolved = resolveTheme(normalized);
  document.documentElement.dataset.theme = resolved;
  // The colour scales in @radix-ui/colors swap under `.dark`, and this is what
  // puts it there. `data-theme` stays the app's own switch - the Tailwind
  // `dark:` variant and the tests read it - and the class is only how the
  // upstream scales are selected, so the two are written together.
  document.documentElement.classList.toggle("dark", resolved === "dark");
  window.dispatchEvent(new CustomEvent(THEME_EVENT, { detail: resolved }));
  return resolved;
}

/**
 * Keep "system" honest while the page is open: without this the console would
 * stay on whatever the OS was set to when the tab was opened. Returns a
 * teardown function.
 */
export function watchSystemTheme(onChange?: (theme: ResolvedTheme) => void): () => void {
  const media = window.matchMedia("(prefers-color-scheme: dark)");
  const listener = () => {
    if (currentTheme() !== "system") return;
    const resolved = applyTheme("system");
    onChange?.(resolved);
  };
  media.addEventListener("change", listener);
  return () => media.removeEventListener("change", listener);
}
