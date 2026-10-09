import React from "react";

/*
 * The one header every page renders: the page title, and on the right the
 * controls that act on the whole page - the time range first among them.
 *
 * It is deliberately one line. A page used to stack a group eyebrow ("监控")
 * over the title over a description of the page, which repeated what the
 * sidebar and the navigation already said and pushed the content down by three
 * lines. The descriptions now live in the guide.
 */
export function PageHeader({ title, actions }) {
  return (
    <header className="page-header">
      <h1>{title}</h1>
      {actions ? <div className="page-header-actions">{actions}</div> : null}
    </header>
  );
}
