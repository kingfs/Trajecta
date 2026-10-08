import React from "react";

// PageHeader is the one header every page renders. Standalone pages keep their
// own `.topbar` markup, which is styled to match, so the console never shows
// two different header treatments side by side.
export function PageHeader({ eyebrow, title, subtitle, actions, meta }) {
  return (
    <header className="page-header">
      <div className="page-header-title">
        {eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}
        <h1>{title}</h1>
        {subtitle ? <p className="page-header-subtitle">{subtitle}</p> : null}
      </div>
      {actions || meta ? (
        <div className="page-header-actions">
          {meta}
          {actions}
        </div>
      ) : null}
    </header>
  );
}
