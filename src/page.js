// Claude Backdrop — page script.
//
// Runs in the main world of every claude.ai page of Claude Desktop. The loader
// wraps it as `(async () => { const CB = {...}; <this file> })()`, so top-level
// `return` and `await` are fine here. It must stay idempotent: every run first
// disposes of the previous one (config changes re-run it live).
//
// CB carries: { version, mode, autoClear, rotate, fixed, gallery, settings }.
//   - `fixed`   preview (data: URL) of the fixed image, or "".
//   - `gallery` [{ id, title, file, url }] pictures to pick from, `url` being a
//               small preview. The loader sends the full-size picture on
//               screen afterwards, through setFull(id, url).
//
// What it does, on top of the static theme CSS:
//   1. Picks the background picture (one at random per conversation, the fixed
//      one, or the one chosen in the panel) and paints it behind the page: the
//      preview at once, then the full-size picture, with a data:->blob:
//      fallback if the CSP refuses it.
//   2. Marks large opaque layers the CSS does not know by name (class names
//      change between Claude releases) so the CSS can make them transparent
//      (data-cb-clear) or frosted (data-cb-glass) — including the terminal.
//   3. Adds a small gallery button (top-right) to browse, add and switch images.
//   4. Reports what it did, for `claude-backdrop status` / `doctor`.

const NS = "__claudeBackdrop";
try {
  if (window[NS]) window[NS].dispose({ rerun: true });
} catch {}

const root = document.documentElement;
const CLEAR = "data-cb-clear";
const GLASS = "data-cb-glass";
const TERM = "data-cb-term";
const NOIMAGE = "data-cb-noimage";
const MODE = "data-cb-mode";
const UI_ID = "cb-ui";

