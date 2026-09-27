// Claude Backdrop — loader (Electron main process).
//
// Appended to the end of Claude Desktop's main script inside app.asar. It never
// changes on its own: everything it applies is read from
// ~/Library/Application Support/ClaudeBackdrop/ and reloaded live when those
// files change (config.json, theme.css, custom.css, the picture).
//
// claude-backdrop (src/embed.go) inlines the version and a copy of the page
// script (src/page.js) below. The page script actually run is the one in the
// support folder when there is one (claude-backdrop writes it there, like
// theme.css), so its fixes apply live, without reinstalling Claude.
// Any failure is caught and logged: the loader must never break Claude.
;(function claudeBackdropLoader() {
  "use strict";
  try {
    const { app, nativeImage, webContents } = require("electron");
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

    // The gallery on disk: <DIR>/gallery/<file>, listed in gallery/manifest.json
    // (written by `claude-backdrop` and by the panel commands below). The page
    // picks one per conversation.
    function readManifest() {
      try {
        const m = JSON.parse(fs.readFileSync(file(path.join("gallery", "manifest.json")), "utf8"));
        return Array.isArray(m) ? m : [];
      } catch {
        return [];
      }
    }

    // Pictures are big (up to 4K): the page gets a small preview of each, for
    // the panel and to paint something right away, and only the picture on
    // screen is sent in full afterwards (sendFull). Previews are cached by file.
    const PREVIEW_WIDTH = 640;
    const previews = new Map(); // path -> { key, url }
    function previewUrl(p) {
      const key = stamp(p);
      const hit = previews.get(p);
      if (hit && hit.key === key) return hit.url;
      let url = "";
      try {
        const img = nativeImage.createFromPath(p);
        if (!img.isEmpty()) {
          const small = img.getSize().width > PREVIEW_WIDTH ? img.resize({ width: PREVIEW_WIDTH, quality: "good" }) : img;
          url = `data:image/jpeg;base64,${small.toJPEG(82).toString("base64")}`;
        }
      } catch {}
      if (!url) url = dataUrl(p); // a format nativeImage can't read: send it as is
      previews.set(p, { key, url });
      return url;
    }

    const galleryFile = (entry) => file(path.join("gallery", path.basename(typeof entry.file === "string" ? entry.file : `${entry.id}.jpg`)));

    function gallery() {
      const out = [];
      const seen = new Set();
      for (const entry of readManifest()) {
        if (!entry || !entry.id || seen.has(entry.id)) continue;
        const p = galleryFile(entry);
        if (!fs.existsSync(p)) continue;
        const url = previewUrl(p);
        if (!url) continue;
        seen.add(entry.id);
        out.push({ id: entry.id, title: entry.title || entry.id, file: path.basename(p), url });
      }
      return out;
    }

    // The full-size file behind a picture id ("fixed" is config.image).
    function fullFile(id) {
      if (id === "fixed") return imagePath(readConfig());
      const entry = readManifest().find((e) => e && e.id === id);
      return entry ? galleryFile(entry) : "";
    }

    // Send the full-size picture to a page, which swaps it for the preview.
    // Resolves to the page's new status (null if nothing was sent).
    async function sendFull(wc, id) {
      if (!id || typeof id !== "string" || wc.isDestroyed()) return null;
      const p = fullFile(id);
      const url = p ? dataUrl(p) : "";
      if (!url) return null;
      const status = await wc.executeJavaScript(
        `(()=>{const a=window.__claudeBackdrop;return a&&a.setFull?a.setFull(${JSON.stringify(id)},${JSON.stringify(url)}).then(()=>a.status()):null})()`,
        true,
      );
      const a = applied.get(wc.id);
      if (a && status) {
        a.page = status;
        scheduleStatus();
      }
      return status;
    }

    // Live-tunable style tokens, from config (with defaults + clamps).
    const NUM = {
      dim: [0.55, 0, 0.95],
      glass: [0.5, 0, 1],
      blur: [22, 0, 80],
      imageBlur: [6, 0, 60],
      brightness: [1, 0.3, 1.6],
      imageOpacity: [1, 0.1, 1],
      terminalGlass: [0.6, 0, 1],
    };
    const num = (cfg, key) => clamp(cfg[key], NUM[key][0], NUM[key][1], NUM[key][2]);

    function stableTokens(cfg) {
      const position = typeof cfg.position === "string" && /^[a-z0-9 .%-]{1,40}$/i.test(cfg.position) ? cfg.position : "center";
      const size = typeof cfg.size === "string" && /^(cover|contain|auto|\d{1,4}(\.\d+)?(px|%))$/.test(cfg.size) ? cfg.size : "cover";
      let css =
        `:root{--cb-dim:${num(cfg, "dim")};--cb-glass:${num(cfg, "glass")};--cb-blur:${num(cfg, "blur")}px;` +
        `--cb-image-blur:${num(cfg, "imageBlur")}px;--cb-image-opacity:${num(cfg, "imageOpacity")};` +
        `--cb-brightness:${num(cfg, "brightness")};--cb-term-glass:${num(cfg, "terminalGlass")};` +
        `--cb-position:${position};--cb-size:${size}}`;
      if (num(cfg, "blur") === 0) css += "\n.dframe-sidebar,[data-cb-glass]{-webkit-backdrop-filter:none!important;backdrop-filter:none!important}";
      return css;
    }

    // ---- commands from the in-app gallery panel (localStorage 'cb-cmd') -------
    // The panel runs in the page (no fs). It leaves a command in localStorage;
    // the loader reads it here, persists the change, and every window re-applies.
    const EXT = { "image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif", "image/avif": ".avif" };
    const slug = (s) => String(s || "image").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 32) || "image";

    function writeConfigPatch(patch) {
      const cfg = readConfig();
      fs.writeFileSync(file("config.json"), `${JSON.stringify({ ...cfg, ...patch }, null, 2)}\n`);
    }

    // Decode a data: URL to bytes + extension. Returns null if not a data image.
    function decodeDataUrl(url) {
      const m = /^data:([^;,]+)(;base64)?,/.exec(url || "");
      if (!m) return null;
      const mime = m[1];
      const body = url.slice(m[0].length);
      const bytes = m[2] ? Buffer.from(body, "base64") : Buffer.from(decodeURIComponent(body), "binary");
      if (bytes.length > MAX_IMAGE_BYTES) throw new Error("image too large");
      return { bytes, ext: EXT[mime] || ".jpg" };
    }

    function handleCommand(cmd) {
      if (!cmd || typeof cmd !== "object") return;
      if (cmd.action === "rotate") {
        writeConfigPatch({ rotate: cmd.value === "off" ? "off" : "conversation" });
      } else if (cmd.action === "set" && typeof cmd.key === "string" && NUM[cmd.key]) {
        writeConfigPatch({ [cmd.key]: clamp(cmd.value, NUM[cmd.key][0], NUM[cmd.key][1], NUM[cmd.key][2]) });
      } else if (cmd.action === "add" && typeof cmd.dataUrl === "string") {
        const decoded = decodeDataUrl(cmd.dataUrl);
        if (!decoded) return;
        const galleryDir = file("gallery");
        fs.mkdirSync(galleryDir, { recursive: true });
        const id = /^custom-[a-z0-9-]{1,48}$/.test(cmd.id || "") ? cmd.id : `custom-${slug(cmd.name)}-${Date.now().toString(36)}`;
        const fileName = `${id}${decoded.ext}`;
        fs.writeFileSync(path.join(galleryDir, fileName), decoded.bytes);
        const manifest = readManifest().filter((e) => e.id !== id);
        manifest.push({ id, title: cmd.name || id, file: fileName, custom: true });
        fs.writeFileSync(path.join(galleryDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
        writeConfigPatch({ rotate: "conversation" });
      } else if (cmd.action === "delete" && typeof cmd.file === "string") {
        const name = path.basename(cmd.file);
        fs.rmSync(path.join(file("gallery"), name), { force: true });
        const manifest = readManifest().filter((e) => (e.file || `${e.id}.jpg`) !== name);
        fs.writeFileSync(file(path.join("gallery", "manifest.json")), `${JSON.stringify(manifest, null, 2)}\n`);
        // It was the fixed picture: back to the random pick.
        if (readConfig().image === `gallery/${name}`) writeConfigPatch({ rotate: "conversation" });
      } else if (cmd.action === "default" && typeof cmd.id === "string" && (cmd.id === "fixed" || readManifest().some((e) => e && e.id === cmd.id))) {
        // A picture of the gallery: point at its full-size file.
        const entry = readManifest().find((e) => e && e.id === cmd.id);
        writeConfigPatch(entry ? { image: `gallery/${path.basename(galleryFile(entry))}`, rotate: "off" } : { rotate: "off" });
      } else if (cmd.action === "default" && typeof cmd.dataUrl === "string") {
        const decoded = decodeDataUrl(cmd.dataUrl);
        if (!decoded) return;
        for (const old of fs.readdirSync(DIR).filter((n) => /^background\.[a-z0-9]+$/i.test(n))) fs.rmSync(file(old), { force: true });
        const name = `background${decoded.ext}`;
        fs.writeFileSync(file(name), decoded.bytes);
        writeConfigPatch({ image: name, rotate: "off" });
      }
    }

    let lastCmdTs = 0;
    async function pollCommands() {
      // cb-cmd (localStorage, shared by the windows) carries panel commands; a
      // window asks for the full-size picture it just switched to through
      // window.__claudeBackdrop.want (its own).
      const reader =
        "(()=>{var r={};try{var v=localStorage.getItem('cb-cmd');if(v)localStorage.removeItem('cb-cmd');r.cmd=v}catch(e){}" +
        "try{var a=window.__claudeBackdrop;if(a&&a.want){r.want=a.want;a.want=null}}catch(e){}return JSON.stringify(r)})()";
      for (const wc of webContents.getAllWebContents()) {
        if (wc.isDestroyed() || !isTarget(wc.getURL())) continue;
        let got;
        try {
          got = JSON.parse(await wc.executeJavaScript(reader, true));
        } catch {
          continue;
        }
        if (got.want) sendFull(wc, got.want).catch(log);
        if (!got.cmd) continue;
        let cmd;
        try {
          cmd = JSON.parse(got.cmd);
        } catch {
          continue;
        }
        if (!cmd || cmd.ts === lastCmdTs) continue;
        lastCmdTs = cmd.ts;
        try {
          handleCommand(cmd);
        } catch (e) {
          log("command", e);
        }
      }
    }

    // The page script: the support folder's copy, or the built-in one.
    function pageScript() {
      const text = readText(file("page.js"));
      return text.includes("__claudeBackdrop") ? text : PAGE_SCRIPT;
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
        stamp(file("page.js")),
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
        const fixed = fs.existsSync(picture) && fs.statSync(picture).size <= MAX_IMAGE_BYTES ? previewUrl(picture) : "";
        const rotate = cfg.rotate === "off" ? "off" : "conversation";
        // The gallery ships even with a fixed image: the panel lists it, and a
        // window can still switch to another picture.
        const gal = gallery();
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
          settings: { imageBlur: num(cfg, "imageBlur"), brightness: num(cfg, "brightness") },
        };
        css = `${readText(file("theme.css"))}\n${stableTokens(cfg)}\n${readText(file("custom.css"))}`;
      }
      cache = { key, enabled, css, image, page, script: pageScript() };
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
          ? `(async()=>{const CB=${JSON.stringify(b.page)};\n${b.script}\n})()`
          : "(()=>{try{window.__claudeBackdrop&&window.__claudeBackdrop.dispose()}catch(e){}return null})()";
        current.page = await wc.executeJavaScript(js, true);
        // The page shows a preview: follow up with the full-size picture,
        // unless it kept it from a previous run.
        const shown = current.page && current.page.painting;
        if (shown && shown !== "none" && !current.page.full) {
          const after = await sendFull(wc, shown);
          if (after) current.page = after;
        }
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
          pageScript: readText(file("page.js")).includes("__claudeBackdrop") ? "support" : "builtin",
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
        // Poll: pick up gallery-panel commands, then re-apply if a file changed.
        const timer = setInterval(() => {
          pollCommands()
            .catch(log)
            .finally(() => {
              const before = cache && cache.key;
              if (build().key !== before) applyAll(false);
            });
        }, 1200);
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
