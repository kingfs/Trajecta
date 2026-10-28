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
  const needles = [];
  const add = (candidate) => {
    if (candidate && !needles.includes(candidate)) {
      needles.push(candidate);
    }
  };
  add(target);
  if (target.includes("\\")) {
    add(target.replaceAll("\\", "\\\\"));
  }
  // A URL reaches the page through several spellings. The tool-call arguments
  // are JSON, Markdown may HTML-escape the ampersand, and an API URL can be
  // percent-encoded. The index stores the decoded value, so try the spellings a
  // rendered conversation can show instead of giving up on the first miss.
  if (target.includes("&")) {
    if (target.includes("&amp;")) {
      add(target.replaceAll("&amp;", "&"));
    } else {
      add(target.replaceAll("&", "&amp;"));
    }
    add(target.replaceAll("&", "\\u0026"));
    add(target.replaceAll("&", "%26"));
  }
  if (target.includes("%")) {
    try {
      add(decodeURIComponent(target));
    } catch {
      // A bare percent in prose is not an escape; keep the raw needle.
    }
  }
  if (target.includes("&") || target.includes("?") || target.includes("%")) {
    try {
      add(encodeURIComponent(target));
    } catch {
      // encodeURIComponent only throws on a lone surrogate, which the value
      // cannot contain after the server coerced it to valid UTF-8.
    }
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


// The conversation finder marks every occurrence of a typed query, not one
// exact audited value. Browser find semantics are the right model: substring,
// case-insensitive, all matches, original casing preserved in the mark. A
// pathological query in a huge output is capped per block; the count in the
// toolbar comes from the rendered marks, so a truncated block under-reports
// rather than freezing the page.
export const FIND_MATCH_LIMIT = 200;

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * findMatches splits text into alternating plain and matched segments.
 *
 * @param {string} text
 * @param {string} query
 * @returns {{segments: {text: string, hit?: boolean}[], count: number, truncated: boolean} | null}
 */
export function findMatches(text, query) {
  const source = String(text || "");
  const needle = String(query || "").trim();
  if (!source || !needle) {
    return null;
  }
  let pattern;
  try {
    pattern = new RegExp(escapeRegExp(needle), "gi");
  } catch {
    return null;
  }
  const segments = [];
  let last = 0;
  let count = 0;
  let truncated = false;
  let match = pattern.exec(source);
  while (match) {
    if (!match[0]) {
      pattern.lastIndex += 1;
      match = pattern.exec(source);
      continue;
    }
    if (count >= FIND_MATCH_LIMIT) {
      truncated = true;
      break;
    }
    if (match.index > last) {
      segments.push({ text: source.slice(last, match.index) });
    }
    segments.push({ text: match[0], hit: true });
    last = match.index + match[0].length;
    count += 1;
    match = pattern.exec(source);
  }
  if (!count) {
    return null;
  }
  if (last < source.length) {
    segments.push({ text: source.slice(last) });
  }
  return { segments, count, truncated };
}
