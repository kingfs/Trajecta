/**
 * Copy text to the clipboard, on a secure context and off it.
 *
 * The async Clipboard API only exists on a secure context. A Monitor reached
 * over plain HTTP on a LAN address - which is how a test deployment is normally
 * opened - has no `navigator.clipboard` at all, so a button that only calls
 * `writeText` silently does nothing there. The legacy `document.execCommand`
 * over a temporary selection still works on those origins, and when even that
 * is refused the caller is told, so it can leave the text selected for a manual
 * Ctrl+C.
 *
 * Resolves to true only when the value actually reached the clipboard.
 */
export async function copyText(value) {
  const text = String(value ?? "");
  if (!text) {
    return false;
  }

  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // A denied permission or an origin the browser will not treat as secure:
      // fall through to the legacy path rather than reporting a false success.
    }
  }

  if (typeof document === "undefined" || typeof document.execCommand !== "function") {
    return false;
  }
  return legacyCopy(text);
}

function legacyCopy(text) {
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  // Off-screen rather than hidden: `display: none` and `visibility: hidden`
  // both drop the selection the copy command reads from.
  area.style.position = "fixed";
  area.style.top = "0";
  area.style.left = "-9999px";
  document.body.appendChild(area);

  const selection = typeof document.getSelection === "function" ? document.getSelection() : null;
  const previous = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;
  try {
    area.select();
    area.setSelectionRange(0, area.value.length);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.body.removeChild(area);
    if (previous && selection) {
      selection.removeAllRanges();
      selection.addRange(previous);
    }
  }
}
