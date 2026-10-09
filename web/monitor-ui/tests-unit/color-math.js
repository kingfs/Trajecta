/*
 * Colour maths for the token tests: CSS Colour 4 `oklch()` to sRGB, and
 * compositing a translucent colour over a background.
 *
 * The tokens are asserted on numbers rather than eyeballed, because "is this
 * grey readable on that grey" is exactly the kind of question that gets a
 * confident wrong answer in a pull request.
 */

/** @param {number} t @returns {number} */
const toLinear = (t) => (t <= 0.04045 ? t / 12.92 : ((t + 0.055) / 1.055) ** 2.4);
/** @param {number} t @returns {number} */
const toGamma = (t) => (t <= 0.0031308 ? t * 12.92 : 1.055 * t ** (1 / 2.4) - 0.055);

/**
 * Parse `oklch(L C H)` or `oklch(L C H / A)`.
 * @param {string} value
 * @returns {{ l: number, c: number, h: number, alpha: number }}
 */
export function parseOklch(value) {
  const match = /^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.-]+)\s*(?:\/\s*([\d.]+%?)\s*)?\)$/.exec(value.trim());
  if (!match) throw new Error(`not an oklch() colour: ${value}`);
  const alpha = match[4] === undefined ? 1 : match[4].endsWith("%") ? parseFloat(match[4]) / 100 : parseFloat(match[4]);
  return { l: parseFloat(match[1]), c: parseFloat(match[2]), h: parseFloat(match[3]), alpha };
}

/** OKLCH to linear-light sRGB, which is the space the maths has to happen in. */
export function oklchToLinearRgb({ l, c, h }) {
  const hr = (h * Math.PI) / 180;
  const a = c * Math.cos(hr);
  const b = c * Math.sin(hr);
  const lp = l + 0.3963377774 * a + 0.2158037573 * b;
  const mp = l - 0.1055613458 * a - 0.0638541728 * b;
  const sp = l - 0.0894841775 * a - 1.291485548 * b;
  const l3 = lp ** 3;
  const m3 = mp ** 3;
  const s3 = sp ** 3;
  return [
    +4.0767416621 * l3 - 3.3077115913 * m3 + 0.2309699292 * s3,
    -1.2684380046 * l3 + 2.6097574011 * m3 - 0.3413193965 * s3,
    -0.0041960863 * l3 - 0.7034186147 * m3 + 1.707614701 * s3,
  ];
}

/** Gamma sRGB to OKLab, so a `color-mix(in oklab, ...)` can be evaluated. */
export function rgbToOklab([r, g, b]) {
  const [lr, lg, lb] = [r, g, b].map(toLinear);
  const l = Math.cbrt(0.4122214708 * lr + 0.5363325363 * lg + 0.0514459929 * lb);
  const m = Math.cbrt(0.2119034982 * lr + 0.6806995451 * lg + 0.1073969566 * lb);
  const s = Math.cbrt(0.0883024619 * lr + 0.2817188376 * lg + 0.6299787005 * lb);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}

/** OKLab back to gamma sRGB. */
export function oklabToRgb([l, a, b]) {
  const lp = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const mp = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const sp = (l - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return [
    +4.0767416621 * lp - 3.3077115913 * mp + 0.2309699292 * sp,
    -1.2684380046 * lp + 2.6097574011 * mp - 0.3413193965 * sp,
    -0.0041960863 * lp - 0.7034186147 * mp + 1.707614701 * sp,
  ].map((x) => Math.min(1, Math.max(0, toGamma(x))));
}

/**
 * Split `color-mix(in <space>, <colour> <pct>%, <colour> <pct>%)`, including the
 * single-operand `..., transparent)` form, and evaluate it.
 */
function parseColorMix(value) {
  const space = /^color-mix\(in\s+(srgb|oklab)\s*,/.exec(value)[1];
  const body = value.slice(value.indexOf(",") + 1, -1);
  const parts = [];
  let depth = 0;
  let current = "";
  for (const char of body) {
    if (char === "(") depth += 1;
    if (char === ")") depth -= 1;
    if (char === "," && depth === 0) {
      parts.push(current);
      current = "";
    } else {
      current += char;
    }
  }
  parts.push(current);

  const operands = parts.map((part) => {
    const trimmed = part.trim();
    if (trimmed === "transparent") return { rgb: [0, 0, 0], alpha: 0, weight: null };
    const match = /^(.*?)\s+([\d.]+)%$/.exec(trimmed);
    const colour = parseColor(match ? match[1] : trimmed);
    return { ...colour, weight: match ? parseFloat(match[2]) / 100 : null };
  });
  // An omitted percentage takes whatever is left over.
  const specified = operands.reduce((sum, o) => sum + (o.weight ?? 0), 0);
  const missing = operands.filter((o) => o.weight === null).length;
  for (const operand of operands) if (operand.weight === null) operand.weight = (1 - specified) / missing;

  const convert = space === "oklab" ? rgbToOklab : (rgb) => rgb;
  const invert = space === "oklab" ? oklabToRgb : (rgb) => rgb;
  const mixed = [0, 0, 0];
  let alpha = 0;
  for (const operand of operands) {
    const coords = convert(operand.rgb);
    for (let i = 0; i < 3; i += 1) mixed[i] += coords[i] * operand.weight * operand.alpha;
    alpha += operand.weight * operand.alpha;
  }
  const unPremultiplied = alpha === 0 ? [0, 0, 0] : mixed.map((x) => x / alpha);
  return { rgb: invert(unPremultiplied), alpha };
}

/**
 * A token value as `{ rgb: [r, g, b] in 0..1 gamma-encoded, alpha }`. Understands
 * the forms tokens.css uses: `oklch()`, hex, and `color-mix()` in either sRGB or
 * OKLab.
 * @param {string} value
 */
export function parseColor(value) {
  const v = value.trim();
  if (v.startsWith("color-mix(")) return parseColorMix(v);
  if (v.startsWith("#")) {
    const hex = v.slice(1);
    const [r, g, b] = [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
    return { rgb: [r, g, b], alpha: hex.length === 8 ? parseInt(hex.slice(6, 8), 16) / 255 : 1 };
  }
  if (v.startsWith("oklch(")) {
    const { alpha, ...lch } = parseOklch(v);
    const linear = oklchToLinearRgb(lch);
    return { rgb: linear.map((x) => Math.min(1, Math.max(0, toGamma(x)))), alpha };
  }
  throw new Error(`unsupported colour syntax: ${v}`);
}

/** Composite a possibly translucent colour over an opaque one. */
export function over(front, back) {
  const b = parseColor(back).rgb;
  return front.rgb.map((channel, i) => channel * front.alpha + b[i] * (1 - front.alpha));
}

/**
 * WCAG 2.1 contrast ratio. Both arguments are values from tokens.css, so a
 * translucent token is composited over the thing it is being compared against.
 * @param {string} foreground @param {string} background
 */
export function contrastRatio(foreground, background) {
  const fg = over(parseColor(foreground), background);
  const bg = parseColor(background).rgb;
  const luminance = (rgb) => {
    const [r, g, b] = rgb.map(toLinear);
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const [hi, lo] = [luminance(fg), luminance(bg)].sort((a, b) => b - a);
  return (hi + 0.05) / (lo + 0.05);
}
