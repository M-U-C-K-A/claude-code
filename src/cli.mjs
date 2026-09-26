#!/usr/bin/env node
// claude-backdrop — puts a background picture behind Claude Desktop (macOS).
// See README.md. User-facing messages are in French.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import readline from "node:readline/promises";
import vm from "node:vm";
import { inspectAsar, patchAsar, unpatchAsar, verifyAsar } from "./asar.mjs";
import { SRC, VERSION, buildLoader } from "./build.mjs";
import { GALLERY, download, galleryEntry, store, syncGallery } from "./image.mjs";
import * as mac from "./macos.mjs";

const SUPPORT = process.env.CLAUDE_BACKDROP_DIR || path.join(os.homedir(), "Library", "Application Support", "ClaudeBackdrop");
const CONFIG = path.join(SUPPORT, "config.json");
const STATUS = path.join(SUPPORT, "status.json");
const ENTITLEMENTS = path.join(SRC, "entitlements.plist");
const DEFAULT_APP = "/Applications/Claude.app";

const DEFAULTS = {
  enabled: true,
  image: "background.jpg",
  rotate: "conversation", // "conversation" = une image au hasard par conversation ; "off" = image fixe
  dim: 0.55,
  glass: 0.5,
  blur: 22,
  imageBlur: 6,
  position: "center",
  size: "cover",
  mode: "dark",
  autoClear: true,
};

const CUSTOM_CSS = `/* Tes propres règles CSS pour Claude Desktop, rechargées en direct.
 * claude-backdrop n'écrase jamais ce fichier (theme.css, lui, est remplacé
 * à chaque « claude-backdrop install »).
 *
 * Exemples :
 *
 *   Recadrer l'image sur le haut du tableau :
 *     :root { --cb-position: 50% 20% !important; }
 *
 *   Barre latérale opaque, sans verre :
 *     .dframe-sidebar { background-color: #141414 !important; backdrop-filter: none !important; }
 *
 *   Un calque reste opaque ? « claude-backdrop doctor » le liste ; ajoute ici :
 *     .sa-classe { background: transparent !important; }
 */
`;

// ---------------------------------------------------------------- output

const tty = process.stdout.isTTY;
const paint = (code) => (text) => (tty ? `\x1b[${code}m${text}\x1b[0m` : String(text));
const bold = paint("1");
const dim = paint("2");
const green = paint("32");
const yellow = paint("33");
const red = paint("31");
const say = (text = "") => console.log(text);
const step = (text) => say(`${bold("›")} ${text}`);
const ok = (text) => say(`${green("✓")} ${text}`);
const warn = (text) => say(`${yellow("!")} ${text}`);

class UserError extends Error {}
const fail = (message) => {
  throw new UserError(message);
};

// ---------------------------------------------------------------- arguments

function parseArgs(argv) {
  const opts = { _: [] };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--yes" || arg === "-y") opts.yes = true;
    else if (arg === "--no-app-backup") opts.noAppBackup = true;
    else if (arg === "--purge") opts.purge = true;
    else if (arg === "--no-launch") opts.noLaunch = true;
    else if (arg === "--app" || arg === "--image" || arg === "--asar") {
      if (i + 1 >= argv.length) fail(`${arg} attend une valeur`);
      opts[arg.slice(2)] = argv[(i += 1)];
    } else if (arg === "--help" || arg === "-h") opts.help = true;
    else if (arg.startsWith("--")) fail(`option inconnue : ${arg}`);
    else opts._.push(arg);
  }
  return opts;
}

const appPath = (opts) => path.resolve(opts.app || process.env.CLAUDE_APP || DEFAULT_APP);

function requireMac() {
  if (process.platform !== "darwin") fail("Cette commande ne tourne que sur macOS (là où est installé Claude Desktop).");
}

function requireApp(app) {
  if (!fs.existsSync(path.join(app, "Contents", "Resources", "app.asar"))) {
    fail(`Claude Desktop introuvable dans ${app}.\nInstalle-le depuis https://claude.ai/download, ou indique son chemin avec --app.`);
  }
}

