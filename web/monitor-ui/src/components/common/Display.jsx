import React, { useMemo, useState } from "react";
import * as Collapsible from "@radix-ui/react-collapsible";
import { useI18n } from "../../lib/i18n";
import { formatRawNumber } from "../../lib/monitor";

/**
 * A disclosure, owned by Radix Collapsible.
 *
 * It was a `useState` and a plain button, which left two things missing: the
 * trigger reported no `aria-expanded`, so a screen reader could not tell whether
 * the section in front of it was open, and it never named the section it opens.
 * The label beside it was the literal string "hide"/"show" in a UI that ships
 * two languages. Radix derives the state attribute from the root, generates the
 * content id, and names it through `aria-controls` while the content is mounted,
 * which is the only time that id resolves. The label is a dictionary entry now.
 */
export function CollapsibleCard({ title, subtitle, defaultOpen = false, children, bodyClassName = "" }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(defaultOpen);

  return (
    <Collapsible.Root asChild open={open} onOpenChange={setOpen}>
      <section className="collapse-card">
        <Collapsible.Trigger asChild>
          <button className="collapse-head" type="button">
            <div>
              <strong>{title}</strong>
              {subtitle ? <span>{subtitle}</span> : null}
            </div>
            <span>{open ? t("common.hide") : t("common.show")}</span>
          </button>
        </Collapsible.Trigger>
        <Collapsible.Content asChild>
          <div className={`collapse-body ${bodyClassName}`.trim()}>{children}</div>
        </Collapsible.Content>
      </section>
    </Collapsible.Root>
  );
}

export function StatCard({ label, value, accent = "", detail = "", mono = false, title = "" }) {
  return (
    <article className={`stat-card ${accent}`.trim()} title={title || rawValueTitle(value)}>
      <span>{label}</span>
      <strong className={mono ? "mono" : ""}>{value}</strong>
      {detail ? <small className={mono ? "mono stat-detail" : "stat-detail"}>{detail}</small> : null}
    </article>
  );
}

function rawValueTitle(value) {
  if (typeof value === "number") {
    return formatRawNumber(value);
  }
  return "";
}

export function CodeBlock({ value }) {
  return <pre className="code-block">{value}</pre>;
}

export function MessageContent({ value, format, renderMarkdown, className = "", collapsedLines = 10, forceExpanded = false }) {
  const [expanded, setExpanded] = useState(false);
  // `forceExpanded` is how a caller that jumped to this content - the trace
  // page's conversation, landing on a finding's anchor - shows it open without
  // owning the disclosure state: a clamped block would hide the very sentence
  // the link was about.
  const collapsible = useMemo(() => shouldCollapseContent(value, collapsedLines) && !forceExpanded, [value, collapsedLines, forceExpanded]);
  const bodyClassName = [
    className,
    "message-content",
    collapsible && !expanded ? "message-content-collapsed" : "",
  ].filter(Boolean).join(" ");

  const showAll = expanded || forceExpanded;
  if (renderMarkdown && format === "markdown") {
    return (
      <ExpandableContent expanded={showAll} collapsible={collapsible} onToggle={() => setExpanded((current) => !current)}>
        <MarkdownContent value={value} className={bodyClassName} />
      </ExpandableContent>
    );
  }
  return (
    <ExpandableContent expanded={showAll} collapsible={collapsible} onToggle={() => setExpanded((current) => !current)}>
      <div className={`${bodyClassName} prose-block`.trim()}>{value}</div>
    </ExpandableContent>
  );
}

function ExpandableContent({ expanded, collapsible, onToggle, children }) {
  return (
    <div className={collapsible ? "message-content-wrap message-content-wrap-collapsible" : "message-content-wrap"}>
      {children}
      {collapsible ? (
        <button className="message-expand-button" type="button" onClick={onToggle}>
          {expanded ? "Show less" : "Show all"}
        </button>
      ) : null}
    </div>
  );
}

function shouldCollapseContent(value, collapsedLines) {
  const text = String(value || "");
  if (!text.trim()) {
    return false;
  }
  const lineCount = text.split(/\r\n|\r|\n/).length;
  return lineCount > collapsedLines || text.length > 900;
}

/*
 * Markdown is a large dependency that only the trace detail page reaches, so it
 * is loaded on demand and the plain text is shown while it arrives. The fallback
 * uses the same classes, so a slow chunk is a moment of unstyled text rather than
 * an empty box.
 */
const MarkdownBlock = React.lazy(() => import("./MarkdownBlock"));

function MarkdownContent({ value, className = "" }) {
  return (
    <React.Suspense fallback={<div className={`${className} prose-block`.trim()}>{value}</div>}>
      <MarkdownBlock value={value} className={className} />
    </React.Suspense>
  );
}
