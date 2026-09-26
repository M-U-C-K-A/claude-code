// Claude Backdrop — page script.
//
// Runs in the main world of every claude.ai page of Claude Desktop. The loader
// wraps it as `(async () => { const CB = {...}; <this file> })()`, so top-level
// `return` and `await` are fine here. It must stay idempotent: every run first
// disposes of the previous one (config changes re-run it live).
//
// CB carries: { version, mode, autoClear, rotate, fixed, gallery }.
//   - `fixed`   data: URL of the chosen fixed image (rotate "off"), or "".
//   - `gallery` [{ id, mode, url }] paintings to pick from per conversation.
//
// What it does, on top of the static theme CSS:
//   1. Picks the background picture (one at random per conversation, matching
//      the light/dark mode, or the fixed one) and sets --cb-image. If the page
//      CSP refuses a data: image it retries as a blob:, then gives up to a
//      gradient (image: "blocked").
//   2. Marks large opaque layers the CSS does not know by name (class names
//      change between Claude releases) so the CSS can make them transparent
//      (data-cb-clear) or frosted (data-cb-glass) — including the terminal.
//   3. Reports what it did, for `claude-backdrop status` / `doctor`.

const NS = "__claudeBackdrop";
try {
  if (window[NS]) window[NS].dispose();
} catch {}

const root = document.documentElement;
const CLEAR = "data-cb-clear";
const GLASS = "data-cb-glass";
const TERM = "data-cb-term";
const NOIMAGE = "data-cb-noimage";
const MODE = "data-cb-mode";

// Never touch what floats above the page (dialogs, menus, popovers, tooltips)
// nor surfaces whose colours carry meaning (code, diffs, tables). The terminal
// is handled on its own (see markTerminals) so it is kept out of the generic
// pass here.
const KEEP = [
  "dialog", '[role="dialog"]', '[role="alertdialog"]', '[aria-modal="true"]',
  '[role="menu"]', '[role="listbox"]', '[role="tooltip"]', "[data-radix-popper-content-wrapper]",
  "pre", "code", "table", "diffs-container", ".xterm",
].join(",");
const CONTENT = "pre, code, table, diffs-container, img, video, canvas, iframe, .xterm";
const EDITOR = '.ProseMirror, [contenteditable="true"], textarea';

const alphaOf = (color) => {
  if (!color || color === "transparent") return 0;
  let match = /\/\s*([\d.]+)(%?)\s*\)$/.exec(color);
  if (match) return match[2] ? Number(match[1]) / 100 : Number(match[1]);
  match = /^rgba\(([^)]*)\)$/.exec(color);
  if (match) {
    const parts = match[1].split(",");
    return parts.length === 4 ? Number.parseFloat(parts[3]) : 1;
  }
  return 1;
};
const visibleArea = (r, vw, vh) =>
  Math.max(0, Math.min(r.right, vw) - Math.max(r.left, 0)) * Math.max(0, Math.min(r.bottom, vh) - Math.max(r.top, 0));
const depth = (el) => {
  let d = 0;
  for (let n = el.parentElement; n; n = n.parentElement) d += 1;
  return d;
};

// ---------------------------------------------------------------- image choice

let disposed = false;
let chosen = { id: "none", image: "pending" };
let blobUrl = "";

function detectMode() {
  if (CB.mode === "dark" || CB.mode === "light") return CB.mode;
  const el = document.querySelector('[data-mode="light"], .light, .lightTheme');
  if (el && !document.querySelector('[data-mode="dark"], .dark, .darkTheme')) return "light";
  const dark = document.querySelector('[data-mode="dark"], .dark, .darkTheme');
  return dark ? "dark" : "dark";
}

// Stable per-conversation seed: kept in sessionStorage so a reload keeps the
// same painting, while a different window (a different conversation) gets its
// own. Falls back to a per-run random if storage is blocked.
let seedCache = null;
function seed() {
  if (seedCache !== null) return seedCache;
  try {
    let s = sessionStorage.getItem("cb-seed");
    if (!s) {
      s = String(Math.floor(Math.random() * 1e9));
      sessionStorage.setItem("cb-seed", s);
    }
    seedCache = s;
  } catch {
    seedCache = String(Math.floor(Math.random() * 1e9));
  }
  return seedCache;
}
const hash = (str) => {
  let h = 2166136261;
  for (let i = 0; i < str.length; i += 1) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
};

// The picture for this conversation: fixed one, or one drawn from the gallery
// for the current mode.
function pickImage() {
  const gallery = Array.isArray(CB.gallery) ? CB.gallery : [];
  if (CB.rotate !== "off" && gallery.length) {
    const mode = detectMode();
    let pool = gallery.filter((g) => g.mode === mode);
    if (!pool.length) pool = gallery;
    const pick = pool[hash(`${seed()}:${mode}`) % pool.length];
    return { id: pick.id, url: pick.url };
  }
  return { id: CB.fixed ? "fixed" : "none", url: CB.fixed || "" };
}

