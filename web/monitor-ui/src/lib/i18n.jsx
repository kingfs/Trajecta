import React, { createContext, useContext, useEffect, useMemo } from "react";
import i18next from "i18next";
import { initReactI18next, useTranslation } from "react-i18next";
import resourcesToBackend from "i18next-resources-to-backend";
import {
  DEFAULT_LANGUAGE,
  FALLBACK_LANGUAGE,
  baseOptions,
  languageOptions,
  loadMessages,
  normalizeLanguage,
  supportedLanguages,
} from "./i18nOptions";

export { languageOptions, normalizeLanguage, supportedLanguages };

export const LANGUAGE_KEY = "trajecta.monitor.language";

export function currentLanguage() {
  if (typeof window === "undefined") {
    return DEFAULT_LANGUAGE;
  }
  return normalizeLanguage(window.localStorage.getItem(LANGUAGE_KEY));
}

let bootstrap;

/**
 * Resolves once the active language is usable. Callers must await it before
 * rendering, because until then `t()` returns keys.
 */
export function bootstrapI18n() {
  if (!bootstrap) {
    bootstrap = (async () => {
      i18next.use(resourcesToBackend(loadMessages)).use(initReactI18next);
      const language = currentLanguage();
      // Probe the active locale before handing over. i18next resolves `init`
      // even when a namespace fails to load, so without this a missing chunk
      // would render raw keys instead of falling back. The probe and the
      // backend resolve the same module record, so this is not a second fetch.
      const usable = await loadMessages(language).then(
        () => true,
        () => false,
      );
      // No `resources` here on purpose: passing them makes i18next treat every
      // language as already bundled and never consult the backend, which is
      // exactly what switching language at runtime needs it to do.
      return i18next.init({ ...baseOptions(), lng: usable ? language : FALLBACK_LANGUAGE });
    })();
  }
  return bootstrap;
}

/**
 * Translate outside React, for the helpers that take `t` as an argument.
 * Returns the key itself when i18next has not been initialised yet, which is
 * what i18next would do anyway.
 */
export function translate(language, key, values = {}) {
  if (!i18next.isInitialized) {
    return key;
  }
  return i18next.getFixedT(normalizeLanguage(language))(key, values);
}

const I18nContext = createContext(null);

export function I18nProvider({ children }) {
  const { i18n, t } = useTranslation();
  const language = normalizeLanguage(i18n.resolvedLanguage || i18n.language);

  useEffect(() => {
    document.documentElement.lang = language === "en" ? "en" : "zh-CN";
  }, [language]);

  const value = useMemo(() => {
    const setLanguage = async (nextLanguage) => {
      const normalized = normalizeLanguage(nextLanguage);
      window.localStorage.setItem(LANGUAGE_KEY, normalized);
      // Load first, switch second: `changeLanguage` on its own renders one frame
      // with the untranslated keys while the module is still in flight.
      await i18next.loadLanguages(normalized);
      await i18n.changeLanguage(normalized);
    };
    return {
      language,
      setLanguage,
      t: (key, values) => t(key, values),
    };
  }, [i18n, language, t]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n() {
  const value = useContext(I18nContext);
  if (!value) {
    return {
      language: DEFAULT_LANGUAGE,
      setLanguage: () => {},
      t: (key, values) => translate(DEFAULT_LANGUAGE, key, values),
    };
  }
  return value;
}