const KEEP = [
  "dialog", '[role="dialog"]', '[role="alertdialog"]', '[aria-modal="true"]',
  '[role="menu"]', '[role="listbox"]', '[role="tooltip"]', "[data-radix-popper-content-wrapper]",
  "pre", "code", "table", "diffs-container", ".xterm", `#${UI_ID}`,
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

let disposed = false;

// ---------------------------------------------------------------- image choice

let chosen = { id: "none", image: "pending", url: "" };
// A blob: URL on screen is handed over from the previous run, not revoked.
let blobUrl = window.__claudeBackdropBlob || "";
delete window.__claudeBackdropBlob;

// The last full-size picture received, kept on window across re-runs so a
// settings change does not fetch it again.
const FULL = "__claudeBackdropFull";
const fullFor = (id) => {
  const f = window[FULL];
  return f && f.id === id ? f.url : "";
};

function detectMode() {
  if (CB.mode === "dark" || CB.mode === "light") return CB.mode;
  const light = document.querySelector('[data-mode="light"], .light, .lightTheme');
  const dark = document.querySelector('[data-mode="dark"], .dark, .darkTheme');
  if (light && !dark) return "light";
  return "dark";
}

// Stable per-conversation seed (sessionStorage): a reload keeps the same
// painting, a different window gets its own.
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
const galleryList = () => (Array.isArray(CB.gallery) ? CB.gallery : []);

// All backgrounds the panel can show, de-duplicated by id and by url. The fixed
// image (rotation off, or a custom "default") is included once.
function items() {
  const out = [];
  const ids = new Set();
  const urls = new Set();
  for (const g of galleryList()) {
    if (!g || ids.has(g.id) || urls.has(g.url)) continue;
    ids.add(g.id);
    urls.add(g.url);
    const custom = /^custom-/.test(g.id);
    out.push({ id: g.id, url: g.url, file: g.file, custom, title: custom ? "Ajoutée" : g.title || g.id });
  }
  if (CB.fixed && !urls.has(CB.fixed)) out.unshift({ id: "fixed", url: CB.fixed, fixed: true, title: "Par défaut" });
  return out;
}

function pickUrl() {
  // A choice made in the panel wins, and sticks for this conversation. Stored as
  // a small id (a data: URL would blow the sessionStorage quota).
  try {
    const id = sessionStorage.getItem("cb-pick-id");
    if (id) {
      const found = items().find((i) => i.id === id);
      if (found) return { id: found.id, url: found.url };
    }
  } catch {}
  const pool = items();
  if (CB.rotate !== "off" && pool.length) {
    const pick = pool[hash(seed()) % pool.length];
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

// The picture goes into a stylesheet built here, not into the --cb-image
// variable: Chromium drops a custom property over 2 MB, and a 4K picture as a
// data: URL is bigger. The sheet outlives re-runs (settings changes re-run
// this script), so the picture does not blink. CSSOM is not subject to the
// page's style-src CSP; the picture itself is, hence the blob: fallback.
const SHEET = "__claudeBackdropSheet";
function picture(url) {
  let sheet = window[SHEET];
  if (!sheet) {
    sheet = window[SHEET] = new CSSStyleSheet();
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }
  sheet.replaceSync(url ? `html body::before { background-image: url("${url}") !important; }` : "");
}
function removePicture() {
  const sheet = window[SHEET];
  if (!sheet) return;
  document.adoptedStyleSheets = document.adoptedStyleSheets.filter((s) => s !== sheet);
  delete window[SHEET];
}

// Paint a URL the page will actually render: data: first, then a
// blob: of the same bytes if the CSP refuses it, else the no-image gradient.
async function showImage(url, id) {
  chosen = { id, image: "pending", url };
  if (blobUrl) {
    URL.revokeObjectURL(blobUrl);
    blobUrl = "";
  }
  root.removeAttribute(NOIMAGE);
  if (!url) {
    picture("");
    root.setAttribute(NOIMAGE, "");
    chosen.image = "none";
    return;
  }
  const setVar = picture;
  if (await canLoad(url)) {
    if (disposed) return;
    setVar(url);
    chosen.image = url.startsWith("data:") ? "data" : "url";
    return;
  }
  try {
    if (url.startsWith("data:")) {
      const comma = url.indexOf(",");
      const mime = /^data:([^;,]+)/.exec(url)?.[1] || "image/jpeg";
      const binary = atob(url.slice(comma + 1));
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
      const blob = URL.createObjectURL(new Blob([bytes], { type: mime }));
      if (!disposed && (await canLoad(blob))) {
        blobUrl = blob;
        setVar(blob);
        chosen.image = "blob";
        return;
      }
      URL.revokeObjectURL(blob);
    }
  } catch {}
  if (disposed) return;
  picture("");
  root.setAttribute(NOIMAGE, "");
  chosen.image = "blocked";
}

const applyImage = () => {
  const pick = pickUrl();
  return showImage(fullFor(pick.id) || pick.url, pick.id);
};

// Called by the loader with the full-size picture of `id`.
async function setFull(id, url) {
  if (!id || !url) return;
  window[FULL] = { id, url };
  if (!disposed && chosen.id === id) await showImage(url, id);
}

// ---------------------------------------------------------------- opaque layers

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
  const host = el.parentElement && el.parentElement.closest(`[${GLASS}="panel"], .dframe-sidebar`);
  if (host) return area >= 0.5 * visibleArea(host.getBoundingClientRect(), vw, vh) ? "clear" : null;
  if (area >= 0.3 * vw * vh && r.width >= 0.45 * vw) return "clear";
  const edge = r.top <= 8 || r.bottom >= vh - 8;
  if (edge && r.width >= 0.45 * vw && r.height >= 24 && r.height <= 0.4 * vh && !el.querySelector(CONTENT)) return "clear";
  if (r.height >= 0.6 * vh && r.width >= 160 && r.width < 0.45 * vw) return "glass";
  return null;
}

// ---------------------------------------------------------------- Claude Code panes

// The side panes of the Code view (diff, files, preview, terminal): the pane
// frame becomes frosted glass, and the big opaque surfaces inside it (code
// view, gutters, the diff viewer's own container) are cleared so the picture
// shows through. Those can live in a shadow root, which the injected CSS does
// not reach, so they get an inline style instead. Small coloured rows (added or
// removed lines, a selection) keep their colour. The chat column's hidden
// placeholder panel is left alone.
const PANE = '[data-pane-root], .epitaxy-view-panel:not([aria-hidden="true"])';
const PANE_SKIP = [
  "iframe", "webview", "canvas", "video", "img", ".xterm", '[aria-modal="true"]',
  '[role="menu"]', '[role="listbox"]', '[role="tooltip"]', "[data-radix-popper-content-wrapper]",
].join(",");

// Inline styles set inside shadow roots, to put back on dispose.
let inlineCleared = [];

// Is `el` (possibly inside shadow roots) somewhere under `ancestor`?
function within(ancestor, el) {
  for (let n = el; n; ) {
    if (ancestor.contains(n)) return true;
    const r = n.getRootNode();
    n = r instanceof ShadowRoot ? r.host : null;
  }
  return false;
}

function clearInline(el) {
  if (el.hasAttribute(CLEAR)) return;
  const saved = { el };
  for (const prop of ["background-color", "background-image"]) {
    saved[prop] = [el.style.getPropertyValue(prop), el.style.getPropertyPriority(prop)];
  }
  inlineCleared.push(saved);
  el.style.setProperty("background-color", "transparent", "important");
  el.style.setProperty("background-image", "none", "important");
  el.setAttribute(CLEAR, "");
}

function restoreInline() {
  for (const saved of inlineCleared) {
    for (const prop of ["background-color", "background-image"]) {
      const [value, priority] = saved[prop];
      if (value) saved.el.style.setProperty(prop, value, priority);
      else saved.el.style.removeProperty(prop);
    }
    saved.el.removeAttribute(CLEAR);
  }
  inlineCleared = [];
}

function clearPane(pane) {
  const pr = pane.getBoundingClientRect();
  if (pr.width < 160 || pr.height < 120) return;
  const found = new Set();
  const visit = (scope, x, y, depth) => {
    for (const el of scope.elementsFromPoint(x, y)) {
      if (found.has(el)) continue;
      found.add(el);
      if (el.shadowRoot && depth < 4) visit(el.shadowRoot, x, y, depth + 1);
    }
  };
  // Columns across the pane, plus one near each edge (gutters are narrow);
  // rows down it, plus one just under the top edge (the pane's header).
  const xs = [pr.left + 20, pr.right - 20];
  for (let i = 0; i < 4; i += 1) xs.push(pr.left + ((i + 0.5) * pr.width) / 4);
  const ys = [pr.top + 12];
  for (let j = 0; j < 5; j += 1) ys.push(pr.top + ((j + 0.5) * pr.height) / 5);
  for (const x of xs) for (const y of ys) visit(document, x, y, 0);
  for (const el of found) {
    if (!(el instanceof HTMLElement) || el === pane || !within(pane, el)) continue;
    if (el.closest(PANE_SKIP) || el.hasAttribute(GLASS)) continue;
    if (alphaOf(getComputedStyle(el).backgroundColor) < 0.85) continue;
    const r = el.getBoundingClientRect();
    const w = Math.min(r.right, pr.right) - Math.max(r.left, pr.left);
    const h = Math.min(r.bottom, pr.bottom) - Math.max(r.top, pr.top);
    if (w <= 0 || h <= 0) continue;
    const surface = w * h >= 0.25 * pr.width * pr.height; // the code view, the diff container
    const strip = w >= 16 && (h >= 0.5 * pr.height || (h >= 120 && h >= 3 * w)); // a gutter
    const header = w >= 0.9 * pr.width && h <= 64 && r.top - pr.top <= 4; // the pane's title bar
    if (!surface && !strip && !header) continue;
    if (el.getRootNode() === document) el.setAttribute(CLEAR, "");
    else clearInline(el);
  }
}

function markPanes() {
  for (const pane of document.querySelectorAll(PANE)) {
    if (!(pane instanceof HTMLElement) || pane.parentElement?.closest(PANE)) continue; // outermost only
    if (pane.closest('[aria-hidden="true"]') || pane.closest(`#${UI_ID}`)) continue;
    const r = pane.getBoundingClientRect();
    if (r.width < 160 || r.height < 120) continue;
    if (!pane.hasAttribute(GLASS)) pane.setAttribute(GLASS, "pane");
    clearPane(pane);
  }
}

// A tall panel docked on the right (the chat's file and artifact viewer, the
// Code view's tile stack): the same treatment as a Code pane. Parents come
// first in `list`, so the outermost one is taken; one that already holds
// frosted panes is left alone, so glass is not stacked on glass.
function markSidePanels(list, vw, vh) {
  for (const el of list) {
    if (el.hasAttribute(GLASS) || el.closest(`#${UI_ID}`) || el.closest('[aria-modal="true"]')) continue;
    const r = el.getBoundingClientRect();
    // right edge on the window's, starting past 40 % of it (a narrow window
    // with a wide sidebar must not turn the chat column itself into glass)
    const docked = r.right >= vw - 48 && r.left >= 0.4 * vw && r.width >= 260 && r.width <= 0.6 * vw && r.height >= 0.7 * vh;
    if (!docked || el.parentElement?.closest(`[${GLASS}="pane"]`)) continue;
    if (el.querySelector(`[${GLASS}="pane"], [${GLASS}="term"]`)) continue;
    el.setAttribute(GLASS, "pane");
    clearPane(el);
  }
}

// xterm.js paints the terminal background on an opaque canvas, which no CSS can
// clear. An SVG filter turns that colour transparent instead: alpha grows with
// the distance in brightness from the terminal background, so the text stays
// opaque and the frosted pane behind shows through the empty cells. Keyed on
// the theme background xterm writes on .xterm-viewport; kept across re-runs.
const KEY_ID = "cb-term-key";
const DEFS_ID = "cb-defs";
function rgbOf(color) {
  const m = /rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/.exec(color || "");
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null;
}
function termKey(term) {
  const bg = rgbOf(term.querySelector(".xterm-viewport")?.style.backgroundColor) || [11, 14, 20];
  const luma = (0.2126 * bg[0] + 0.7152 * bg[1] + 0.0722 * bg[2]) / 255;
  // Light terminal: the text is darker than the background, flip the sign.
  const k = luma > 0.5 ? -12 : 12;
  const alpha = [0.2126 * k, 0.7152 * k, 0.0722 * k, 0, -(k * luma) - 0.2].map((v) => v.toFixed(4)).join(" ");
  let defs = document.getElementById(DEFS_ID);
  if (!defs) {
    // Built node by node: claude.ai enforces Trusted Types, innerHTML throws.
    const SVG_NS = "http://www.w3.org/2000/svg";
    defs = document.createElementNS(SVG_NS, "svg");
    defs.id = DEFS_ID;
    defs.setAttribute("width", "0");
    defs.setAttribute("height", "0");
    defs.setAttribute("aria-hidden", "true");
    defs.style.position = "absolute";
    const filter = document.createElementNS(SVG_NS, "filter");
    filter.id = KEY_ID;
    filter.setAttribute("color-interpolation-filters", "sRGB");
    const matrix = document.createElementNS(SVG_NS, "feColorMatrix");
    matrix.setAttribute("type", "matrix");
    filter.appendChild(matrix);
    defs.appendChild(filter);
    document.body.appendChild(defs);
  }
  const values = `1 0 0 0 0  0 1 0 0 0  0 0 1 0 0  ${alpha}`;
  const matrix = defs.querySelector("feColorMatrix");
  if (matrix.getAttribute("values") !== values) matrix.setAttribute("values", values);
}

// Terminals (xterm.js): frost the pane behind, clear the wrappers in between,
// and key out the background of the canvas (termKey above).
function markTerminals(vw, vh) {
  for (const term of document.querySelectorAll(".xterm")) {
    if (!(term instanceof HTMLElement)) continue;
    term.setAttribute(TERM, "");
    termKey(term);
    // Already inside a frosted Code pane: make that pane the darker terminal
    // glass rather than stacking a second layer of glass.
    const outer = term.closest('[data-cb-glass="pane"]');
    if (outer) {
      outer.setAttribute(GLASS, "term");
      continue;
    }
    let pane = null;
    const chain = [];
    let el = term.parentElement;
    for (let i = 0; el && i < 8; i += 1, el = el.parentElement) {
      const r = el.getBoundingClientRect();
      if (r.width >= 0.98 * vw && r.height >= 0.98 * vh) break;
      if (r.height >= 0.35 * vh && r.width >= 0.22 * vw) pane = el;
      else chain.push(el);
    }
    if (pane) {
      if (!pane.hasAttribute(GLASS)) pane.setAttribute(GLASS, "term");
      // wrappers between the xterm and the pane must not keep an opaque fill
      for (const w of chain) {
        if (pane.contains(w) && !w.hasAttribute(GLASS)) w.setAttribute(CLEAR, "");
      }
    }
  }
}

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

// Each step runs on its own: Claude's DOM changes between releases, and one
// step failing must not leave the rest (or the picture) undone. Failures show
// in `claude-backdrop status`.
let errors = [];
function step(name, fn) {
  try {
    fn();
  } catch (e) {
    if (errors.length < 8) errors.push(`${name}: ${(e && e.message) || e}`);
  }
}

function run() {
  timer = 0;
  if (disposed) return;
  lastRun = Date.now();
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  if (!vw || !vh || !document.body) return;
  errors = [];
  step("gallery button", buildUI);
  let list = [];
  step("sampling", () => {
    list = sample(vw, vh);
  });
  if (CB.autoClear !== false) {
    step("Code panes", markPanes);
    step("side panels", () => markSidePanels(list, vw, vh));
    step("terminals", () => markTerminals(vw, vh));
    step("layers", () => {
      for (const el of list) {
        const kind = classify(el, vw, vh);
        if (kind === "clear") el.setAttribute(CLEAR, "");
        else if (kind === "glass") el.setAttribute(GLASS, "panel");
      }
    });
    step("composer", markComposer);
  }
  step("report", () => {
    opaque = leftovers(list, vw, vh);
  });
}

function schedule() {
  if (timer || disposed) return;
  timer = setTimeout(run, Math.max(120, 700 - (Date.now() - lastRun)));
}

// ---------------------------------------------------------------- gallery panel

const el = (tag, props = {}, style = {}) => {
  const node = document.createElement(tag);
  Object.assign(node, props);
  Object.assign(node.style, style);
  return node;
};
const svg = (paths, size = 18) => {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("width", size);
  s.setAttribute("height", size);
  s.setAttribute("fill", "none");
  s.setAttribute("stroke", "currentColor");
  s.setAttribute("stroke-width", "2");
  s.setAttribute("stroke-linecap", "round");
  s.setAttribute("stroke-linejoin", "round");
  for (const d of paths) {
    const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
    p.setAttribute("d", d);
    s.appendChild(p);
  }
  return s;
};

// Leave a command for the loader to persist (it polls localStorage).
function sendCmd(cmd) {
  try {
    localStorage.setItem("cb-cmd", JSON.stringify({ ts: Date.now(), ...cmd }));
  } catch {}
}

function currentUrl() {
  return chosen.url || "";
}

// Show a background in this window and remember the choice for the conversation
// (by small id, not the data URL).
function pickHere(url, id) {
  try {
    if (id) sessionStorage.setItem("cb-pick-id", id);
    else sessionStorage.removeItem("cb-pick-id");
  } catch {}
  showImage(fullFor(id) || url, id || "choisi").then(renderGrid);
  if (id && !fullFor(id)) api.want = id; // the loader sends the full size

}

let gridEl = null;
function renderGrid() {
  if (!gridEl) return;
  gridEl.textContent = "";
  for (const it of items()) {
    const cell = el("button", { className: "cb-thumb", type: "button" });
    if (it.id === chosen.id) cell.classList.add("cb-current");
    cell.style.backgroundImage = `url("${it.url}")`;
    cell.title = it.title || it.id;
    cell.onclick = () => pickHere(it.url, it.id);
    cell.appendChild(el("span", { className: "cb-caption", textContent: it.title || it.id }));
    if (it.custom && it.file) {
      const del = el("button", { className: "cb-del", type: "button", title: "Retirer" });
      del.appendChild(svg(["M6 6l12 12M18 6L6 18"], 12));
      del.onclick = (e) => {
        e.stopPropagation();
        sendCmd({ action: "delete", file: it.file });
        cell.remove();
      };
      cell.appendChild(del);
    }
    gridEl.appendChild(cell);
  }
  // add tile — just the ＋, no label
  const add = el("label", { className: "cb-thumb cb-add", title: "Ajouter une image" });
  add.appendChild(svg(["M12 5v14M5 12h14"], 24));
  const input = el("input", { type: "file", accept: "image/*" });
  input.style.display = "none";
  input.onchange = async () => {
    const f = input.files && input.files[0];
    if (!f) return;
    let url;
    try {
      url = await downscale(f); // keep it small enough for the command channel
    } catch {
      return;
    }
    if (!url.startsWith("data:image/")) return;
    const id = `custom-${Date.now().toString(36)}`;
    sendCmd({ action: "add", id, name: f.name, dataUrl: url });
    pickHere(url, id); // shows it now; survives the loader's re-inject via the id
  };
  add.appendChild(input);
  gridEl.appendChild(add);
}

// Read an image file and re-encode it as a JPEG no wider than 1920px. A raw
// multi-MB file would overflow the localStorage command channel (and bloat
// every injection); this keeps custom images light and reliable.
function downscale(file, max = 1920, quality = 0.85) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error("read"));
    reader.onload = () => {
      const img = new Image();
      img.onerror = () => reject(new Error("decode"));
      img.onload = () => {
        const scale = Math.min(1, max / Math.max(img.naturalWidth, img.naturalHeight));
        const w = Math.max(1, Math.round(img.naturalWidth * scale));
        const h = Math.max(1, Math.round(img.naturalHeight * scale));
        const canvas = document.createElement("canvas");
        canvas.width = w;
        canvas.height = h;
        canvas.getContext("2d").drawImage(img, 0, 0, w, h);
        try {
          resolve(canvas.toDataURL("image/jpeg", quality));
        } catch (e) {
          reject(e);
        }
      };
      img.src = String(reader.result || "");
    };
    reader.readAsDataURL(file);
  });
}