async function confirm(question, opts) {
  if (opts.yes) return;
  if (!process.stdin.isTTY) fail("Confirmation impossible hors terminal : relance avec --yes.");
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const answer = (await rl.question(`${question} [o/N] `)).trim().toLowerCase();
  rl.close();
  if (!["o", "oui", "y", "yes"].includes(answer)) fail("Annulé, rien n'a été modifié.");
}

// ---------------------------------------------------------------- support folder

function readConfig() {
  try {
    const cfg = JSON.parse(fs.readFileSync(CONFIG, "utf8"));
    return cfg && typeof cfg === "object" ? cfg : {};
  } catch {
    return {};
  }
}

function writeConfig(cfg) {
  const tmp = `${CONFIG}.tmp`;
  fs.writeFileSync(tmp, `${JSON.stringify(cfg, null, 2)}\n`);
  fs.renameSync(tmp, CONFIG);
}

function ensureSupport() {
  fs.mkdirSync(SUPPORT, { recursive: true, mode: 0o700 });
  fs.copyFileSync(path.join(SRC, "theme.css"), path.join(SUPPORT, "theme.css"));
  const custom = path.join(SUPPORT, "custom.css");
  if (!fs.existsSync(custom)) fs.writeFileSync(custom, CUSTOM_CSS);
  writeConfig({ ...DEFAULTS, ...readConfig() });
}

function currentImage() {
  const cfg = readConfig();
  const name = typeof cfg.image === "string" && cfg.image ? cfg.image : DEFAULTS.image;
  const file = path.isAbsolute(name) ? name : path.join(SUPPORT, name);
  return fs.existsSync(file) ? file : null;
}

// Rewrite gallery/manifest.json from whichever paintings actually downloaded,
// so the loader offers only the ones present, each with its light/dark tag.
function writeGalleryManifest() {
  const galleryDir = path.join(SUPPORT, "gallery");
  const manifest = GALLERY.filter((entry) => fs.existsSync(path.join(galleryDir, `${entry.id}.jpg`))).map((entry) => ({
    id: entry.id,
    mode: entry.mode,
    title: entry.title,
  }));
  fs.mkdirSync(galleryDir, { recursive: true });
  fs.writeFileSync(path.join(galleryDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  return manifest;
}

// Download the gallery (idempotent) and refresh the manifest.
async function ensureGallery(force = false) {
  fs.mkdirSync(SUPPORT, { recursive: true, mode: 0o700 });
  const results = await syncGallery(SUPPORT, { force, onStep: (e) => step(`Téléchargement : ${e.title}`) });
  const manifest = writeGalleryManifest();
  for (const r of results.filter((r) => r.status === "failed")) {
    warn(`Tableau indisponible (${r.id}) : ${r.error}`);
  }
  return manifest;
}

// Set a FIXED background image (turns rotation off): a gallery id, a file, or a URL.
async function setImage(spec) {
  let input;
  let label = spec;
  const entry = spec && galleryEntry(spec.toLowerCase());
  if (!spec || ["socrate", "socrates", "defaut", "défaut", "default"].includes(String(spec).toLowerCase())) {
    step(`Téléchargement de l'image par défaut : ${GALLERY[0].title}`);
    input = await download(GALLERY[0].sources);
    label = GALLERY[0].title;
  } else if (entry) {
    step(`Téléchargement : ${entry.title}`);
    input = await download(entry.sources);
    label = entry.title;
  } else if (/^https?:\/\//i.test(spec)) {
    step(`Téléchargement de ${spec}`);
    input = await download([spec]);
  } else {
    input = path.resolve(spec.replace(/^~(?=\/)/, os.homedir()));
    if (!fs.existsSync(input)) fail(`Fichier introuvable : ${input}`);
  }
  fs.mkdirSync(SUPPORT, { recursive: true, mode: 0o700 });
  const saved = store(input, SUPPORT);
  writeConfig({ ...DEFAULTS, ...readConfig(), image: saved.name, rotate: "off" });
  const size = saved.size ? `${saved.size[0]}×${saved.size[1]}, ` : "";
  ok(`Image fixe : ${label} ${dim(`(${size}${Math.round(saved.bytes / 1024)} Ko)`)}`);
  say(dim("Rotation par conversation désactivée. Pour la réactiver : claude-backdrop set rotate on"));
}

// ---------------------------------------------------------------- app state

function readAppState(app) {
  const p = mac.paths(app);
  const archive = fs.readFileSync(p.asar);
  const info = inspectAsar(archive);
  const plists = mac.integrityPlists(app);
  const signature = mac.signatureInfo(app);
  return {
    p,
    archive,
    info,
    plists,
    signature,
    version: mac.appVersion(app),
    hashOk: plists.length > 0 && plists.every((entry) => entry.hash === info.headerHash),
  };
}

function checkWritable(dir) {
  const probe = path.join(dir, `.claude-backdrop-${process.pid}`);
  try {
    fs.writeFileSync(probe, "");
    fs.rmSync(probe, { force: true });
  } catch (error) {
    if (error.code === "EPERM") {
      fail(
        "macOS empêche ce terminal de modifier les applications.\n" +
          "  Réglages Système › Confidentialité et sécurité › Gestion des apps (App Management)\n" +
          "  → active ton terminal (Terminal, iTerm, Ghostty…), puis relance la commande.",
      );
    }
    if (error.code === "EACCES") {
      fail(`${dir} ne t'appartient pas (Claude installé par un administrateur ?).\n  Donne-toi les droits : sudo chown -R "$(whoami)" "${path.dirname(path.dirname(dir))}"`);
    }
    throw error;
  }
}

function writeAtomic(file, data) {
  const tmp = `${file}.claude-backdrop-tmp`;
  fs.writeFileSync(tmp, data);
  fs.renameSync(tmp, file);
}

const backupDir = (version) => path.join(SUPPORT, "backups", version);

function bundleSizeMB(app) {
  let total = 0;
  const walk = (dir) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else if (entry.isFile()) total += fs.statSync(full).size;
    }
  };
  try {
    walk(app);
  } catch {}
  return Math.round(total / 1024 / 1024);
}

