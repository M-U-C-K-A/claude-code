// Claude Backdrop — page script.
//
// Runs in the main world of every claude.ai page of Claude Desktop. The loader
// wraps it as `(async () => { const CB = {...}; <this file> })()`, so top-level
// `return` and `await` are fine here. It must stay idempotent: every run first
// disposes of the previous one (config changes re-run it live).
//
// CB carries: { version, mode, autoClear, rotate, fixed, gallery }.
//   - `fixed`   data: URL of the chosen fixed image (rotate "off"), or "".
//   - `gallery` [{ id, mode, file, url }] paintings to pick from.
//
// What it does, on top of the static theme CSS:
//   1. Picks the background picture (one at random per conversation matching the
//      light/dark mode, the fixed one, or the one chosen in the panel) and sets
//      --cb-image, with a data:->blob: fallback if the CSP refuses it.
//   2. Marks large opaque layers the CSS does not know by name (class names
//      change between Claude releases) so the CSS can make them transparent
//      (data-cb-clear) or frosted (data-cb-glass) — including the terminal.
//   3. Adds a small gallery button (top-right) to browse, add and switch images.
//   4. Reports what it did, for `claude-backdrop status` / `doctor`.

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
let blobUrl = "";

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
    out.push({ id: g.id, url: g.url, file: g.file, custom: /^custom-/.test(g.id) });
  }
  if (CB.fixed && !urls.has(CB.fixed)) out.unshift({ id: "fixed", url: CB.fixed, fixed: true });
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

// Point --cb-image at a URL the page will actually render: data: first, then a
// blob: of the same bytes if the CSP refuses it, else the no-image gradient.
async function showImage(url, id) {
  chosen = { id, image: "pending", url };
  if (blobUrl) {
    URL.revokeObjectURL(blobUrl);
    blobUrl = "";
  }
  root.removeAttribute(NOIMAGE);
  if (!url) {
    root.style.removeProperty("--cb-image");
    root.setAttribute(NOIMAGE, "");
    chosen.image = "none";
    return;
  }
  const setVar = (value) => root.style.setProperty("--cb-image", `url("${value}")`);
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
  root.style.removeProperty("--cb-image");
  root.setAttribute(NOIMAGE, "");
  chosen.image = "blocked";
}

const applyImage = () => {
  const pick = pickUrl();
  return showImage(pick.url, pick.id);
};

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

// Terminals (xterm.js). Its background is painted on an opaque canvas, so CSS
// cannot clear it; instead we frost the pane behind it, clear the wrappers in
// between, and make the xterm itself slightly translucent (--cb-term-opacity)
// so the frosted picture shows through. Coloured output stays readable.
function markTerminals(vw, vh) {
  for (const term of document.querySelectorAll(".xterm")) {
    if (!(term instanceof HTMLElement)) continue;
    term.setAttribute(TERM, "");
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

function run() {
  timer = 0;
  if (disposed) return;
  lastRun = Date.now();
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  if (!vw || !vh || !document.body) return;
  buildUI();
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
  showImage(url, id || "choisi").then(renderGrid);
}

let gridEl = null;
function renderGrid() {
  if (!gridEl) return;
  gridEl.textContent = "";
  const cur = currentUrl();
  for (const it of items()) {
    const cell = el("div", { className: "cb-thumb" });
    if (it.url === cur) cell.classList.add("cb-current");
    cell.style.backgroundImage = `url("${it.url}")`;
    cell.title = it.fixed ? "Image par défaut" : it.custom ? "Ajoutée" : it.id;
    cell.onclick = () => pickHere(it.url, it.id);
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
  // add tile
  const add = el("label", { className: "cb-thumb cb-add", title: "Ajouter une image" });
  add.appendChild(svg(["M12 5v14M5 12h14"], 22));
  const input = el("input", { type: "file", accept: "image/*" });
  input.style.display = "none";
  input.onchange = () => {
    const f = input.files && input.files[0];
    if (!f) return;
    const reader = new FileReader();
    reader.onload = () => {
      const url = String(reader.result || "");
      if (!url.startsWith("data:image/")) return;
      const id = `custom-${Date.now().toString(36)}`;
      sendCmd({ action: "add", id, name: f.name, dataUrl: url });
      pickHere(url, id); // shows it now; survives the loader's re-inject via the id
    };
    reader.readAsDataURL(f);
  };
  add.appendChild(input);
  gridEl.appendChild(add);
}

// A labelled slider that live-updates a CSS var and persists via the loader.
function slider(label, key, cssVar, min, max, step, value, format) {
  const row = el("label", { className: "cb-slider" });
  const top = el("div", { className: "cb-slider-top" });
  const out = el("span", { className: "cb-slider-val", textContent: format(value) });
  top.append(el("span", { textContent: label }), out);
  const input = el("input", { type: "range", min, max, step, value });
  input.oninput = () => {
    root.style.setProperty(cssVar, input.value);
    out.textContent = format(Number(input.value));
  };
  input.onchange = () => sendCmd({ action: "set", key, value: Number(input.value) });
  row.append(top, input);
  return row;
}
const pct = (v) => `${Math.round(v * 100)}%`;

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
    slider("Opacité", "imageOpacity", "--cb-image-opacity", 0.1, 1, 0.05, settings.imageOpacity ?? 1, pct),
    slider("Luminosité", "brightness", "--cb-brightness", 0.3, 1.6, 0.05, settings.brightness ?? 1, pct),
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
    const url = currentUrl();
    if (url) sendCmd({ action: "default", dataUrl: url });
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

function dispose() {
  disposed = true;
  clearTimeout(timer);
  observer.disconnect();
  window.removeEventListener("resize", schedule);
  for (const node of document.querySelectorAll(`[${CLEAR}], [${GLASS}], [${TERM}]`)) {
    node.removeAttribute(CLEAR);
    node.removeAttribute(GLASS);
    node.removeAttribute(TERM);
  }
  document.getElementById(UI_ID)?.remove();
  gridEl = null;
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
setTimeout(schedule, 800);
setTimeout(schedule, 3000);

await applyImage();
if (disposed) return null;
run();
return status();