// A labelled slider that live-updates a CSS var and persists via the loader.
// `unit` is the CSS unit written into the var (e.g. "px"); `format` is display.
function slider(label, key, cssVar, min, max, step, value, format, unit = "") {
  const row = el("label", { className: "cb-slider" });
  const top = el("div", { className: "cb-slider-top" });
  const out = el("span", { className: "cb-slider-val", textContent: format(value) });
  top.append(el("span", { textContent: label }), out);
  const input = el("input", { type: "range", min, max, step, value });
  input.oninput = () => {
    root.style.setProperty(cssVar, input.value + unit);
    out.textContent = format(Number(input.value));
  };
  input.onchange = () => sendCmd({ action: "set", key, value: Number(input.value) });
  row.append(top, input);
  return row;
}
const pct = (v) => `${Math.round(v * 100)}%`;
const px = (v) => `${Math.round(v)} px`;

function buildUI() {
  if (document.getElementById(UI_ID) || !document.body) return;
  const wrap = el("div", { id: UI_ID });

  const btn = el("button", { id: "cb-gallery-btn", type: "button", title: "Fonds d'écran (claude-backdrop)" });
  btn.appendChild(svg(["M3 5h18v14H3z", "M3 15l5-5 4 4 3-3 6 6"], 18));

  const panel = el("div", { id: "cb-gallery-panel", hidden: true });
  const head = el("div", { className: "cb-head" });
  head.appendChild(el("span", { textContent: "Fonds d'écran" }));
  const close = el("button", { className: "cb-x", type: "button", title: "Fermer" });
  close.appendChild(svg(["M6 6l12 12M18 6L6 18"], 14));
  head.appendChild(close);

  gridEl = el("div", { className: "cb-grid" });

  const settings = (CB.settings && typeof CB.settings === "object") ? CB.settings : {};
  const sliders = el("div", { className: "cb-sliders" });
  sliders.append(
    slider("Flou", "imageBlur", "--cb-image-blur", 0, 40, 1, settings.imageBlur ?? 6, px, "px"),
    slider("Luminosité", "brightness", "--cb-brightness", 0.4, 1.6, 0.05, settings.brightness ?? 1, pct),
  );

  const foot = el("div", { className: "cb-foot" });
  const rot = el("label", { className: "cb-rotate" });
  const check = el("input", { type: "checkbox" });
  check.checked = CB.rotate !== "off";
  check.onchange = () => {
    sendCmd({ action: "rotate", value: check.checked ? "on" : "off" });
    if (check.checked) {
      try {
        sessionStorage.removeItem("cb-pick-id");
      } catch {}
    }
  };
  rot.append(check, el("span", { textContent: "Une image au hasard par conversation" }));
  const def = el("button", { className: "cb-default", type: "button", textContent: "Définir par défaut" });
  def.title = "Utiliser l'image affichée dans toutes les fenêtres";
  def.onclick = () => {
    // A gallery picture goes by id (the loader uses its full-size file); only
    // an unknown one is sent as data, which must fit in localStorage.
    if (items().some((i) => i.id === chosen.id)) sendCmd({ action: "default", id: chosen.id });
    else if (currentUrl()) sendCmd({ action: "default", dataUrl: currentUrl() });
  };
  foot.append(rot, def);

  panel.append(head, gridEl, sliders, foot);
  const setOpen = (open) => {
    panel.hidden = !open;
    try {
      if (open) sessionStorage.setItem("cb-panel-open", "1");
      else sessionStorage.removeItem("cb-panel-open");
    } catch {}
    if (open) renderGrid();
  };
  close.onclick = () => setOpen(false);
  btn.onclick = () => setOpen(panel.hidden);
  wrap.append(btn, panel);
  document.body.appendChild(wrap);
  // Keep the panel open across the loader's live re-injections (slider drags,
  // toggles and additions all re-apply the page script).
  let stayOpen = false;
  try {
    stayOpen = sessionStorage.getItem("cb-panel-open") === "1";
  } catch {}
  if (stayOpen) setOpen(true);
}

