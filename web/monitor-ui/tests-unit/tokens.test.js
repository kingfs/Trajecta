import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { contrastRatio } from "./color-math.js";

/*
 * The palette is built from published scales, but "generated from a good
 * source" is not the same as "readable". These tests read the shipped
 * tokens.css together with the scales it imports and check the relationships
 * that matter, so a token that is later hand-edited, or a ramp that gets bumped
 * to a version with different steps, fails here instead of in someone's eyes.
 *
 * The colour scales live in @radix-ui/colors rather than in tokens.css, so the
 * two have to be merged the way the browser merges them: `-dark` files are
 * scoped to `.dark`, which lib/theme.ts puts on <html> beside `data-theme`, and
 * the light block in tokens.css wins over the base `:root` block.
 */

const css = readFileSync(new URL("../src/styles/tokens.css", import.meta.url), "utf8");

/**
 * The declarations of one rule block. `marker` finds the selector; `after` is
 * an optional declaration that has to be inside the block, which is how the
 * second `:root` block is picked rather than the typography one.
 */
function blockFor(text, marker, after) {
  const start = after ? text.lastIndexOf(marker, text.indexOf(after)) : text.indexOf(marker);
  assert.notEqual(start, -1, `no block for ${marker}`);
  const open = text.indexOf("{", start);
  const close = text.indexOf("\n}", open);
  const body = text.slice(open + 1, close);
  const out = new Map();
  for (const line of body.split("\n")) {
    const m = /^\s*--([a-z0-9-]+):\s*(.+?);\s*$/.exec(line);
    if (m) out.set(m[1], m[2]);
  }
  return out;
}

/**
 * The hex declarations of a scale file. Every Radix file repeats its variables
 * inside an `@supports (color: color(display-p3 ...))` block, and the tests are
 * written against sRGB, so the first declaration of each name is the one kept.
 */
