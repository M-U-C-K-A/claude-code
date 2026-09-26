// The background pictures: a small built-in gallery of public-domain paintings,
// plus any file or URL the user points at. Downloading and JPEG conversion run
// on the user's Mac (macOS `sips`), where the network is available; this
// container can't reach the museums.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { hasSips, toJpeg } from "./macos.mjs";

const commons = (file, width = 1800) =>
  `https://commons.wikimedia.org/wiki/Special:FilePath/${encodeURIComponent(file)}?width=${width}`;

// Built-in gallery of public-domain paintings. Several sources per entry: the
// first that downloads wins.
export const GALLERY = [
  {
    id: "socrates",
    title: "La Mort de Socrate — Jacques-Louis David, 1787",
    sources: [commons("David - The Death of Socrates.jpg"), "met:436105"],
  },
  {
    id: "horatii",
    title: "Le Serment des Horaces — Jacques-Louis David, 1784",
    sources: [
      commons("David-Oath_of_the_Horatii-1784.jpg"),
      commons("Jacques-Louis_David_-_Oath_of_the_Horatii_-_Google_Art_Project.jpg"),
      commons("Le_Serment_des_Horaces.jpg"),
    ],
  },
  {
    id: "pandemonium",
    title: "Pandemonium — John Martin, 1841",
    sources: [
      "search:John Martin Pandemonium 1841",
      commons("John_Martin_-_Pandemonium_-_Google_Art_Project.jpg"),
      commons("John_Martin_-_Pandemonium_-_WGA14140.jpg"),
    ],
  },
  {
    id: "school-of-athens",
    title: "L'École d'Athènes — Raphaël, 1511",
    sources: [
      commons("Raphael_School_of_Athens.jpg"),
      "search:Raphael School of Athens Stanza",
    ],
  },
];

export const galleryEntry = (id) => GALLERY.find((entry) => entry.id === id);
export const DEFAULT_IMAGE = GALLERY[0];

const USER_AGENT = "claude-backdrop/1.0 (https://github.com/M-U-C-K-A/claude-code)";
const EXTENSIONS = {
  "image/jpeg": ".jpg",
  "image/png": ".png",
  "image/webp": ".webp",
  "image/gif": ".gif",
  "image/avif": ".avif",
  "image/heic": ".heic",
};
const MAX_RAW_BYTES = 24 * 1024 * 1024;

// Resolve a Commons file-search into a real image URL: robust to exact
// filenames (which drift), because it searches the File namespace by words.
async function commonsSearch(query, width = 1800) {
  const api =
    `https://commons.wikimedia.org/w/api.php?action=query&format=json&origin=*` +
    `&generator=search&gsrnamespace=6&gsrlimit=8&gsrsearch=${encodeURIComponent(query)}` +
    `&prop=imageinfo&iiprop=url&iiurlwidth=${width}`;
  const res = await fetch(api, { headers: { "User-Agent": USER_AGENT } });
  if (!res.ok) throw new Error(`recherche Commons : HTTP ${res.status}`);
  const data = await res.json();
  const pages = data && data.query && data.query.pages ? Object.values(data.query.pages) : [];
  pages.sort((a, b) => (a.index || 99) - (b.index || 99));
  for (const page of pages) {
    const info = page.imageinfo && page.imageinfo[0];
    const url = info && (info.thumburl || info.url);
    if (url && /\.(jpe?g|png)$/i.test((info.url || url).split("?")[0])) return url;
  }
  throw new Error(`aucune image pour « ${query} »`);
}

async function fetchImage(source) {
  let url = source;
  if (url.startsWith("met:")) {
    const api = await fetch(`https://collectionapi.metmuseum.org/public/collection/v1/objects/${url.slice(4)}`, {
      headers: { "User-Agent": USER_AGENT },
    });
    if (!api.ok) throw new Error(`API du Met : HTTP ${api.status}`);
    const object = await api.json();
    url = object.primaryImage || object.primaryImageSmall;
    if (!url) throw new Error("API du Met : pas d'image");
  } else if (url.startsWith("search:")) {
    url = await commonsSearch(url.slice(7));
  }
  const response = await fetch(url, { headers: { "User-Agent": USER_AGENT }, redirect: "follow" });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  const type = (response.headers.get("content-type") || "").split(";")[0].trim();
  if (!type.startsWith("image/")) throw new Error(`réponse non-image (${type || "type inconnu"})`);
  return { bytes: Buffer.from(await response.arrayBuffer()), type };
}

// Download to a temporary file. `sources` are tried in order.
export async function download(sources) {
  const errors = [];
  for (const source of sources) {
    try {
      const { bytes, type } = await fetchImage(source);
      const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "claude-backdrop-")), `image${EXTENSIONS[type] || ".img"}`);
      fs.writeFileSync(file, bytes);
      return file;
    } catch (error) {
      errors.push(`  - ${String(source).slice(0, 80)}: ${error.message}`);
    }
  }
  throw new Error(`téléchargement impossible :\n${errors.join("\n")}`);
}

// Convert `input` into a JPEG at `dest` (longest side <= max). Falls back to a
// plain copy when `sips` is missing (no resize/convert).
export function convert(input, dest, max = 2200) {
  if (!fs.statSync(input).isFile()) throw new Error(`${input} n'est pas un fichier`);
  fs.mkdirSync(path.dirname(dest), { recursive: true });
  const staging = `${dest}.new`;
  let size = null;
  if (hasSips()) {
    size = toJpeg(input, staging, max);
  } else {
    if (fs.statSync(input).size > MAX_RAW_BYTES) throw new Error("image trop lourde (24 Mo max sans sips)");
    fs.copyFileSync(input, staging);
  }
  fs.renameSync(staging, dest);
  return { size, bytes: fs.statSync(dest).size };
}

// Store the chosen fixed image as background.jpg in the support folder; returns
// the file name to put in config.json. Removes older background.* files.
export function store(input, dir, max = 2560) {
  const name = hasSips() ? "background.jpg" : `background${path.extname(input).toLowerCase() || ".jpg"}`;
  const result = convert(input, path.join(dir, name), max);
  for (const old of fs.readdirSync(dir).filter((n) => /^background\.[a-z0-9]+$/i.test(n))) {
    if (old !== name) fs.rmSync(path.join(dir, old), { force: true });
  }
  return { name, ...result };
}

// Download every gallery painting into <dir>/gallery/<id>.jpg (skips those
// already there). Gallery images are kept smaller than the fixed one, because
// all of them ship to each page for the per-conversation pick. Returns a report.
export async function syncGallery(dir, { force = false, onStep } = {}) {
  const galleryDir = path.join(dir, "gallery");
  fs.mkdirSync(galleryDir, { recursive: true });
  const results = [];
  for (const entry of GALLERY) {
    const dest = path.join(galleryDir, `${entry.id}.jpg`);
    if (!force && fs.existsSync(dest)) {
      results.push({ id: entry.id, status: "present" });
      continue;
    }
    try {
      onStep?.(entry);
      const tmp = await download(entry.sources);
      convert(tmp, dest, 1600);
      results.push({ id: entry.id, status: "downloaded" });
    } catch (error) {
      results.push({ id: entry.id, status: "failed", error: error.message.split("\n")[0] });
    }
  }
  return results;
}