// ---------------------------------------------------------------- lifecycle

const observer = new MutationObserver(schedule);

// On a re-run ({ rerun: true }) the picture stays up for the next run to
// replace; otherwise (theme turned off) it goes.
function dispose({ rerun = false } = {}) {
  disposed = true;
  clearTimeout(timer);
  observer.disconnect();
  window.removeEventListener("resize", schedule);
  for (const node of document.querySelectorAll(`[${CLEAR}], [${GLASS}], [${TERM}]`)) {
    node.removeAttribute(CLEAR);
    node.removeAttribute(GLASS);
    node.removeAttribute(TERM);
  }
  restoreInline();
  document.getElementById(UI_ID)?.remove();
  gridEl = null;
  root.removeAttribute(MODE);
  root.removeAttribute(NOIMAGE);
  root.style.removeProperty("--cb-image");
  if (rerun) {
    if (blobUrl) window.__claudeBackdropBlob = blobUrl;
  } else {
    removePicture();
    document.getElementById(DEFS_ID)?.remove();
    if (blobUrl) URL.revokeObjectURL(blobUrl);
  }
  if (window[NS] === api) delete window[NS];
}

const status = () => ({
  version: CB.version,
  image: chosen.image,
  painting: chosen.id,
  full: Boolean(fullFor(chosen.id)),
  mode: root.getAttribute(MODE),
  viewport: [window.innerWidth, window.innerHeight],
  cleared: document.querySelectorAll(`[${CLEAR}]`).length,
  glass: document.querySelectorAll(`[${GLASS}]`).length,
  terminals: document.querySelectorAll(`[${TERM}]`).length,
  errors,
  opaque,
});

const api = { version: CB.version, dispose, rescan: run, status, setFull, want: null };
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
setTimeout(schedule, 800);
setTimeout(schedule, 3000);

await applyImage();
if (disposed) return null;
run();
return status();
