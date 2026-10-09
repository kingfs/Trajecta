/*
 * i18next configuration, deliberately free of React and of any browser global
 * so it can be exercised by `node --test` as well as by lib/i18n.jsx. The two
 * mistakes this file has to keep not making are both invisible in a browser
 * until a specific string renders wrong:
 *
 *   - i18next interpolates `{{name}}` by default. Every message here was
 *     written as `{name}`, so the delimiters have to be redefined or the
 *     placeholders are emitted literally.
 *   - keys are flat names containing dots ("metric.totalDuration"), so both the
 *     key and namespace separators have to be off or every lookup misses.
 */

export const DEFAULT_LANGUAGE = "zh-CN";
export const FALLBACK_LANGUAGE = "en";

export type LanguageOption = {
  value: string;
  label: string;
  short: string;
};

export const languageOptions: LanguageOption[] = [
  { value: "zh-CN", label: "中文", short: "中" },
  { value: "en", label: "English", short: "EN" },
];

export const supportedLanguages = languageOptions.map((option) => option.value);

/** Messages are flat `{ "dotted.key": "text" }` maps. */
export type Messages = Record<string, string>;

/** Anything the browser might hand back from localStorage. */
export function normalizeLanguage(value: unknown): string {
  return typeof value === "string" && languageOptions.some((option) => option.value === value) ? value : DEFAULT_LANGUAGE;
}

/**
 * Messages live in `src/locales/<language>.js` and are pulled in with a dynamic
 * import, so the browser downloads only the language in use. They used to be a
 * single object literal inside lib/i18n.jsx, which put every Chinese and English
 * string in the entry chunk: 130.6 kB minified, a third of the application code
 * in the bundle, half of which no single reader can ever see.
 *
 * Vite turns the template literal into one chunk per matching module.
 */
export function loadMessages(language: string): Promise<Messages> {
  // Vite needs a statically analysable pattern here; it emits one chunk per
  // matching module. TypeScript cannot resolve the template, so the assertion
  // says what the bundler guarantees: every supported language ships a default
  // export that is a Messages map.
  return import(`../locales/${language}.js`).then(
    (module) => (module as { default: Messages }).default,
  );
}

/** Everything except `lng`, which the caller decides. */
export function baseOptions() {
  return {
    // Not FALLBACK_LANGUAGE. i18next loads every language in the fallback chain
    // through the backend, so a fallback would fetch a second ~16 kB chunk for
    // every reader and give back most of what the split saves. Both tables are
    // generated from the same key set and the unit tests enforce that, so there
    // is nothing to fall back to; bootstrapI18n handles a failed load instead.
    fallbackLng: false,
    supportedLngs: supportedLanguages,
    // "zh-CN" must not be reduced to "zh", and i18next must not go looking for a
    // locale file this app does not ship.
    load: "currentOnly",
    nonExplicitSupportedLngs: false,
    ns: ["translation"],
    defaultNS: "translation",
    keySeparator: false,
    nsSeparator: false,
    // React escapes interpolated values already.
    interpolation: { prefix: "{", suffix: "}", escapeValue: false },
    react: { useSuspense: false },
  };
}