// Two layers of backup per Claude version:
//  - the original app.asar (small, always), enough to take the loader out;
//  - a full copy of Claude.app while it still has Anthropic's signature, so
//    `restore` can bring back the exact original (auto-updates included).
function backup(app, state, opts) {
  const dir = backupDir(state.version);
  fs.mkdirSync(dir, { recursive: true });
  const asarCopy = path.join(dir, "app.asar");
  if (!fs.existsSync(asarCopy)) {
    const original = state.info.loaderTag ? unpatchAsar(state.archive).archive : state.archive;
    writeAtomic(asarCopy, original);
  }
  const full = path.join(dir, "Claude.app");
  if (!opts.noAppBackup && !fs.existsSync(full)) {
    if (state.signature.kind === "developer-id" && state.signature.valid) {
      step(`Copie de sauvegarde de Claude.app (~${bundleSizeMB(app)} Mo) ${dim(`→ ${dir}`)}`);
      const partial = `${full}.partial`;
      fs.rmSync(partial, { recursive: true, force: true });
      mac.copyBundle(app, partial);
      fs.renameSync(partial, full);
    } else {
      warn("Claude.app n'a plus sa signature d'origine : pas de copie complète (seul app.asar est sauvegardé).");
    }
  }
  // Keep only the backup of the installed version.
  for (const other of fs.readdirSync(path.dirname(dir))) {
    if (other !== state.version) fs.rmSync(path.join(path.dirname(dir), other), { recursive: true, force: true });
  }
  return dir;
}

// ---------------------------------------------------------------- commands

