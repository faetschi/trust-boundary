// Throwaway WCAG 2.2 contrast check for the tbound palette proposal (read-only analysis).
// oklch -> sRGB via Ottosson's matrices (out-of-gamut channels are clipped and flagged).
// Translucent colours are composited over the actual surface in gamma-encoded sRGB, which is
// how browsers blend color-mix(... transparent) layers. Each element is checked against the
// WORST surface it may sit on. Text needs 4.5:1, non-text UI needs 3:1 (WCAG 2.2 1.4.3, 1.4.11).

const clamp01 = v => Math.min(1, Math.max(0, v));
const toLin = c => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const toEnc = c => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055);

function toRgbRaw({ L, C = 0, h = 0, a = 1 }) {
  const hr = (h * Math.PI) / 180;
  const A = C * Math.cos(hr);
  const B = C * Math.sin(hr);
  const l = (L + 0.3963377774 * A + 0.2158037573 * B) ** 3;
  const m = (L - 0.1055613458 * A - 0.0638541728 * B) ** 3;
  const s = (L - 0.0894841775 * A - 1.2914855480 * B) ** 3;
  const lin = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s,
  ];
  const clipped = lin.some(v => v < -1e-4 || v > 1 + 1e-4);
  return { rgb: lin.map(v => clamp01(toEnc(clamp01(v)))), a, clipped };
}

// CSS Color 4-style gamut mapping: if the colour is outside sRGB, reduce chroma (keep L and h) until it fits.
function toRgb(spec) {
  const raw = toRgbRaw(spec);
  if (!raw.clipped || !spec.C) return raw;
  let lo = 0;
  let hi = spec.C;
  for (let i = 0; i < 30; i++) {
    const mid = (lo + hi) / 2;
    if (toRgbRaw({ ...spec, C: mid }).clipped) hi = mid;
    else lo = mid;
  }
  return toRgbRaw({ ...spec, C: lo });
}

