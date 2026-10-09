import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import { I18nProvider } from "./lib/i18n";
import { applyTheme } from "./lib/theme";

// Load order matters: tokens define the vocabulary, styles.css holds the
// pre-redesign page rules, and the three layers after it re-declare the frame
// and the shared primitives on top of those rules.
import "./styles/tokens.css";
import "./styles.css";
import "./styles/base.css";
import "./styles/layout.css";
import "./styles/components.css";
import "./styles/pages.css";

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