async function cmdInstall(opts) {
  requireMac();
  const app = appPath(opts);
  requireApp(app);
  const loader = buildLoader();
  const state = readAppState(app);
  say(bold(`Claude Backdrop ${VERSION}`) + dim(` — Claude ${state.version}, ${app}`));

  step("Fichiers du thème");
  ensureSupport();
  if (opts.image) await setImage(opts.image);
  const cfg = { ...DEFAULTS, ...readConfig() };
  if (cfg.rotate !== "off") {
    step("Galerie de tableaux (une image au hasard par conversation)");
    const manifest = await ensureGallery();
    if (!manifest.length) warn("Aucun tableau téléchargé ; le thème marche, mais sans rotation. Réessaie : claude-backdrop gallery");
    else ok(`${manifest.length} tableau(x) prêt(s) : ${manifest.map((m) => m.id).join(", ")}`);
  } else if (!currentImage()) {
    try {
      await setImage("socrate");
    } catch (error) {
      warn(`Image par défaut indisponible (${error.message.split("\n")[0]}).`);
      warn("Le thème marche quand même ; choisis une image avec : claude-backdrop image ~/chemin/vers/image.jpg");
    }
  }
  ok(`Thème prêt dans ${dim(SUPPORT)}`);

  if (state.plists.length === 0) {
    fail("Aucun ElectronAsarIntegrity dans les Info.plist de Claude : structure inattendue, je n'y touche pas.");
  }
  if (state.info.loaderTag === loader.tag && state.hashOk && state.signature.valid) {
    ok("Le loader est déjà installé et à jour. Le thème se recharge tout seul, rien à redémarrer.");
    return;
  }

  step("Essai du patch sur une copie en mémoire");
  const patched = patchAsar(state.archive, loader);
  const files = verifyAsar(patched.archive);
  if (inspectAsar(patched.archive).loaderTag !== loader.tag) fail("le loader n'apparaît pas dans l'archive patchée");
  new vm.Script(loader.code, { filename: "claude-backdrop-loader.js" });
  ok(`Archive valide (${files} fichiers vérifiés, point d'entrée ${state.info.mainPath})`);

  if (mac.runningInside(app)) {
    fail("Cette commande tourne dans un terminal ouvert DANS Claude : le fermer la tuerait en plein milieu.\n  Lance-la depuis Terminal.app, iTerm, Ghostty…");
  }
  checkWritable(state.p.resources);

  say();
  say("Claude va être fermé, son app.asar reçoit le loader, puis l'app est re-signée localement (ad-hoc).");
  say(dim("Tant que le thème est installé, les mises à jour automatiques de Claude échouent : voir le README."));
  await confirm("Continuer ?", opts);

  const saved = backup(app, state, opts);
  ok(`Sauvegarde : ${dim(saved)}`);

  step("Fermeture de Claude");
  const wasRunning = await mac.quit(app);
  const originalPlists = state.plists.map((entry) => ({ ...entry, bytes: fs.readFileSync(entry.plist) }));
  try {
    if (patched.changed) {
      step("Installation du loader dans app.asar");
      writeAtomic(state.p.asar, patched.archive);
    }
    step("Mise à jour de l'empreinte d'intégrité (Info.plist)");
    for (const entry of state.plists) mac.setIntegrity(entry.plist, patched.headerHash);
    step("Signature locale (ad-hoc)");
    mac.resign(app, ENTITLEMENTS);
  } catch (error) {
    warn(`Échec : ${error.stderr?.toString().trim() || error.message}`);
    step("Retour à l'état précédent");
    rollback(app, state, originalPlists);
    throw new UserError("Installation annulée, Claude est revenu à son état précédent.");
  }
  ok("Loader installé, intégrité et signature à jour.");

  if (!opts.noLaunch) {
    step(wasRunning ? "Relance de Claude" : "Ouverture de Claude");
    mac.launch(app);
  }
  say();
  ok(bold("C'est fait."));
  say(`  Changer d'image :   ${bold("claude-backdrop image ~/Images/tableau.jpg")}`);
  say(`  Régler le rendu :   ${bold("claude-backdrop set dim 0.6")}   ${dim("(dim, glass, blur, position, mode…)")}`);
  say(`  Vérifier :          ${bold("claude-backdrop doctor")}`);
  say(dim("  Au premier lancement, macOS peut redemander le trousseau (« Claude Safe Storage » → Toujours autoriser)"));
  say(dim("  et les autorisations micro / écran : c'est la nouvelle signature locale, une seule fois."));
}

function rollback(app, state, originalPlists) {
  const full = path.join(backupDir(state.version), "Claude.app");
  try {
    if (fs.existsSync(full)) {
      replaceBundle(app, full);
      ok("Claude.app d'origine remis en place.");
      return;
    }
    writeAtomic(state.p.asar, state.archive);
    for (const entry of originalPlists) fs.writeFileSync(entry.plist, entry.bytes);
    if (!mac.signatureInfo(app).valid) mac.resign(app, ENTITLEMENTS);
    ok("app.asar et Info.plist d'origine remis en place.");
  } catch (error) {
    warn(`Retour arrière incomplet (${error.message}). Réinstalle Claude depuis https://claude.ai/download.`);
  }
}

