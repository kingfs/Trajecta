import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import { I18nProvider } from "./lib/i18n";
import { applyTheme } from "./lib/theme";

// tokens.css stays outside the cascade layers: it only declares custom
// properties, and the theme attribute selectors it uses have to keep beating
// the `:root` defaults. Everything else is imported by tailwind.css, which
// demotes it into a `legacy` layer below Tailwind's utilities.
import "./styles/tokens.css";
import "./styles/tailwind.css";

applyTheme();

ReactDOM.createRoot(document.getElementById("root")).render(
  <React.StrictMode>
    <I18nProvider>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </I18nProvider>
  </React.StrictMode>,
);