function scaleVariables(file) {
  const out = new Map();
  for (const line of readFileSync(new URL(`../node_modules/@radix-ui/colors/${file}`, import.meta.url), "utf8").split("\n")) {
    const m = /^\s*--([a-z0-9-]+):\s*(#[0-9a-f]{3,8});\s*$/i.exec(line);
    if (m && !out.has(m[1])) out.set(m[1], m[2]);
  }
  return out;
}

const SCALES = ["slate", "blue", "green", "amber", "red", "cyan", "violet"];
const scaleFiles = (suffix) => SCALES.map((scale) => scaleVariables(`${scale}${suffix}.css`));

const root = blockFor(css, ":root {", "--bg-canvas");
const light = blockFor(css, ':root[data-theme="light"] {');

/** The variables in scope for one theme, merged in cascade order. */
function theme(block, dark) {
  const merged = new Map();
  for (const map of dark ? scaleFiles("-dark") : scaleFiles("")) for (const [k, v] of map) merged.set(k, v);
  for (const [k, v] of root) merged.set(k, v);
  for (const [k, v] of block) merged.set(k, v);
  return merged;
}

const themes = {
  dark: theme(new Map(), true),
  light: theme(light, false),
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

for (const [name, tokens] of Object.entries(themes)) {
  test(`${name}: every semantic colour resolves to a real value`, () => {
    for (const surface of SURFACES) {
      assert.match(resolve(tokens, surface), /^(#|rgb|oklch|color-mix)/, `--${surface} did not resolve`);
    }
    // A scale that was not imported at all would resolve to the literal `var()`
    // text, which is the failure this catches.
    for (const hue of HUES) {
      for (const step of ["", "-solid", "-soft", "-border", "-strong"]) {
        const value = resolve(tokens, `${hue}${step}`);
        assert.doesNotMatch(value, /var\(/, `--${hue}${step} is an unresolved var(): ${value}`);
      }
    }
  });

  test(`${name}: body text clears WCAG AA on every surface`, () => {
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
          `${name}: --${text} on --${surface} is ${ratio.toFixed(2)}:1, needs ${minimum}:1`,
        );
      }
    }
  });

  test(`${name}: status colours are readable as text and as a tint`, () => {
    for (const hue of HUES) {
      // Used for icons, inline values and chart strokes, so it has to work on
      // the plain page and on a panel.
      for (const surface of ["bg-canvas", "surface-1"]) {
        const ratio = contrastRatio(resolve(tokens, hue), resolve(tokens, surface));
        assert.ok(ratio >= 4.5, `${name}: --${hue} on --${surface} is ${ratio.toFixed(2)}:1, needs 4.5:1`);
      }
      // The badge recipe: a tinted background with the hue's high-contrast step
      // as the label.
      const onTint = contrastRatio(resolve(tokens, `${hue}-strong`), resolve(tokens, `${hue}-soft`));
      assert.ok(onTint >= 4.5, `${name}: --${hue}-strong on --${hue}-soft is ${onTint.toFixed(2)}:1, needs 4.5:1`);
    }
  });

  test(`${name}: solid fills are distinguishable from their surface and their label`, () => {
    for (const hue of HUES) {
      const onSolid = contrastRatio(resolve(tokens, `on-${hue}`), resolve(tokens, `${hue}-solid`));
      // 3:1 is the non-text / large-text bar. Step 9 is the scale's solid step;
      // amber and cyan are light by design, which is why their ink is a fixed
      // dark rather than a step that flips with the theme.
      assert.ok(onSolid >= 3, `${name}: --on-${hue} on --${hue}-solid is ${onSolid.toFixed(2)}:1, needs 3:1`);
      // A solid fill has to be visible against the surface it sits on.
      const vsSurface = contrastRatio(resolve(tokens, `${hue}-solid`), resolve(tokens, "surface-1"));
      assert.ok(vsSurface >= 1.2, `${name}: --${hue}-solid on --surface-1 is ${vsSurface.toFixed(2)}:1`);
    }
  });

  test(`${name}: the three border weights are ordered and visible`, () => {
    const canvas = resolve(tokens, "bg-canvas");
    const ratios = ["border-subtle", "border", "border-strong"].map((b) => contrastRatio(resolve(tokens, b), canvas));
    assert.ok(ratios[0] < ratios[1] && ratios[1] < ratios[2], `${name}: borders are not ordered: ${ratios}`);
    // The subtle hairline is the one that disappeared in the old light theme:
    // 9% black on a white card is a contrast ratio of about 1.1, which is why
    // light mode read as one flat sheet. It has to be a visible step now.
    assert.ok(ratios[0] >= 1.2, `${name}: --border-subtle on the canvas is ${ratios[0].toFixed(2)}:1, needs 1.2:1`);
  });

  test(`${name}: the inverted pair is readable and the scrim is a real colour`, () => {
    const inverted = contrastRatio(resolve(tokens, "on-inverse"), resolve(tokens, "surface-inverse"));
    assert.ok(inverted >= 4.5, `${name}: --on-inverse on --surface-inverse is ${inverted.toFixed(2)}:1`);
    // The scrim is composited over the page, so it only has to parse and to
    // darken what is behind it.
    const scrim = resolve(tokens, "scrim");
    assert.ok(contrastRatio(scrim, resolve(tokens, "bg-canvas")) > 1.05, `${name}: --scrim does not darken the page`);
  });

  test(`${name}: every surface is a visible step from the one it sits on`, () => {
    // Direction is not asserted, because it differs by theme on purpose: a
    // light theme sunken its controls into a white card, a dark theme lifts
    // them off it. What has to hold in both is that the step is visible, and
    // that a raised surface is never the same colour as the canvas.
    const canvasStep = contrastRatio(resolve(tokens, "surface-1"), resolve(tokens, "bg-canvas"));
    assert.ok(canvasStep >= 1.05, `${name}: --surface-1 on the canvas is ${canvasStep.toFixed(3)}:1`);
    const steps = [
      ["surface-2", "surface-1"],
      ["surface-3", "surface-2"],
      ["surface-inset", "surface-1"],
    ];
    for (const [above, below] of steps) {
      const ratio = contrastRatio(resolve(tokens, above), resolve(tokens, below));
      assert.ok(ratio >= 1.05, `${name}: --${above} on --${below} is ${ratio.toFixed(3)}:1, needs 1.05:1`);
    }
  });
}