// Swap in a full copy of the bundle: copy next to the app, then two renames.
function replaceBundle(app, source) {
  const parent = path.dirname(app);
  const incoming = path.join(parent, ".Claude.app.claude-backdrop-incoming");
  const outgoing = path.join(parent, ".Claude.app.claude-backdrop-outgoing");
  fs.rmSync(incoming, { recursive: true, force: true });
  fs.rmSync(outgoing, { recursive: true, force: true });
  mac.copyBundle(source, incoming);
  fs.renameSync(app, outgoing);
  fs.renameSync(incoming, app);
  fs.rmSync(outgoing, { recursive: true, force: true });
}

async function cmdRestore(opts) {
  requireMac();
  const app = appPath(opts);
  requireApp(app);
  const state = readAppState(app);
  const full = path.join(backupDir(state.version), "Claude.app");
  const asarCopy = path.join(backupDir(state.version), "app.asar");

  if (!state.info.loaderTag && state.signature.kind === "developer-id" && state.signature.valid) {
    ok(`Claude ${state.version} est déjà d'origine, rien à restaurer.`);
  } else {
    if (mac.runningInside(app)) fail("Lance cette commande depuis un terminal hors de Claude (Terminal.app, iTerm…).");
    checkWritable(state.p.resources);
    await confirm(`Claude va être fermé et remis dans son état d'origine (${state.version}). Continuer ?`, opts);
    step("Fermeture de Claude");
    await mac.quit(app);
    if (fs.existsSync(full) && mac.signatureInfo(full).valid) {
      step("Remise en place de la copie d'origine de Claude.app");
      replaceBundle(app, full);
      ok("Claude d'origine restauré, signature d'Anthropic comprise : les mises à jour automatiques remarchent.");
    } else {
      step("Retrait du loader");
      const original = fs.existsSync(asarCopy) ? fs.readFileSync(asarCopy) : unpatchAsar(state.archive).archive;
      writeAtomic(state.p.asar, original);
      const hash = inspectAsar(original).headerHash;
      for (const entry of state.plists) mac.setIntegrity(entry.plist, hash);
      mac.resign(app, ENTITLEMENTS);
      ok("Loader retiré.");
      warn("Sans copie complète, la signature reste locale : pour retrouver celle d'Anthropic (et les mises à jour");
      warn("automatiques), réinstalle Claude depuis https://claude.ai/download.");
    }
    if (!opts.noLaunch) mac.launch(app);
  }
  if (opts.purge) {
    fs.rmSync(SUPPORT, { recursive: true, force: true });
    ok(`Dossier ${SUPPORT} supprimé.`);
  }
}

async function cmdImage(opts) {
  const ids = GALLERY.map((e) => e.id).join(", ");
  if (!opts._[1]) fail(`Usage : claude-backdrop image <fichier | url | ${ids}>`);
  ensureSupport();
  await setImage(opts._[1]);
  say(dim("Claude recharge l'image tout seul (quelques secondes)."));
}

async function cmdGallery(opts) {
  const sub = opts._[1];
  say(bold("Galerie de tableaux") + dim("  (une image au hasard par conversation)"));
  for (const entry of GALLERY) {
    const present = fs.existsSync(path.join(SUPPORT, "gallery", `${entry.id}.jpg`));
    const tag = entry.mode === "light" ? yellow("clair") : dim("sombre");
    say(`  ${present ? green("●") : dim("○")} ${entry.id.padEnd(16)} ${tag}  ${dim(entry.title)}`);
  }
  if (sub === "list") return;
  if (sub && sub !== "sync") fail("Usage : claude-backdrop gallery [sync]");
  ensureSupport();
  say();
  const manifest = await ensureGallery(sub === "sync");
  writeConfig({ ...DEFAULTS, ...readConfig(), rotate: "conversation" });
  ok(`Rotation activée, ${manifest.length} tableau(x) disponible(s).`);
}

