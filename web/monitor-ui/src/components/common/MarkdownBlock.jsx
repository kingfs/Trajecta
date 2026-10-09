import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

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

const COMPONENTS = {
  pre: ({ node, className, ...props }) => <pre className={className ? `md-pre ${className}` : "md-pre"} {...props} />,
  a: ({ node, ...props }) => <a target="_blank" rel="noreferrer" {...props} />,
};

export default function MarkdownBlock({ value = "", className = "" }) {
  return (
    <div className={`${className} prose-block rendered-markdown`.trim()}>
      <ReactMarkdown remarkPlugins={REMARK_PLUGINS} components={COMPONENTS}>
        {String(value || "")}
      </ReactMarkdown>
    </div>
  );
}
