import React, { useEffect } from "react";
import { useI18n } from "../../lib/i18n";

/*
 * The one header every page renders: the page title, and on the right the
 * controls that act on the whole page - the time range first among them.
 *
 * The title is also the document title. A console is opened in several tabs and
 * every one of them used to be called "trajecta monitor".
 *
 * It replaced a literal "CONTROL PLANE" painted by a CSS `::before` and, for one
 * round, a rail-group label above the title. Both are gone: the rail already
 * says which group the reader is in and marks the entry they are on, so a second
 * name for the same thing above every page title was one more line to read
 * before the thing they came for.
 */
export function PageHeader({ title, actions }) {
  useEffect(() => {
    if (!title) {
      return;
    }
    const previous = document.title;
    document.title = `${title} · Trajecta`;
    return () => {
      document.title = previous;
    };
  }, [title]);

  return (
    <header className="page-header">
      <h1>{title}</h1>
      {actions ? <div className="page-header-actions">{actions}</div> : null}
    </header>
  );
}