const canLoad = (src) =>
  new Promise((resolve) => {
    const img = new Image();
    const done = (ok) => {
      clearTimeout(guard);
      resolve(ok);
    };
    const guard = setTimeout(() => done(false), 5000);
    img.onload = () => done(true);
    img.onerror = () => done(false);
    img.src = src;
  });

// Set --cb-image to a URL the page will actually render. data: first; if the
// CSP refuses it, re-serve the same bytes as a blob:; else fall back to the
// no-image gradient.
async function applyImage() {
  const pick = pickImage();
  chosen = { id: pick.id, image: "pending" };
  if (blobUrl) {
    URL.revokeObjectURL(blobUrl);
    blobUrl = "";
  }
  root.removeAttribute(NOIMAGE);
  if (!pick.url) {
    root.style.removeProperty("--cb-image");
    root.setAttribute(NOIMAGE, "");
    chosen.image = "none";
    return;
  }
  const setVar = (value) => root.style.setProperty("--cb-image", `url("${value}")`);
  if (await canLoad(pick.url)) {
    if (disposed) return;
    setVar(pick.url);
    chosen.image = pick.url.startsWith("data:") ? "data" : "url";
    return;
  }
  // CSP blocked the data: URL — try a blob: of the same bytes.
  try {
    if (pick.url.startsWith("data:")) {
      const comma = pick.url.indexOf(",");
      const mime = /^data:([^;,]+)/.exec(pick.url)?.[1] || "image/jpeg";
      const binary = atob(pick.url.slice(comma + 1));
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
      const url = URL.createObjectURL(new Blob([bytes], { type: mime }));
      if (!disposed && (await canLoad(url))) {
        blobUrl = url;
        setVar(url);
        chosen.image = "blob";
        return;
      }
      URL.revokeObjectURL(url);
    }
  } catch {}
  if (disposed) return;
  root.style.removeProperty("--cb-image");
  root.setAttribute(NOIMAGE, "");
  chosen.image = "blocked";
}

// ---------------------------------------------------------------- opaque layers

// Elements under a grid of points across the window, plus the top and bottom
// edges (title bars, composer docks). Parents come first.
function sample(vw, vh) {
  const found = new Set();
  const COLS = 8;
  const ROWS = 6;
  const add = (x, y) => {
    for (const el of document.elementsFromPoint(x, y)) found.add(el);
  };
  for (let i = 0; i < COLS; i += 1) {
    const x = ((i + 0.5) * vw) / COLS;
    for (let j = 0; j < ROWS; j += 1) add(x, ((j + 0.5) * vh) / ROWS);
    add(x, 6);
    add(x, vh - 6);
  }
  return [...found]
    .filter((el) => el instanceof HTMLElement && el !== root && el !== document.body)
    .sort((a, b) => depth(a) - depth(b));
}

function classify(el, vw, vh) {
  if (el.hasAttribute(CLEAR) || el.hasAttribute(GLASS)) return null;
  if (el.closest(KEEP) || el.closest(EDITOR)) return null;
  const style = getComputedStyle(el);
  if (alphaOf(style.backgroundColor) < 0.85) return null;
  const r = el.getBoundingClientRect();
  const area = visibleArea(r, vw, vh);
  if (area <= 0) return null;
  // Opaque fillers inside a frosted panel would hide the frost: clear them.
  const host = el.parentElement && el.parentElement.closest(`[${GLASS}="panel"], .dframe-sidebar`);
  if (host) return area >= 0.5 * visibleArea(host.getBoundingClientRect(), vw, vh) ? "clear" : null;
  // Page-sized layers.
  if (area >= 0.3 * vw * vh && r.width >= 0.45 * vw) return "clear";
  // Wide bands glued to the top or bottom edge: title bars, composer docks.
  const edge = r.top <= 8 || r.bottom >= vh - 8;
  if (edge && r.width >= 0.45 * vw && r.height >= 24 && r.height <= 0.4 * vh && !el.querySelector(CONTENT)) return "clear";
  // Tall side panels: frosted glass, like the sidebar.
  if (r.height >= 0.6 * vh && r.width >= 160 && r.width < 0.45 * vw) return "glass";
  return null;
}

// Terminals: make the xterm layers see-through and frost the pane behind them,
// so the picture shows through the empty cells while coloured output keeps its
// colours. Kept out of the generic pass (KEEP has .xterm).
function markTerminals(vw, vh) {
  for (const term of document.querySelectorAll(".xterm")) {
    if (!(term instanceof HTMLElement)) continue;
    term.setAttribute(TERM, "");
    let pane = null;
    let el = term;
    for (let i = 0; el && i < 8; i += 1, el = el.parentElement) {
      const r = el.getBoundingClientRect();
      if (r.width >= 0.98 * vw && r.height >= 0.98 * vh) break; // reached the window
      if (r.height >= 0.35 * vh && r.width >= 0.22 * vw) pane = el;
    }
    if (pane && !pane.hasAttribute(GLASS)) pane.setAttribute(GLASS, "term");
  }
}