const SETTINGS = {
  rotate: {
    help: "on = une image au hasard par conversation ; off = image fixe",
    parse: (v) => ({ on: "conversation", conversation: "conversation", off: "off", fixe: "off" })[v] ?? null,
  },
  dim: { help: "assombrissement de l'image, 0 à 0.95", parse: (v) => number(v, 0, 0.95) },
  imageblur: { help: "flou de l'image de fond en px, 0 à 60", parse: (v) => number(v, 0, 60), key: "imageBlur" },
  glass: { help: "opacité du verre (barre latérale, panneaux, terminal), 0 à 1", parse: (v) => number(v, 0, 1) },
  blur: { help: "flou du verre en px, 0 à 80 (0 = sans flou)", parse: (v) => number(v, 0, 80) },
  position: { help: "cadrage : center, top, bottom, « 50% 30% »…", parse: (v) => (/^[a-z0-9 .%-]{1,40}$/i.test(v) ? v : null) },
  size: { help: "cover (remplit la fenêtre) ou contain", parse: (v) => (["cover", "contain"].includes(v) ? v : null) },
  mode: { help: "auto, dark ou light : suit le thème de Claude (auto) ou le force", parse: (v) => (["auto", "dark", "light"].includes(v) ? v : null) },
  autoClear: { help: "true/false : détection auto des calques opaques", parse: (v) => ({ true: true, false: false, on: true, off: false })[v] ?? null },
};

function number(value, lo, hi) {
  const n = Number(String(value).replace(",", "."));
  return Number.isFinite(n) && n >= lo && n <= hi ? n : null;
}

function cmdSet(opts) {
  const [, key, ...rest] = opts._;
  const cfg = { ...DEFAULTS, ...readConfig() };
  if (!key) {
    say(bold("Réglages") + dim(`  (${CONFIG})`));
    for (const [name, spec] of Object.entries(SETTINGS)) {
      say(`  ${name.padEnd(10)} ${String(cfg[spec.key || name]).padEnd(12)} ${dim(spec.help)}`);
    }
    say(dim("\nExemple : claude-backdrop set dim 0.6"));
    return;
  }
  const spec = SETTINGS[key.toLowerCase()];
  if (!spec) fail(`Réglage inconnu : ${key}. Possibles : ${Object.keys(SETTINGS).join(", ")}`);
  const value = spec.parse(rest.join(" ").trim());
  if (value === null || value === undefined || rest.length === 0) fail(`Valeur invalide pour ${key} : ${spec.help}`);
  ensureSupport();
  writeConfig({ ...cfg, [spec.key || key.toLowerCase()]: value });
  ok(`${key} = ${value} ${dim("(appliqué en direct)")}`);
}

function cmdToggle(enabled) {
  ensureSupport();
  writeConfig({ ...DEFAULTS, ...readConfig(), enabled });
  ok(enabled ? "Thème activé." : "Thème désactivé (le loader reste installé ; « claude-backdrop on » pour le remettre).");
}

function readStatus() {
  try {
    return JSON.parse(fs.readFileSync(STATUS, "utf8"));
  } catch {
    return null;
  }
}

function printPages(status) {
  if (!status) {
    say(`  ${dim("pas encore de rapport : ouvre Claude (le loader écrit status.json au chargement)")}`);
    return;
  }
  const age = Math.round((Date.now() - Date.parse(status.at)) / 1000);
  say(`  rapport du loader ${status.loader}, il y a ${age} s — image ${status.image}, thème ${status.enabled ? "actif" : "désactivé"}`);
  for (const page of status.pages || []) {
    const p = page.page || {};
    const image = { data: green("affichée"), blob: green("affichée (blob)"), url: green("affichée"), blocked: red("bloquée par la CSP"), none: yellow("aucune") }[p.image] || p.image || "?";
    say(`  • ${page.url}  css ${page.css === "inserted" ? green("ok") : red(page.css)}  image ${image}${p.painting && p.painting !== "none" ? dim(` [${p.painting}]`) : ""}`);
    say(`    ${dim(`mode ${p.mode ?? "?"} · transparents ${p.cleared ?? "?"} · verre ${p.glass ?? "?"} · terminaux ${p.terminals ?? 0}`)}`);
    if (page.error) say(`    ${red(page.error)}`);
    for (const layer of (p.opaque || []).slice(0, 6)) {
      const name = `${layer.tag}${layer.id ? `#${layer.id}` : ""}${layer.class ? `.${layer.class.split(" ").join(".")}` : ""}`;
      say(`    ${yellow("opaque")} ${name} ${dim(`${layer.background}, ${Math.round(layer.share * 100)} % de la fenêtre`)}`);
    }
  }
}

