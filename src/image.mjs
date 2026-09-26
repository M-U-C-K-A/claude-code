// The background picture: download it (the default painting or any URL),
// convert it to a reasonably sized JPEG with macOS `sips`, and store it in the
// support folder, where the loader picks it up live.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { hasSips, toJpeg } from "./macos.mjs";

// Jacques-Louis David, "The Death of Socrates" (1787), The Metropolitan Museum
// of Art, public domain — the painting of the reference screenshot.
export const DEFAULT_IMAGE = {
  title: "La Mort de Socrate — Jacques-Louis David, 1787 (The Met, domaine public)",
  sources: [
    "https://commons.wikimedia.org/wiki/Special:FilePath/David_-_The_Death_of_Socrates.jpg?width=2560",
    "met:436105",
  ],
};

const USER_AGENT = "claude-backdrop/1.0 (https://github.com/M-U-C-K-A/claude-code)";
const EXTENSIONS = {
  "image/jpeg": ".jpg",
  "image/png": ".png",
  "image/webp": ".webp",
  "image/gif": ".gif",
  "image/avif": ".avif",
  "image/heic": ".heic",
};
const MAX_RAW_BYTES = 16 * 1024 * 1024;

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
  }
  const response = await fetch(url, { headers: { "User-Agent": USER_AGENT }, redirect: "follow" });
  if (!response.ok) throw new Error(`HTTP ${response.status} pour ${url}`);
  const type = (response.headers.get("content-type") || "").split(";")[0].trim();
  if (!type.startsWith("image/")) throw new Error(`${url} n'est pas une image (${type || "type inconnu"})`);
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
      errors.push(`  - ${source}: ${error.message}`);
    }
  }
  throw new Error(`téléchargement impossible :\n${errors.join("\n")}`);
}

// Copy `input` into the support folder as background.<ext>; returns the file
// name (relative to the folder) to store in config.json.
export function store(input, dir) {
  if (!fs.statSync(input).isFile()) throw new Error(`${input} n'est pas un fichier`);
  const previous = fs.readdirSync(dir).filter((name) => /^background\.[a-z0-9]+$/i.test(name));
  const staging = path.join(dir, ".background-new");
  let name;
  let size = null;
  if (hasSips()) {
    name = "background.jpg";
    size = toJpeg(input, `${staging}.jpg`);
    fs.renameSync(`${staging}.jpg`, path.join(dir, name));
  } else {
    const ext = path.extname(input).toLowerCase() || ".jpg";
    if (fs.statSync(input).size > MAX_RAW_BYTES) throw new Error("image trop lourde (16 Mo max sans sips)");
    name = `background${ext}`;
    fs.copyFileSync(input, `${staging}${ext}`);
    fs.renameSync(`${staging}${ext}`, path.join(dir, name));
  }
  for (const old of previous) if (old !== name) fs.rmSync(path.join(dir, old), { force: true });
  return { name, size, bytes: fs.statSync(path.join(dir, name)).size };
}