// Composite a colour spec over an opaque backdrop (sRGB 0..1). Opaque specs ignore the backdrop.
function paint(spec, backdrop = [1, 1, 1]) {
  const c = toRgb(spec);
  return c.rgb.map((v, i) => c.a * v + (1 - c.a) * backdrop[i]);
}
function lum(rgb) {
  const [r, g, b] = rgb.map(toLin);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
function ratio(x, y) {
  const a = lum(x);
  const b = lum(y);
  const [hi, lo] = a > b ? [a, b] : [b, a];
  return (hi + 0.05) / (lo + 0.05);
}
const hex = rgb => '#' + rgb.map(c => Math.round(c * 255).toString(16).padStart(2, '0')).join('').toUpperCase();

// ---------- palettes (oklch) ----------
// tbound's current sheet (styles.css :root). It is dark-only, so this is the baseline.
const CURRENT_DARK = {
  canvas: { L: 0.145 }, sidebar: { L: 0.18 }, raised: { L: 0.205 }, bubble: { L: 0.269 }, hover: { L: 0.371 },
  fg: { L: 0.985 }, fg2: { L: 0.708 },
  okInk: { L: 0.82, C: 0.14, h: 155 }, okLine: { L: 0.7, C: 0.15, h: 155 },
  denyInk: { L: 0.637, C: 0.237, h: 25.331 }, denyLine: { L: 0.704, C: 0.191, h: 22.216 },
  allowInk: { L: 0.72, C: 0.13, h: 250 },
  reportedInk: { L: 0.87, C: 0.13, h: 82 }, reportedLine: { L: 0.8, C: 0.15, h: 82 },
  inputBorder: { L: 1, a: 0.15 }, // border: 1px solid oklch(1 0 0 / 15%)
};

// Proposal, light theme. Neutral (chroma 0) surfaces and text; state hues kept from tbound.
const PROPOSED_LIGHT = {
  canvas: { L: 1 }, sidebar: { L: 0.975 }, raised: { L: 1 }, bubble: { L: 0.955 }, hover: { L: 0.945 },
  fg: { L: 0.2 }, fg2: { L: 0.42 }, fg3: { L: 0.5 },
  okInk: { L: 0.44, C: 0.12, h: 150 }, okLine: { L: 0.52, C: 0.13, h: 150 },
  denyInk: { L: 0.46, C: 0.18, h: 27 }, denyLine: { L: 0.52, C: 0.19, h: 27 },
  allowInk: { L: 0.44, C: 0.09, h: 250 },
  reportedInk: { L: 0.45, C: 0.11, h: 70 }, reportedLine: { L: 0.6, C: 0.15, h: 70 },
  pendingLine: { L: 0.5 }, idle: { L: 0.55 },
  srcUser: { L: 0.42, C: 0.09, h: 210 }, srcAssistant: { L: 0.5, C: 0.13, h: 300 },
  srcTool: { L: 0.45, C: 0.11, h: 70 }, srcResult: { L: 0.42, C: 0.03, h: 250 }, srcDecision: { L: 0.45, C: 0.11, h: 92 }, selection: { L: 0.88 },
  primaryFill: { L: 0.2 }, primaryFg: { L: 0.985 }, focus: { L: 0.2 }, inputBorder: { L: 0.6 },
};

// Proposal, dark theme.
const PROPOSED_DARK = {
  canvas: { L: 0.18 }, sidebar: { L: 0.215 }, raised: { L: 0.245 }, bubble: { L: 0.275 }, hover: { L: 0.29 },
  fg: { L: 0.97 }, fg2: { L: 0.82 }, fg3: { L: 0.7 },
  okInk: { L: 0.76, C: 0.15, h: 155 }, okLine: { L: 0.76, C: 0.15, h: 155 },
  denyInk: { L: 0.76, C: 0.16, h: 22 }, denyLine: { L: 0.76, C: 0.16, h: 22 },
  allowInk: { L: 0.76, C: 0.09, h: 250 },
  reportedInk: { L: 0.86, C: 0.13, h: 80 }, reportedLine: { L: 0.8, C: 0.14, h: 80 },
  pendingLine: { L: 0.72 }, idle: { L: 0.62 },
  srcUser: { L: 0.8, C: 0.1, h: 210 }, srcAssistant: { L: 0.8, C: 0.12, h: 300 },
  srcTool: { L: 0.86, C: 0.13, h: 80 }, srcResult: { L: 0.78, C: 0.03, h: 250 }, srcDecision: { L: 0.86, C: 0.13, h: 92 }, selection: { L: 0.4 },
  primaryFill: { L: 0.96 }, primaryFg: { L: 0.2 }, focus: { L: 0.93 }, inputBorder: { L: 0.6 },
};

const SURF = ['canvas', 'sidebar', 'raised', 'bubble', 'hover'];
const CHIP_SURF = ['canvas', 'raised', 'bubble', 'hover'];

// Each row: what is checked, the foreground key, the surfaces it may sit on, and an optional tint key.
const ROWS = [
  { label: 'text primary', kind: 'text', fg: 'fg', on: SURF },
  { label: 'text secondary', kind: 'text', fg: 'fg2', on: SURF },
  { label: 'text tertiary', kind: 'text', fg: 'fg3', on: SURF },
  { label: 'chip ink ok (verified)', kind: 'chip', fg: 'okInk', tint: 'okLine', on: CHIP_SURF },
  { label: 'chip ink deny', kind: 'chip', fg: 'denyInk', tint: 'denyLine', on: CHIP_SURF },
  { label: 'chip ink allow (permission)', kind: 'chip', fg: 'allowInk', tint: 'allowInk', on: CHIP_SURF },
  { label: 'chip ink reported/unknown', kind: 'chip', fg: 'reportedInk', tint: 'reportedLine', on: CHIP_SURF },
  { label: 'chip ink pending (=text2)', kind: 'chip', fg: 'fg2', tint: 'fg2', on: CHIP_SURF },
  { label: 'src chip user', kind: 'chip', fg: 'srcUser', tint: 'srcUser', on: CHIP_SURF },
  { label: 'src chip assistant', kind: 'chip', fg: 'srcAssistant', tint: 'srcAssistant', on: CHIP_SURF },
  { label: 'src chip tool', kind: 'chip', fg: 'srcTool', tint: 'srcTool', on: CHIP_SURF },
  { label: 'src chip result/message', kind: 'chip', fg: 'srcResult', tint: 'srcResult', on: CHIP_SURF },
  { label: 'src chip decision', kind: 'chip', fg: 'srcDecision', tint: 'srcDecision', on: CHIP_SURF },
  { label: 'selection text (::selection)', kind: 'fill', fg: 'fg', bg: 'selection', on: ['canvas'] },
  { label: 'primary button label', kind: 'fill', fg: 'primaryFg', bg: 'primaryFill', on: ['canvas'] },
  { label: 'dot ok (verified) fill', kind: 'nontext', fg: 'okLine', on: SURF },
  { label: 'dot deny fill/border', kind: 'nontext', fg: 'denyLine', on: SURF },
  { label: 'dot allow border', kind: 'nontext', fg: 'allowInk', on: SURF },
  { label: 'pending hollow ring', kind: 'nontext', fg: 'pendingLine', on: SURF },
  { label: 'reported dashed border', kind: 'nontext', fg: 'reportedLine', on: SURF },
  { label: 'unknown dotted border', kind: 'nontext', fg: 'reportedLine', on: SURF },
  { label: 'idle filled dot', kind: 'nontext', fg: 'idle', on: SURF },
  { label: 'focus ring (2px)', kind: 'nontext', fg: 'focus', on: SURF },
  { label: 'control boundary (input)', kind: 'nontext', fg: 'inputBorder', on: ['canvas', 'sidebar', 'raised'] },
];

// Baseline rows for the current dark sheet, same rules.
const CURRENT_ROWS = [
  { label: 'text foreground', kind: 'text', fg: 'fg', on: ['canvas', 'raised', 'bubble'] },
  { label: 'text muted', kind: 'text', fg: 'fg2', on: ['canvas', 'raised', 'bubble'] },
  { label: 'chip ink ok', kind: 'chip', fg: 'okInk', tint: 'okLine', on: ['canvas', 'raised'] },
  { label: 'chip ink deny', kind: 'chip', fg: 'denyInk', tint: 'denyLine', on: ['canvas', 'raised'] },
  { label: 'chip ink allow', kind: 'chip', fg: 'allowInk', tint: 'allowInk', on: ['canvas', 'raised'] },
  { label: 'chip ink reported', kind: 'chip', fg: 'reportedInk', tint: 'reportedLine', on: ['canvas', 'raised'] },
  { label: 'reported dashed border', kind: 'nontext', fg: 'reportedLine', on: ['canvas', 'raised'] },
  { label: 'control boundary (15% white)', kind: 'nontext', fg: 'inputBorder', on: ['canvas', 'raised'] },
];

function check(pal, row) {
  let worst = { r: Infinity, surface: '' };
  for (const sk of row.on) {
    const surf = paint(pal[sk]);
    let fgRgb;
    let bgRgb;
    if (row.kind === 'chip') {
      bgRgb = paint({ ...pal[row.tint], a: 0.12 }, surf); // state colour at 12% over the surface
      fgRgb = paint(pal[row.fg], bgRgb);
    } else if (row.kind === 'fill') {
      bgRgb = paint(pal[row.bg]);
      fgRgb = paint(pal[row.fg], bgRgb);
    } else {
      bgRgb = surf;
      fgRgb = paint(pal[row.fg], surf);
    }
    const r = ratio(fgRgb, bgRgb);
    if (r < worst.r) worst = { r, surface: row.kind === 'fill' ? row.bg : sk, fgRgb, bgRgb };
  }
  const need = row.kind === 'nontext' ? 3 : 4.5; // text, chip ink and fill label are text; dots, rings, borders are non-text
  return { ...worst, need, pass: worst.r >= need };
}

function table(name, pal, rows) {
  const out = rows.map(row => {
    const c = check(pal, row);
    return {
      element: row.label,
      ink: hex(paint(pal[row.fg], c.bgRgb)),
      worstOn: c.surface,
      ratio: c.r.toFixed(2),
      need: c.need.toFixed(1),
      verdict: c.pass ? 'PASS' : 'FAIL',
    };
  });
  console.log(`\n== ${name} ==`);
  console.table(out);
  return out;
}

console.log('Proposed palette sRGB hex (clipped = channel was outside sRGB):');
for (const [name, pal] of [['LIGHT', PROPOSED_LIGHT], ['DARK', PROPOSED_DARK], ['CURRENT_DARK', CURRENT_DARK]]) {
  const flat = Object.entries(pal).map(([k, v]) => `${k}=${hex(paint(v))}${toRgbRaw(v).clipped ? "*" : ""}`);
  console.log(name + ':', flat.join(' '));
}

const base = table('BASELINE tbound current dark (styles.css :root)', CURRENT_DARK, CURRENT_ROWS);
const light = table('PROPOSED light', PROPOSED_LIGHT, ROWS);
const dark = table('PROPOSED dark', PROPOSED_DARK, ROWS);
const fails = [...light.map(r => ['light', r]), ...dark.map(r => ['dark', r])].filter(([, r]) => r.verdict === 'FAIL');
console.log(`\nPROPOSED FAILS (${fails.length}):`, fails.map(([t, r]) => `${t}:${r.element}=${r.ratio}`).join(' | ') || 'none');
console.log(`BASELINE FAILS: ${base.filter(r => r.verdict === 'FAIL').map(r => `${r.element}=${r.ratio}`).join(' | ') || 'none'}`);