function cmdStatus(opts) {
  const app = appPath(opts);
  const loader = buildLoader();
  say(bold(`Claude Backdrop ${VERSION}`));
  if (process.platform === "darwin" && fs.existsSync(mac.paths(app).asar)) {
    const state = readAppState(app);
    const tag = state.info.loaderTag;
    const loaderText = !tag ? yellow("non installé") : tag === loader.tag ? green(`installé (${tag})`) : yellow(`ancienne version (${tag}) : relance « claude-backdrop install »`);
    say(`  Claude ${state.version} ${dim(app)}${mac.isRunning(app) ? "" : dim(" (fermé)")}`);
    say(`  loader        ${loaderText}`);
    say(`  intégrité     ${state.hashOk ? green("ok") : red("Info.plist ne correspond pas à app.asar")}`);
    say(`  signature     ${state.signature.kind}${state.signature.valid ? "" : red(" (invalide)")}`);
    const backups = path.join(SUPPORT, "backups");
    const versions = fs.existsSync(backups) ? fs.readdirSync(backups) : [];
    say(`  sauvegardes   ${versions.length ? versions.map((v) => `${v}${fs.existsSync(path.join(backups, v, "Claude.app")) ? " (app complète)" : ""}`).join(", ") : "aucune"}`);
  } else {
    say(`  ${dim(`Claude Desktop introuvable (${app})`)}`);
  }
  const cfg = { ...DEFAULTS, ...readConfig() };
  const galleryDir = path.join(SUPPORT, "gallery");
  const present = GALLERY.filter((e) => fs.existsSync(path.join(galleryDir, `${e.id}.jpg`))).map((e) => e.id);
  const image = currentImage();
  say(`  dossier       ${SUPPORT}`);
  if (cfg.rotate !== "off") {
    say(`  images        ${green("rotation par conversation")} — ${present.length ? present.join(", ") : yellow("galerie vide (claude-backdrop gallery)")}`);
  } else {
    say(`  image         ${green("fixe")} — ${image ? `${path.basename(image)} (${Math.round(fs.statSync(image).size / 1024)} Ko)` : yellow("aucune")}`);
  }
  say(`  réglages      ${Object.keys(SETTINGS).map((k) => `${k}=${cfg[SETTINGS[k].key || k]}`).join("  ")}${cfg.enabled ? "" : yellow("  (désactivé)")}`);
  say(bold("Dans Claude"));
  printPages(readStatus());
}

async function cmdDoctor(opts) {
  if (!fs.existsSync(CONFIG)) fail("Rien d'installé : lance d'abord « claude-backdrop install ».");
  const before = Date.now();
  writeConfig({ ...DEFAULTS, ...readConfig(), refresh: before });
  step("Nouvelle analyse demandée à Claude…");
  let status = null;
  for (let i = 0; i < 40; i += 1) {
    await new Promise((resolve) => setTimeout(resolve, 250));
    const current = readStatus();
    if (current && Date.parse(current.at) >= before && (current.pages || []).every((page) => page.page || page.error)) {
      status = current;
      break;
    }
  }
  if (!status) warn("Pas de réponse de Claude en 10 s : est-il ouvert, et le loader installé (« claude-backdrop status ») ?");
  cmdStatus(opts);
  const blocked = (status?.pages || []).some((page) => page.page?.image === "blocked");
  const opaque = (status?.pages || []).some((page) => (page.page?.opaque || []).length > 0);
  if (blocked) warn("L'image est refusée par la politique de sécurité (CSP) de la page : ouvre une issue avec ce rapport.");
  if (opaque) say(dim(`Calques opaques restants : rends-les transparents dans ${path.join(SUPPORT, "custom.css")}.`));
}

