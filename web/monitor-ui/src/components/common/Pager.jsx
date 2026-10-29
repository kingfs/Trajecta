import React, { useEffect, useState } from "react";
import { Button } from "../ui/button";
import { useI18n } from "../../lib/i18n";

/*
 * One pager for every paged list, with a page-number field instead of only the
 * previous/next pair. The traffic list is routinely thousands of pages long
 * (314k traces at 50 a page is 6,288 pages), so walking it two pages at a time
 * is not a navigation control; the field lets the reader jump to the date range
 * they remember. Enter commits, the value is clamped to the served range, and
 * the component gives no state of its own to the page.
 */
export function Pager({ page, totalPages, onChange, className = "" }) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(String(page));

  useEffect(() => {
    setDraft(String(page));
  }, [page]);

  const commit = (event) => {
    event.preventDefault();
    const next = Math.min(Math.max(1, Math.floor(Number(draft) || 1)), Math.max(1, totalPages));
    setDraft(String(next));
    if (next !== page) {
      onChange(next);
    }
  };

  return (
    <div className={`pager ${className}`.trim()} role="group" aria-label={t("common.pagination")}>
      <Button variant="ghost" disabled={page <= 1} onClick={() => onChange(page - 1)}>
        {t("common.previous")}
      </Button>
      <form className="pager-jump" onSubmit={commit}>
        <input
          className="pager-input"
          type="number"
          min="1"
          max={Math.max(1, totalPages)}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={commit}
          aria-label={t("common.pageNumber")}
        />
      </form>
      <span className="pager-label">/ {Math.max(1, totalPages)}</span>
      <Button variant="ghost" disabled={page >= totalPages} onClick={() => onChange(page + 1)}>
        {t("common.next")}
      </Button>
    </div>
  );
}
