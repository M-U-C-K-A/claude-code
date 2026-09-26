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

    function varsCss(cfg, imageUrl) {
      const dim = clamp(cfg.dim, 0.45, 0, 0.95);
      const glass = clamp(cfg.glass, 0.5, 0, 1);
      const blur = clamp(cfg.blur, 18, 0, 80);
      const position = typeof cfg.position === "string" && /^[a-z0-9 .%-]{1,40}$/i.test(cfg.position) ? cfg.position : "center";
      const size = typeof cfg.size === "string" && /^(cover|contain|auto|\d{1,4}(\.\d+)?(px|%))$/.test(cfg.size) ? cfg.size : "cover";
      let css = `:root{--cb-image:${imageUrl ? `url("${imageUrl}")` : "none"};--cb-dim:${dim};--cb-glass:${glass};--cb-blur:${blur}px;--cb-position:${position};--cb-size:${size}}`;
      if (blur === 0) css += "\n.dframe-sidebar,[data-cb-glass]{-webkit-backdrop-filter:none!important;backdrop-filter:none!important}";
      return css;
    }

    // Everything to inject, rebuilt only when one of the files changed.
    let cache = null;
    function build() {
      const cfg = readConfig();
      const picture = imagePath(cfg);
      const key = [stamp(file("config.json")), stamp(file("theme.css")), stamp(file("custom.css")), picture, stamp(picture)].join("|");
      if (cache && cache.key === key) return cache;
      const enabled = cfg.enabled !== false;
      let css = "";
      let image = "off";
      if (enabled) {
        let url = "";
        try {
          const size = fs.statSync(picture).size;
          if (size > MAX_IMAGE_BYTES) image = "too-large";
          else {
            url = `data:${MIME[path.extname(picture).toLowerCase()] || "image/jpeg"};base64,${fs.readFileSync(picture).toString("base64")}`;
            image = "loaded";
          }
        } catch {
          image = "missing";
        }
        css = `${readText(file("theme.css"))}\n${varsCss(cfg, url)}\n${readText(file("custom.css"))}`;
      }
      const page = { version: VERSION, mode: cfg.mode, autoClear: cfg.autoClear !== false };
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
