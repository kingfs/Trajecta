import React from "react";
import { CircleAlert, Inbox } from "lucide-react";

/*
 * The one "there is nothing here" surface.
 *
 * Two things changed once the console had a rail full of destinations. A panel
 * that reported a failure and a panel that had simply not been filled in looked
 * identical - a sentence in a dashed box - so the eye had to read every one of
 * them to tell "no traffic in this window" from "the API refused". The tone now
 * picks the glyph as well as the colour, which is the same rule the rest of the
 * console follows: colour never carries meaning on its own.
 *
 * The icon is drawn only for the full-height variant. A compact state sits
 * inside a card whose heading already says what is empty, and a glyph there is
 * one more thing between the reader and the next panel.
 *
 * `action` is the way out: an empty state that says "no results" should be able
 * to offer the filter reset that would produce some. It is a slot rather than a
 * callback because the caller owns the handler and the label's translation.
 */
export function EmptyState({ title, detail = "", tone = "default", compact = false, icon, action = null }) {
  const className = [
    "empty-state",
    compact ? "empty-state-inline" : "",
    tone === "danger" ? "empty-state-danger" : "",
  ]
    .filter(Boolean)
    .join(" ");

  const glyph = icon === undefined ? (tone === "danger" ? <CircleAlert size={18} aria-hidden="true" /> : <Inbox size={18} aria-hidden="true" />) : icon;

  return (
    <div className={className}>
      {compact || !glyph ? null : <span className="empty-state-icon">{glyph}</span>}
      <div className="empty-state-body">
        <strong className="empty-state-title">{title}</strong>
        {detail ? <div className="empty-state-detail">{detail}</div> : null}
        {action ? <div className="empty-state-action">{action}</div> : null}
      </div>
    </div>
  );
}
