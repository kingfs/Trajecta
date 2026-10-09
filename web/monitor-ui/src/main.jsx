import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClientProvider } from "@tanstack/react-query";
import App from "./App";
import { Toaster } from "./components/ui/sonner";
import { I18nProvider, bootstrapI18n } from "./lib/i18n";
import { queryClient } from "./lib/queryClient";
import { MotionProvider } from "./lib/motion";
import { applyTheme, watchSystemTheme } from "./lib/theme";

// tokens.css stays outside the cascade layers: it only declares custom
// properties, and the theme attribute selectors it uses have to keep beating
// the `:root` defaults. Everything else is imported by tailwind.css, which
// demotes it into a `legacy` layer below Tailwind's utilities.
import "./styles/tokens.css";
import "./styles/tailwind.css";

applyTheme();
// index.html sets the same value before first paint; this keeps "system" honest
// when the OS preference changes while the page is open.
watchSystemTheme();

// The active locale is a separate chunk, so the app cannot render before it has
// been fetched. bootstrapI18n falls back to English if that fetch fails, so a
// broken locale degrades to English instead of a blank page.
bootstrapI18n().finally(() => {
  ReactDOM.createRoot(document.getElementById("root")).render(
    <React.StrictMode>
      <QueryClientProvider client={queryClient}>
        <MotionProvider>
          <I18nProvider>
            <BrowserRouter>
              <App />
              <Toaster />
            </BrowserRouter>
          </I18nProvider>
        </MotionProvider>
      </QueryClientProvider>
    </React.StrictMode>,
  );
});