// The composer frame: the first rounded ancestor of the editor. Fallback for
// when Claude's own prompt tokens are not in use and the frame stays opaque.
function markComposer() {
  for (const editor of document.querySelectorAll(EDITOR)) {
    if (!(editor instanceof HTMLElement) || editor.closest(KEEP)) continue;
    const base = editor.getBoundingClientRect();
    if (!base.width || !base.height) continue;
    let el = editor.parentElement;
    for (let i = 0; el && el !== document.body && i < 10; i += 1, el = el.parentElement) {
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height) continue;
      if (r.width > base.width + 240 || r.height > base.height + 280) break;
      const style = getComputedStyle(el);
      if ((Number.parseFloat(style.borderTopLeftRadius) || 0) >= 8) {
        if (!el.hasAttribute(GLASS) && alphaOf(style.backgroundColor) >= 0.85) el.setAttribute(GLASS, "field");
        break;
      }
    }
  }
}

// Large opaque surfaces still left after marking, for diagnostics.
function leftovers(list, vw, vh) {
  const out = [];
  for (const el of list) {
    if (!el.isConnected || el.hasAttribute(CLEAR) || el.hasAttribute(GLASS) || el.closest(KEEP)) continue;
    const style = getComputedStyle(el);
    if (alphaOf(style.backgroundColor) < 0.85) continue;
    const r = el.getBoundingClientRect();
    const share = visibleArea(r, vw, vh) / (vw * vh);
    if (share < 0.02) continue;
    const classes = typeof el.className === "string" ? el.className.split(/\s+/).filter(Boolean).slice(0, 4).join(" ") : "";
    out.push({
      tag: el.tagName.toLowerCase(),
      id: el.id || undefined,
      class: classes || undefined,
      testid: el.dataset.testid || undefined,
      background: style.backgroundColor,
      box: [Math.round(r.left), Math.round(r.top), Math.round(r.width), Math.round(r.height)],
      share: Math.round(share * 1000) / 1000,
    });
  }
  return out.sort((a, b) => b.share - a.share).slice(0, 12);
}

let timer = 0;
let lastRun = 0;
let opaque = [];

function run() {
  timer = 0;
  if (disposed) return;
  lastRun = Date.now();
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  if (!vw || !vh || !document.body) return;
  const list = sample(vw, vh);
  if (CB.autoClear !== false) {
    markTerminals(vw, vh);
    for (const el of list) {
      const kind = classify(el, vw, vh);
      if (kind === "clear") el.setAttribute(CLEAR, "");
      else if (kind === "glass") el.setAttribute(GLASS, "panel");
    }
    markComposer();
  }
  opaque = leftovers(list, vw, vh);
}

// At most one scan every 700 ms: streaming replies mutate the DOM constantly.
function schedule() {
  if (timer || disposed) return;
  timer = setTimeout(run, Math.max(120, 700 - (Date.now() - lastRun)));
}

const observer = new MutationObserver(schedule);

function dispose() {
  disposed = true;
  clearTimeout(timer);
  observer.disconnect();
  window.removeEventListener("resize", schedule);
  for (const el of document.querySelectorAll(`[${CLEAR}], [${GLASS}], [${TERM}]`)) {
    el.removeAttribute(CLEAR);
    el.removeAttribute(GLASS);
    el.removeAttribute(TERM);
  }
  root.removeAttribute(MODE);
  root.removeAttribute(NOIMAGE);
  root.style.removeProperty("--cb-image");
  if (blobUrl) URL.revokeObjectURL(blobUrl);
  if (window[NS] === api) delete window[NS];
}

const status = () => ({
  version: CB.version,
  image: chosen.image,
  painting: chosen.id,
  mode: root.getAttribute(MODE),
  viewport: [window.innerWidth, window.innerHeight],
  cleared: document.querySelectorAll(`[${CLEAR}]`).length,
  glass: document.querySelectorAll(`[${GLASS}]`).length,
  terminals: document.querySelectorAll(`[${TERM}]`).length,
  opaque,
});

const api = { version: CB.version, dispose, rescan: run, status };
window[NS] = api;

root.setAttribute(MODE, detectMode());
run();
observer.observe(document.body || root, {
  childList: true,
  subtree: true,
  attributes: true,
  attributeFilter: ["class", "style", "hidden", "data-state", "data-mode"],
});
window.addEventListener("resize", schedule);
// claude.ai keeps rendering after dom-ready: look again once it has settled.
setTimeout(schedule, 800);
setTimeout(schedule, 3000);

await applyImage();
if (disposed) return null;
run();
return status();
