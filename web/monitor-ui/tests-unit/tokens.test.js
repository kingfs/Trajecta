import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { contrastRatio } from "./color-math.js";

/*
 * The palette is generated from published scales, but "generated from a good
 * source" is not the same as "readable". These tests read the shipped
 * tokens.css and check the relationships that matter, so a token that is later
 * hand-edited, or a ramp that gets bumped to a version with different steps,
 * fails here instead of in someone's eyes.
 */

const css = readFileSync(new URL("../src/styles/tokens.css", import.meta.url), "utf8");

/** The declarations of one top-level rule block, whose selector contains `marker`. */
function blockFor(marker) {
  const start = css.indexOf(marker);
  assert.notEqual(start, -1, `no block for ${marker}`);
  const open = css.indexOf("{", start);
  const close = css.indexOf("\n}", open);
  const body = css.slice(open + 1, close);
  const out = new Map();
  for (const line of body.split("\n")) {
    const m = /^\s*--([a-z0-9-]+):\s*(.+?);\s*$/.exec(line);
    if (m) out.set(m[1], m[2]);
  }
  return out;
}

const themes = {
  dark: blockFor(':root[data-theme="dark"]'),
  light: blockFor(':root[data-theme="light"]'),
};

/** Follow `var(--x)` chains and `color-mix(..., var(--x) N%, transparent)`. */
function resolve(tokens, name, depth = 0) {
  assert.ok(depth < 10, `var() cycle at --${name}`);
  const raw = tokens.get(name);
  assert.ok(raw !== undefined, `--${name} is not defined`);
  const direct = /^var\(--([a-z0-9-]+)\)$/.exec(raw);
  if (direct) return resolve(tokens, direct[1], depth + 1);
  return raw.replace(/var\(--([a-z0-9-]+)\)/g, (_, ref) => resolve(tokens, ref, depth + 1));
}

const SURFACES = ["bg-canvas", "surface-1", "surface-2", "surface-3", "surface-inset"];
const HUES = ["accent", "success", "warning", "danger", "info", "violet"];

for (const [theme, tokens] of Object.entries(themes)) {
  test(`${theme}: body text clears WCAG AA on every surface`, () => {
    for (const surface of SURFACES) {
      for (const [text, minimum] of [
        ["text-primary", 4.5],
        ["text-secondary", 4.5],
        // The third level is for de-emphasised labels; it is held to the same
        // bar as body text rather than to the large-text bar, because the
        // labels that use it are 11px.
        ["text-tertiary", 4.5],
      ]) {
        const ratio = contrastRatio(resolve(tokens, text), resolve(tokens, surface));
        assert.ok(
          ratio >= minimum,
          `${theme}: --${text} on --${surface} is ${ratio.toFixed(2)}:1, needs ${minimum}:1`,
        );
      }
    }
  });

  test(`${theme}: status colours are readable as text and as a tint`, () => {
    for (const hue of HUES) {
      // Used for icons, inline values and chart strokes, so it has to work on
      // the plain page and on a panel.
      for (const surface of ["bg-canvas", "surface-1"]) {
        const ratio = contrastRatio(resolve(tokens, hue), resolve(tokens, surface));
        assert.ok(ratio >= 4.5, `${theme}: --${hue} on --${surface} is ${ratio.toFixed(2)}:1, needs 4.5:1`);
      }
      // The badge recipe: a tinted background with the hue's high-contrast step
      // as the label.
      const onTint = contrastRatio(resolve(tokens, `${hue}-strong`), resolve(tokens, `${hue}-soft`));
      assert.ok(onTint >= 4.5, `${theme}: --${hue}-strong on --${hue}-soft is ${onTint.toFixed(2)}:1, needs 4.5:1`);
    }
  });

  test(`${theme}: solid fills are distinguishable from their surface and their label`, () => {
    for (const hue of HUES) {
      const onSolid = contrastRatio(resolve(tokens, `on-${hue}`), resolve(tokens, `${hue}-solid`));
      // 3:1 is the non-text / large-text bar. Step 9 is the scale's solid step;
      // amber is light by design, which is why --on-warning is its scale's dark
      // step 12 rather than white.
      assert.ok(onSolid >= 3, `${theme}: --on-${hue} on --${hue}-solid is ${onSolid.toFixed(2)}:1, needs 3:1`);
      // A solid fill has to be visible against the surface it sits on.
      const vsSurface = contrastRatio(resolve(tokens, `${hue}-solid`), resolve(tokens, "surface-1"));
      assert.ok(vsSurface >= 1.2, `${theme}: --${hue}-solid on --surface-1 is ${vsSurface.toFixed(2)}:1`);
    }
  });

  test(`${theme}: the three border weights are ordered and visible`, () => {
    const canvas = resolve(tokens, "bg-canvas");
    const ratios = ["border-subtle", "border", "border-strong"].map((b) => contrastRatio(resolve(tokens, b), canvas));
    assert.ok(ratios[0] < ratios[1] && ratios[1] < ratios[2], `${theme}: borders are not ordered: ${ratios}`);
  });

  test(`${theme}: the inverted pair is readable and the scrim is a real colour`, () => {
    const inverted = contrastRatio(resolve(tokens, "on-inverse"), resolve(tokens, "surface-inverse"));
    assert.ok(inverted >= 4.5, `${theme}: --on-inverse on --surface-inverse is ${inverted.toFixed(2)}:1`);
    // The scrim is composited over the page, so it only has to parse and to
    // darken what is behind it.
    const scrim = resolve(tokens, "scrim");
    assert.ok(contrastRatio(scrim, resolve(tokens, "bg-canvas")) > 1.05, `${theme}: --scrim does not darken the page`);
  });

  test(`${theme}: the surface ramp is monotonic`, () => {
    // A raised surface must be lighter than the canvas in dark and darker in
    // light; a ramp that reverses somewhere reads as a rendering bug.
    const surfaces = SURFACES.map((s) => contrastRatio(resolve(tokens, s), resolve(tokens, "bg-canvas")));
    const outside = surfaces.filter((r) => r < 1.0);
    assert.equal(outside.length, 0, `${theme}: a surface is darker than the canvas: ${surfaces}`);
  });
}
