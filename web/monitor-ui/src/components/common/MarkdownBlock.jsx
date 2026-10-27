import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { findHighlight } from "../../lib/highlight";

/*
 * Markdown rendering, on react-markdown + remark-gfm.
 *
 * It replaces a ~70-line hand-written renderer that escaped the source and then
 * applied regular expressions to the escaped string. That approach cannot be
 * made correct: inline formatting was applied after code spans were extracted,
 * so `**x**` inside backticks came out bold, and every construct outside the
 * seven it knew - tables, task lists, strikethrough, nested lists - arrived as
 * literal punctuation. This parses to an AST instead, renders real React
 * elements, and needs no `dangerouslySetInnerHTML`.
 *
 * It is a separate module because it is a large dependency for something only
 * the trace detail page uses, and it is loaded through `React.lazy` so it stays
 * out of the entry chunk.
 *
 * The wrapper keeps `.prose-block.rendered-markdown`, which is what the existing
 * stylesheet targets, and `pre` keeps `.md-pre`. Raw HTML in the source is not
 * rendered: react-markdown ignores it without a rehype plugin, and its default
 * `urlTransform` drops `javascript:` and the other unsafe link protocols, so the
 * escaping the hand-written version did by hand is structural here.
 */
const REMARK_PLUGINS = [remarkGfm];

/*
 * The resource locate action marks its value inside rendered Markdown without
 * touching the source: the tree is already parsed, so a text node is where the
 * value can be split into text + <mark> + text and nothing else changes. Splitting
 * the Markdown source first would break emphasis and code spans.
 */
function rehypeHighlight(options = {}) {
  const value = options.value || "";
  return (tree) => {
    const visit = (node) => {
      if (!node || !Array.isArray(node.children)) {
        return;
      }
      const next = [];
      for (const child of node.children) {
        if (child.type === "text") {
          next.push(...highlightTextNode(child, value));
        } else {
          visit(child);
          next.push(child);
        }
      }
      node.children = next;
    };
    visit(tree);
  };
}

function highlightTextNode(node, value) {
  const found = findHighlight(node.value, value);
  if (!found) {
    return [node];
  }
  const out = [];
  found.parts.forEach((piece, index) => {
    if (index > 0) {
      out.push({
        type: "element",
        tagName: "mark",
        properties: { className: ["content-hit"] },
        children: [{ type: "text", value: found.needle }],
      });
    }
    if (piece) {
      out.push({ type: "text", value: piece });
    }
  });
  return out;
}

const COMPONENTS = {
  pre: ({ node, className, ...props }) => <pre className={className ? `md-pre ${className}` : "md-pre"} {...props} />,
  a: ({ node, ...props }) => <a target="_blank" rel="noreferrer" {...props} />,
};

export default function MarkdownBlock({ value = "", className = "", highlight = "" }) {
  const rehypePlugins = highlight ? [[rehypeHighlight, { value: highlight }]] : [];
  return (
    <div className={`${className} prose-block rendered-markdown`.trim()}>
      <ReactMarkdown remarkPlugins={REMARK_PLUGINS} rehypePlugins={rehypePlugins} components={COMPONENTS}>
        {String(value || "")}
      </ReactMarkdown>
    </div>
  );
}
