// Claude Backdrop — page script.
//
// Runs in the main world of every claude.ai page of Claude Desktop. The loader
// wraps it as `(async () => { const CB = {...}; <this file> })()`, so top-level
// `return` and `await` are fine here. It must stay idempotent: every run first
// disposes of the previous one (config changes re-run it live).
//
// What it does, on top of the static theme CSS:
//   1. Makes sure the background picture can load. Claude's Content Security
//      Policy may refuse `data:` images; the picture is then re-served as a
//      `blob:` URL, and if that is refused too the theme falls back to a plain
//      gradient (reported as image: "blocked").
//   2. Finds large opaque layers the CSS does not know by name (class names
//      change between Claude releases) and marks them so the CSS can make them
//      transparent (`data-cb-clear`) or frosted (`data-cb-glass`).
//   3. Reports what it did, for `claude-backdrop status` / `doctor`.

const NS = "__claudeBackdrop";
try {
  if (window[NS]) window[NS].dispose();
} catch {}

const root = document.documentElement;
const CLEAR = "data-cb-clear";
const GLASS = "data-cb-glass";
const NOIMAGE = "data-cb-noimage";
const MODE = "data-cb-mode";

// Never touch what floats above the page (dialogs, menus, popovers, tooltips)
// nor surfaces whose colours carry meaning (code, diffs, tables, terminals).
const KEEP = [
  "dialog", '[role="dialog"]', '[role="alertdialog"]', '[aria-modal="true"]',
  '[role="menu"]', '[role="listbox"]', '[role="tooltip"]', "[data-radix-popper-content-wrapper]",
  "pre", "code", "table", "diffs-container", ".xterm",
].join(",");
const CONTENT = "pre, code, table, diffs-container, img, video, canvas, iframe";
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

// The composer frame: the first rounded ancestor of the editor. The theme
// recolours it through Claude's own prompt tokens; this is the fallback when
// those tokens are not in use and the frame is still opaque.
function composerFrames() {
  const frames = [];
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
        if (!el.hasAttribute(GLASS) && alphaOf(style.backgroundColor) >= 0.85) frames.push(el);
        break;
      }
    }
  }
  return frames;
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

let disposed = false;
let timer = 0;
let lastRun = 0;
let opaque = [];
let image = "pending";
let blobUrl = "";

function run() {
  timer = 0;
  if (disposed) return;
  lastRun = Date.now();
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  if (!vw || !vh || !document.body) return;
  const list = sample(vw, vh);
  if (CB.autoClear !== false) {
    for (const el of list) {
      const kind = classify(el, vw, vh);
      if (kind === "clear") el.setAttribute(CLEAR, "");
      else if (kind === "glass") el.setAttribute(GLASS, "panel");
    }
    for (const el of composerFrames()) el.setAttribute(GLASS, "field");
  }
  opaque = leftovers(list, vw, vh);
}

// At most one scan every 700 ms: streaming replies mutate the DOM constantly.
function schedule() {
  if (timer || disposed) return;
  timer = setTimeout(run, Math.max(120, 700 - (Date.now() - lastRun)));
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

async function checkImage() {
  const raw = getComputedStyle(root).getPropertyValue("--cb-image").trim();
  if (!raw || raw === "none") return "none";
  const match = /^url\(\s*(["']?)(data:[^"')\s]+)\1\s*\)$/.exec(raw);
  if (!match) return (await canLoad(raw.replace(/^url\(\s*["']?|["']?\s*\)$/g, ""))) ? "url" : "blocked";
  if (await canLoad(match[2])) return "data";
  try {
    const comma = match[2].indexOf(",");
    const mime = /^data:([^;,]+)/.exec(match[2])?.[1] || "image/jpeg";
    const binary = atob(match[2].slice(comma + 1));
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
    const url = URL.createObjectURL(new Blob([bytes], { type: mime }));
    if (!disposed && (await canLoad(url))) {
      blobUrl = url;
      root.style.setProperty("--cb-image", `url("${url}")`);
      return "blob";
    }
    URL.revokeObjectURL(url);
  } catch {}
  if (disposed) return "disposed";
  root.setAttribute(NOIMAGE, "");
  return "blocked";
}

const observer = new MutationObserver(schedule);

function dispose() {
  disposed = true;
  clearTimeout(timer);
  observer.disconnect();
  window.removeEventListener("resize", schedule);
  for (const el of document.querySelectorAll(`[${CLEAR}], [${GLASS}]`)) {
    el.removeAttribute(CLEAR);
    el.removeAttribute(GLASS);
  }
  root.removeAttribute(MODE);
  root.removeAttribute(NOIMAGE);
  root.style.removeProperty("--cb-image");
  if (blobUrl) URL.revokeObjectURL(blobUrl);
  if (window[NS] === api) delete window[NS];
}

const status = () => ({
  version: CB.version,
  image,
  mode: root.getAttribute(MODE),
  viewport: [window.innerWidth, window.innerHeight],
  cleared: document.querySelectorAll(`[${CLEAR}]`).length,
  glass: document.querySelectorAll(`[${GLASS}]`).length,
  opaque,
});

const api = { version: CB.version, dispose, rescan: run, status };
window[NS] = api;

root.setAttribute(MODE, CB.mode === "dark" || CB.mode === "light" ? CB.mode : "auto");
run();
observer.observe(document.body || root, {
  childList: true,
  subtree: true,
  attributes: true,
  attributeFilter: ["class", "style", "hidden", "data-state"],
});
window.addEventListener("resize", schedule);
// claude.ai keeps rendering after dom-ready: look again once it has settled.
setTimeout(schedule, 800);
setTimeout(schedule, 3000);

image = await checkImage();
if (disposed) return null;
run();
return status();