function cmdSelftest(opts) {
  const file = opts.asar ? path.resolve(opts.asar) : mac.paths(appPath(opts)).asar;
  if (!fs.existsSync(file)) fail(`Archive introuvable : ${file}`);
  const archive = fs.readFileSync(file);
  const loader = buildLoader();
  const before = inspectAsar(archive);
  const patched = patchAsar(archive, loader);
  const files = verifyAsar(patched.archive);
  const after = inspectAsar(patched.archive);
  if (after.loaderTag !== loader.tag) fail("le loader n'apparaît pas après patch");
  const again = patchAsar(patched.archive, loader);
  if (again.changed) fail("le patch n'est pas idempotent");
  const restored = unpatchAsar(patched.archive);
  if (inspectAsar(restored.archive).loaderTag) fail("le loader ne s'enlève pas");
  if (!before.loaderTag && !restored.archive.equals(archive) && inspectAsar(restored.archive).headerHash !== before.headerHash) {
    fail("l'archive restaurée diffère de l'originale");
  }
  new vm.Script(loader.code, { filename: "claude-backdrop-loader.js" });
  ok(`selftest ok — ${file}`);
  say(`  point d'entrée ${after.mainPath}, ${files} fichiers vérifiés, loader ${loader.tag}`);
  say(`  empreinte d'en-tête : ${before.headerHash.slice(0, 16)}… → ${after.headerHash.slice(0, 16)}…`);
}

const HELP = `${bold("claude-backdrop")} — une image de fond derrière Claude Desktop (macOS)

  ${bold("install")} [--image <fichier|url>] [--app <chemin>] [--no-app-backup] [--yes]
      installe le loader dans Claude.app (sauvegarde, patch, signature locale, relance)
  ${bold("image")} <fichier | url | id>          fixe une image (${GALLERY.map((e) => e.id).join(", ")}), coupe la rotation
  ${bold("gallery")} [sync]                       galerie des tableaux + (ré)active l'image au hasard par conversation
  ${bold("set")} [<réglage> <valeur>]            rotate, dim, imageblur, glass, blur, position, size, mode, autoClear
  ${bold("on")} | ${bold("off")}                             active / désactive le thème sans rien désinstaller
  ${bold("status")}                              état du patch, de la signature et du thème dans Claude
  ${bold("doctor")}                              relance l'analyse dans Claude et liste les calques opaques
  ${bold("restore")} [--purge]                   remet Claude d'origine (--purge : supprime aussi réglages et images)
  ${bold("selftest")} [--asar <fichier>]         essaie le patch sur une copie, sans rien modifier

Fichiers : ${SUPPORT}
  config.json, theme.css (remplacé à chaque install), custom.css (à toi),
  background.jpg (image fixe), gallery/ (tableaux de la rotation)`;

async function main() {
  const major = Number(process.versions.node.split(".")[0]);
  if (major < 18) fail(`Node.js 18 ou plus est requis (tu as ${process.versions.node}) : brew install node`);
  const opts = parseArgs(process.argv.slice(2));
  const command = opts._[0];
  if (opts.help || !command || command === "help") return say(HELP);
  const commands = {
    install: () => cmdInstall(opts),
    image: () => cmdImage(opts),
    gallery: () => cmdGallery(opts),
    set: () => cmdSet(opts),
    on: () => cmdToggle(true),
    off: () => cmdToggle(false),
    status: () => cmdStatus(opts),
    doctor: () => cmdDoctor(opts),
    restore: () => cmdRestore(opts),
    uninstall: () => cmdRestore(opts),
    selftest: () => cmdSelftest(opts),
  };
  if (!commands[command]) fail(`Commande inconnue : ${command}\n\n${HELP}`);
  await commands[command]();
}

main().catch((error) => {
  if (error instanceof UserError) console.error(`${red("✗")} ${error.message}`);
  else if (error?.code === "EPERM") console.error(`${red("✗")} Permission refusée par macOS (${error.path || error.message}). Voir « Gestion des apps » dans le README.`);
  else console.error(`${red("✗")} ${error?.stderr?.toString().trim() || error?.stack || error}`);
  process.exit(1);
});
