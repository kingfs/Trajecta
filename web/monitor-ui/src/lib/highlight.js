/*
 * Marking the exact bytes a resource row stands for inside already-rendered
 * text.
 *
 * The trace conversation renders a message as Markdown or as plain text; the
 * resource locate action passes the value through the URL as `hl`. Splitting the
 * source string with a regular expression would have to re-implement escaping
 * for both paths, so the value is located in the final text instead: a plain
 * block splits the string into React nodes, and the Markdown block splits the
 * text nodes of the parsed tree. Both ask this module where the value is.
 *
 * The index normalizes Windows separators, while the conversation may still show
 * the JSON-escaped spelling of the same path, so both spellings are tried. The
 * value must also stand on its own: a stored /a/main.go must not mark the prefix
 * of /a/main.go.bak, which is a different file.
 */

// A value repeated more often than this is a pattern in the prose rather than a
// position worth marking; marking every occurrence would turn the block into a
// highlight.
const MAX_MATCHES = 12;

// The bytes a path or URL continues with. A match touching one of these on
// either side is part of a longer value.
const CONTINUATION = /[0-9A-Za-z._/\\-]/;

export function highlightNeedles(value) {
  const target = String(value || "");
  if (!target) {
    return [];
  }
  const needles = [target];
  if (target.includes("\\")) {
    needles.push(target.replaceAll("\\", "\\\\"));
  }
  return needles;
}

function matchPositions(source, needle) {
  const positions = [];
  let index = source.indexOf(needle);
  while (index >= 0) {
    const before = index > 0 ? source[index - 1] : "";
    const afterIndex = index + needle.length;
    const after = afterIndex < source.length ? source[afterIndex] : "";
    if (!CONTINUATION.test(before) && !CONTINUATION.test(after)) {
      positions.push(index);
    }
    index = source.indexOf(needle, index + 1);
  }
  return positions;
}

/**
 * findHighlight returns the needle that occurs in text and the pieces around it,
 * or null when nothing matches. Only standalone occurrences count.
 *
 * @param {string} text
 * @param {string} value
 * @returns {{needle: string, parts: string[]} | null}
 */
export function findHighlight(text, value) {
  const source = String(text || "");
  if (!source) {
    return null;
  }
  for (const needle of highlightNeedles(value)) {
    const positions = matchPositions(source, needle);
    if (!positions.length) {
      continue;
    }
    if (positions.length > MAX_MATCHES) {
      return null;
    }
    const parts = [];
    let cursor = 0;
    for (const position of positions) {
      parts.push(source.slice(cursor, position));
      cursor = position + needle.length;
    }
    parts.push(source.slice(cursor));
    return { needle, parts };
  }
  return null;
}
