import React, { useMemo, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Dialog, DialogClose, DialogContent, DialogHeader, DialogTitle } from "../ui/dialog";
import { Button } from "../ui/button";
import { InlineTag } from "../common/Badges";
import { buildPathDocument, childJSONPath, describeJSONValue, joinJSONPath, parseNodePayload, previewJSONValue } from "../../lib/jsonTree";

/*
 * The parsed half of a trace, as a tree.
 *
 * It replaced a recursive renderer that opened every node it had: a request of
 * forty messages drew all forty, each with its own raw JSON block, and the
 * response half of the page was a search problem rather than a section. Here a
 * container is one row until it is opened, and the row carries the parser's name
 * for the value when it had one.
 *
 * Clicking a row opens the value in a dialog, which is how a long string - a
 * whole assistant message, the arguments of a call - is read without widening
 * the column it sits in.
 */

const TOOL_CALL_TYPES = new Set(["tool_call", "server_tool_call", "tool_call_delta"]);
const TOOL_RESULT_TYPES = new Set(["tool_result", "server_tool_result"]);
const FAILURE_TYPES = new Set(["error", "refusal", "safety"]);

function labelTone(label) {
  if (FAILURE_TYPES.has(label)) {
    return "danger";
  }
  if (TOOL_CALL_TYPES.has(label)) {
    return "gold";
  }
  if (TOOL_RESULT_TYPES.has(label)) {
    return "green";
  }
  if (label === "reasoning") {
    return "accent";
  }
  return "default";
}

export function ProtocolTree({ nodes = [], t }) {
  const { document, nodesByPath, unplaced, rowPreviews } = useMemo(() => buildPathDocument(nodes), [nodes]);
  const [selected, setSelected] = useState(null);
  const entries = Object.entries(document);
  // The root is open and everything under it is closed: the reader sees the
  // shape of the payload - the keys the parser kept - without the page growing
  // by a row per message.
  const rootPath = "$";

  return (
    <>
      <div className="json-tree">
        {entries.map(([key, value]) => (
          <JSONNode
            key={key}
            name={key}
            value={value}
            path={joinJSONPath(rootPath, key)}
            depth={0}
            nodesByPath={nodesByPath}
            rowPreviews={rowPreviews}
            onSelect={setSelected}
            t={t}
          />
        ))}
        {/* A node whose path is not a JSON path - an entry-payload root, or a
            shape a parser invented - still has to be readable. */}
        {unplaced.map(({ node, payload }) => (
          <article className="json-node json-node-detached" key={node.id || node.path}>
            <button type="button" className="json-node-main" onClick={() => setSelected({ node, path: node.path, name: node.path, value: payload.ok ? payload.value : payload.text, text: payload.ok ? "" : payload.text })}>
              <span className="json-node-key mono">{node.path || node.id}</span>
              <span className="json-node-value">{previewJSONValue(payload.ok ? payload.value : payload.text)}</span>
              <NodeLabel node={node} value={payload.ok ? payload.value : payload.text} t={t} />
            </button>
          </article>
        ))}
      </div>
      {selected ? <ValueDialog selection={selected} onClose={() => setSelected(null)} onSelect={setSelected} nodesByPath={nodesByPath} rowPreviews={rowPreviews} t={t} /> : null}
    </>
  );
}

