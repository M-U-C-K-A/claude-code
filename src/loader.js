// Claude Backdrop — loader (Electron main process).
//
// Appended to the end of Claude Desktop's main script inside app.asar. It never
// changes on its own: everything it applies is read from
// ~/Library/Application Support/ClaudeBackdrop/ and reloaded live when those
// files change (config.json, theme.css, custom.css, the picture).
//
// src/build.mjs inlines the version and the page script (src/page.js) below.
// Any failure is caught and logged: the loader must never break Claude.
;(function claudeBackdropLoader() {
  "use strict";
  try {
    const { app, webContents } = require("electron");
    const fs = require("fs");
    const os = require("os");
    const path = require("path");

    const VERSION = __CB_VERSION__;
    const PAGE_SCRIPT = __CB_PAGE_SCRIPT__;
    const DIR = process.env.CLAUDE_BACKDROP_DIR || path.join(os.homedir(), "Library", "Application Support", "ClaudeBackdrop");
    const file = (name) => path.join(DIR, name);
    const TARGET = /^https:\/\/([a-z0-9-]+\.)*claude\.(ai|com)(\/|$)/i;
    const MIME = { ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".gif": "image/gif", ".avif": "image/avif" };
    const MAX_IMAGE_BYTES = 16 * 1024 * 1024;

    const log = (...args) => {
      try {
        console.log("[claude-backdrop]", ...args);
      } catch {}
    };
    // Pages the theme applies to: claude.ai itself, and the about:blank windows
    // it opens ("open in new window" renders the chat into them).
    const isTarget = (url) => TARGET.test(url || "") || url === "about:blank";
    const stamp = (p) => {
      try {
        const s = fs.statSync(p);
        return `${s.mtimeMs}:${s.size}`;
      } catch {
        return "-";
      }
    };
    const readText = (p) => {
      try {
        return fs.readFileSync(p, "utf8");
      } catch {
        return "";
      }
    };
    const readConfig = () => {
      try {
        const cfg = JSON.parse(fs.readFileSync(file("config.json"), "utf8"));
        return cfg && typeof cfg === "object" ? cfg : {};
      } catch {
        return {};
      }
    };
    const clamp = (value, fallback, lo, hi) => {
      const n = Number(value);
      return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : fallback;
    };
    const imagePath = (cfg) => {
      const p = typeof cfg.image === "string" && cfg.image ? cfg.image : "background.jpg";
      return path.isAbsolute(p) ? p : path.join(DIR, p);
    };

    // Read a picture off disk and turn it into a data: URL, or "" if unusable.
    function dataUrl(file) {
      try {
        if (fs.statSync(file).size > MAX_IMAGE_BYTES) return "";
        const mime = MIME[path.extname(file).toLowerCase()] || "image/jpeg";
        return `data:${mime};base64,${fs.readFileSync(file).toString("base64")}`;
      } catch {
        return "";
      }
    }

    // The gallery on disk: <DIR>/gallery/<id>.jpg, each tagged dark/light by the
    // manifest that `claude-backdrop` writes next to them. The page picks one
    // per conversation, matching the current light/dark mode.
    function gallery() {
      let manifest = [];
      try {
        manifest = JSON.parse(fs.readFileSync(file(path.join("gallery", "manifest.json")), "utf8"));
      } catch {}
      const out = [];
      for (const entry of Array.isArray(manifest) ? manifest : []) {
        const url = dataUrl(file(path.join("gallery", `${entry.id}.jpg`)));
        if (url) out.push({ id: entry.id, mode: entry.mode === "light" ? "light" : "dark", url });
      }
      return out;
    }

    function stableTokens(cfg) {
      const dim = clamp(cfg.dim, 0.55, 0, 0.95);
      const glass = clamp(cfg.glass, 0.5, 0, 1);
      const blur = clamp(cfg.blur, 22, 0, 80);
      const imageBlur = clamp(cfg.imageBlur, 6, 0, 60);
      const position = typeof cfg.position === "string" && /^[a-z0-9 .%-]{1,40}$/i.test(cfg.position) ? cfg.position : "center";
      const size = typeof cfg.size === "string" && /^(cover|contain|auto|\d{1,4}(\.\d+)?(px|%))$/.test(cfg.size) ? cfg.size : "cover";
      let css = `:root{--cb-dim:${dim};--cb-glass:${glass};--cb-blur:${blur}px;--cb-image-blur:${imageBlur}px;--cb-position:${position};--cb-size:${size}}`;
      if (blur === 0) css += "\n.dframe-sidebar,[data-cb-glass]{-webkit-backdrop-filter:none!important;backdrop-filter:none!important}";
      return css;
    }

    // Everything to inject, rebuilt only when a file changed. The chosen image
    // is left to the page script (it knows the conversation and the mode); the
    // loader just hands it the fixed image and the gallery.
    let cache = null;
    function build() {
      const cfg = readConfig();
      const picture = imagePath(cfg);
      const galleryDir = file("gallery");
      const key = [
        stamp(file("config.json")),
        stamp(file("theme.css")),
        stamp(file("custom.css")),
        picture,
        stamp(picture),
        stamp(path.join(galleryDir, "manifest.json")),
      ].join("|");
      if (cache && cache.key === key) return cache;
      const enabled = cfg.enabled !== false;
      let css = "";
      let image = "off";
      let page = { version: VERSION };
      if (enabled) {
        const fixed = dataUrl(picture);
        const rotate = cfg.rotate === "off" ? "off" : "conversation";
        const gal = rotate === "off" ? [] : gallery();
        image =
          rotate === "off"
            ? fixed
              ? "loaded"
              : fs.existsSync(picture)
                ? "too-large"
                : "missing"
            : gal.length
              ? `gallery(${gal.length})`
              : fixed
                ? "loaded"
                : "missing";
        page = {
          version: VERSION,
          mode: cfg.mode || "dark",
          autoClear: cfg.autoClear !== false,
          rotate,
          fixed,
          gallery: gal,
        };
        css = `${readText(file("theme.css"))}\n${stableTokens(cfg)}\n${readText(file("custom.css"))}`;
      }
      cache = { key, enabled, css, image, page };
      return cache;
    }

    const applied = new Map(); // webContents id -> { key, cssKey, page, error, url }
    const queues = new Map(); // webContents id -> promise (one apply at a time)

    async function applyNow(wc, force) {
      if (!wc || wc.isDestroyed()) return;
      const url = wc.getURL();
      if (!isTarget(url)) return;
      const b = build();
      const previous = applied.get(wc.id);
      if (!force && previous && previous.key === b.key) return;
      const current = { key: b.key, url, cssKey: null, page: null, error: null };
      applied.set(wc.id, current);
      try {
        if (previous && previous.cssKey) {
          try {
            await wc.removeInsertedCSS(previous.cssKey);
          } catch {}
        }
        if (b.css) current.cssKey = await wc.insertCSS(b.css, { cssOrigin: "user" });
        const js = b.enabled
          ? `(async()=>{const CB=${JSON.stringify(b.page)};\n${PAGE_SCRIPT}\n})()`
          : "(()=>{try{window.__claudeBackdrop&&window.__claudeBackdrop.dispose()}catch(e){}return null})()";
        current.page = await wc.executeJavaScript(js, true);
      } catch (e) {
        current.error = String((e && e.message) || e);
      }
      scheduleStatus();
    }

    function apply(wc, force) {
      if (!wc || wc.isDestroyed()) return Promise.resolve();
      const id = wc.id;
      const next = (queues.get(id) || Promise.resolve()).then(() => applyNow(wc, force)).catch(log);
      queues.set(id, next);
      return next;
    }

    const applyAll = (force) => {
      for (const wc of webContents.getAllWebContents()) apply(wc, force);
    };

    let statusTimer = null;
    function scheduleStatus() {
      if (statusTimer) return;
      statusTimer = setTimeout(() => {
        statusTimer = null;
        writeStatus();
      }, 250);
    }
    function writeStatus() {
      try {
        const b = cache || build();
        const pages = [];
        for (const wc of webContents.getAllWebContents()) {
          const a = !wc.isDestroyed() && applied.get(wc.id);
          if (!a) continue;
          let where = a.url;
          try {
            const u = new URL(wc.getURL());
            where = u.origin === "null" ? u.href : u.origin + u.pathname.split("/").slice(0, 2).join("/");
          } catch {}
          pages.push({ id: wc.id, url: where, css: a.cssKey ? "inserted" : a.error ? "error" : "none", error: a.error || undefined, page: a.page });
        }
        const status = {
          loader: VERSION,
          at: new Date().toISOString(),
          app: app.getVersion(),
          electron: process.versions.electron,
          enabled: b.enabled,
          image: b.image,
          cssBytes: b.css.length,
          pages,
        };
        fs.writeFileSync(file("status.json"), `${JSON.stringify(status, null, 2)}\n`);
      } catch (e) {
        log("status", e);
      }
    }

    app.on("web-contents-created", (_event, wc) => {
      try {
        wc.on("dom-ready", () => apply(wc, true));
        wc.once("destroyed", () => {
          applied.delete(wc.id);
          queues.delete(wc.id);
        });
      } catch (e) {
        log(e);
      }
    });

    app
      .whenReady()
      .then(() => {
        applyAll(false);
        // Poll the files (5 stats every 1.5 s) and re-apply live on change.
        const timer = setInterval(() => {
          const before = cache && cache.key;
          if (build().key !== before) applyAll(false);
        }, 1500);
        if (timer.unref) timer.unref();
      })
      .catch(log);

    log(`loader ${VERSION} ready (${DIR})`);
  } catch (e) {
    try {
      console.error("[claude-backdrop]", e);
    } catch {}
  }
})();