function JSONNode({ name, value, path, depth, nodesByPath, rowPreviews, onSelect, t }) {
  const container = typeof value === "object" && value !== null;
  const [open, setOpen] = useState(false);
  const node = nodesByPath.get(path);
  const children = container ? Object.entries(value) : [];
  // The parser's own sentence for this value when it has one, and the shape of
  // the value otherwise.
  const shaped = previewJSONValue(value);
  const text = rowPreviews?.get(path) || "";
  const preview = text || shaped;

  return (
    <div className="json-node" data-depth={depth}>
      <div className="json-node-row">
        {container ? (
          <button
            type="button"
            className="json-node-toggle"
            aria-expanded={open}
            aria-label={t(open ? "traceDetail.collapseNode" : "traceDetail.expandNode", { name })}
            onClick={() => setOpen((current) => !current)}
          >
            {open ? <ChevronDown size={13} aria-hidden="true" /> : <ChevronRight size={13} aria-hidden="true" />}
          </button>
        ) : (
          <span className="json-node-toggle json-node-toggle-leaf" aria-hidden="true" />
        )}
        <button
          type="button"
          className="json-node-main"
          // The parser's own name for the value is the thing a reader came for,
          // and the modal is where the text behind a preview is read in full.
          title={text ? `${path} · ${shaped}` : path}
          onClick={() => onSelect({ node, path, name, value })}
        >
          <span className="json-node-key mono">{name}</span>
          <span className="json-node-value">{preview}</span>
          <NodeLabel node={node} value={value} t={t} />
        </button>
      </div>
      {container && open ? (
        <div className="json-node-children">
          {children.map(([key, child]) => (
            <JSONNode
              key={key}
              name={key}
              value={child}
              path={childJSONPath(path, value, key)}
              depth={depth + 1}
              nodesByPath={nodesByPath}
              rowPreviews={rowPreviews}
              onSelect={onSelect}
              t={t}
            />
          ))}
        </div>
      ) : null}
    </div>
  );
}

function NodeLabel({ node, value, t }) {
  if (!node) {
    return null;
  }
  const { label } = describeJSONValue(node, value);
  const role = node.role ? ` · ${node.role}` : "";
  return (
    <span className="json-node-tags">
      <InlineTag tone={labelTone(label)}>{label}{role}</InlineTag>
    </span>
  );
}

function ValueDialog({ selection, onClose, onSelect, nodesByPath, rowPreviews, t }) {
  const { value, node, path } = selection;
  const payload = parseNodePayload(node?.raw);
  // An error body the parser could not read as JSON is shown as what it is.
  const text = !payload.ok && payload.text ? payload.text : "";
  const container = typeof value === "object" && value !== null;

  return (
    <Dialog open onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="tool-modal" aria-describedby={undefined}>
        <DialogHeader className="tool-modal-head">
          <div>
            <p className="eyebrow">{t("traceDetail.jsonValue")}</p>
            {/* The path is the identity of the value: it is what the reader
                clicked, what the parser named, and what a finding cites. */}
            <DialogTitle className="mono">{path}</DialogTitle>
          </div>
          <DialogClose asChild>
            <Button variant="default" size="icon" aria-label={t("traceDetail.closeJsonValue")}>
              <span className="tool-modal-close">x</span>
            </Button>
          </DialogClose>
        </DialogHeader>
        {node ? (
          <div className="trace-tag-group">
            {node.normalized_type ? <InlineTag tone={labelTone(node.normalized_type)}>{node.normalized_type}</InlineTag> : null}
            {node.provider_type && node.provider_type !== node.normalized_type ? <InlineTag>{node.provider_type}</InlineTag> : null}
            {node.role ? <InlineTag>{node.role}</InlineTag> : null}
          </div>
        ) : null}
        {text ? (
          <pre className="json-value-text">{text}</pre>
        ) : container ? (
          <div className="json-tree json-tree-in-modal">
            {Object.entries(value).map(([key, child]) => (
              <JSONNode
                key={key}
                name={key}
                value={child}
                path={childJSONPath(path, value, key)}
                depth={0}
                nodesByPath={nodesByPath}
                rowPreviews={rowPreviews}
                // Drilling further stays in the dialog rather than closing it
                // and looking for the same value in the page behind it.
                onSelect={onSelect}
                t={t}
              />
            ))}
          </div>
        ) : (
          <pre className="json-value-text">{typeof value === "string" ? value : JSON.stringify(value, null, 2)}</pre>
        )}
      </DialogContent>
    </Dialog>
  );
}
