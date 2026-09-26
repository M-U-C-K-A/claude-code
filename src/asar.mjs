// Minimal, dependency-free reader/patcher for Electron's asar archives.
//
// Layout of an asar file:
//   [0..4)   uint32 LE = 4               (size of the next field, Chromium Pickle)
//   [4..8)   uint32 LE = headerSize      (size of the header Pickle below)
//   [8..12)  uint32 LE = headerSize - 4  (payload size of the header Pickle)
//   [12..16) uint32 LE = jsonLength
//   [16..)   header JSON, padded to a multiple of 4
//   [8 + headerSize ..) file data; each entry's `offset` is relative to this point
//
// Electron's ASAR integrity check (macOS) compares SHA-256(header JSON) with
// ElectronAsarIntegrity in Info.plist, and each file entry carries its own
// SHA-256 block hashes. Patching a file therefore means: rewrite its bytes,
// shift the offsets of everything stored after it, refresh its integrity
// record, and hand the new header hash back to the caller.

import crypto from "node:crypto";

const sha256 = (data) => crypto.createHash("sha256").update(data).digest("hex");

export function parseAsar(archive) {
  if (archive.length < 16 || archive.readUInt32LE(0) !== 4) throw new Error("not an asar archive");
  const headerSize = archive.readUInt32LE(4);
  const jsonLength = archive.readUInt32LE(12);
  const headerJson = archive.subarray(16, 16 + jsonLength);
  return {
    archive,
    header: JSON.parse(headerJson.toString("utf8")),
    headerJson,
    dataOffset: 8 + headerSize,
  };
}

export const headerHash = (parsed) => sha256(parsed.headerJson);

function buildPrefix(header) {
  const json = Buffer.from(JSON.stringify(header), "utf8");
  const padding = (4 - (json.length % 4)) % 4;
  const headerSize = 8 + json.length + padding;
  const prefix = Buffer.alloc(8 + headerSize);
  prefix.writeUInt32LE(4, 0);
  prefix.writeUInt32LE(headerSize, 4);
  prefix.writeUInt32LE(headerSize - 4, 8);
  prefix.writeUInt32LE(json.length, 12);
  json.copy(prefix, 16);
  return { prefix, json };
}

export function findEntry(header, filePath) {
  let node = header;
  for (const part of filePath.split("/").filter(Boolean)) {
    node = node.files?.[part];
    if (!node) throw new Error(`${filePath} not found in app.asar`);
  }
  if (node.files || node.link) throw new Error(`${filePath} is not a regular file`);
  if (node.unpacked) throw new Error(`${filePath} is stored outside the archive (unpacked)`);
  return node;
}

export function walkPacked(node, visit, prefix = "") {
  for (const [name, child] of Object.entries(node.files ?? {})) {
    const childPath = prefix ? `${prefix}/${name}` : name;
    if (child.files) walkPacked(child, visit, childPath);
    else if (!child.link && !child.unpacked) visit(child, childPath);
  }
}

export function readEntry(parsed, filePath) {
  const entry = findEntry(parsed.header, filePath);
  const start = parsed.dataOffset + Number(entry.offset);
  return parsed.archive.subarray(start, start + Number(entry.size));
}

// The app's entry point is package.json "main" (".vite/build/index.pre.js" in
// current Claude builds). Never hardcode it: it has moved between releases.
export function mainEntryPath(parsed) {
  const pkg = JSON.parse(readEntry(parsed, "package.json").toString("utf8"));
  if (typeof pkg.main !== "string" || !pkg.main) throw new Error('package.json in app.asar has no "main"');
  let main = pkg.main.replace(/^\.\//, "");
  try {
    findEntry(parsed.header, main);
  } catch {
    main = `${main.replace(/\/$/, "")}/index.js`;
  }
  return main;
}

export function fileIntegrity(content, blockSize = 4 * 1024 * 1024) {
  const blocks = [];
  for (let offset = 0; offset < content.length; offset += blockSize) {
    blocks.push(sha256(content.subarray(offset, offset + blockSize)));
  }
  return { algorithm: "SHA256", hash: sha256(content), blockSize, blocks };
}

// ---------------------------------------------------------------- loader block
// The loader is appended at the END of the main script, wrapped in markers, so
// the original code (and its "use strict" prologue) stays byte-for-byte intact
// and can be restored exactly.

const BLOCK_RE = /\n\/\* claude-backdrop:loader:([\w.-]+):start \*\/\n[\s\S]*?\n\/\* claude-backdrop:loader:[\w.-]+:end \*\/\n$/;

export const loaderBlock = (tag, code) =>
  `\n/* claude-backdrop:loader:${tag}:start */\n${code.trim()}\n/* claude-backdrop:loader:${tag}:end */\n`;

export function loaderTagOf(source) {
  const match = BLOCK_RE.exec(source);
  return match ? match[1] : null;
}

export const stripLoader = (source) => source.replace(BLOCK_RE, "");

// ---------------------------------------------------------------- patching

// Replace one packed file and return a new archive buffer.
export function replaceEntry(parsed, filePath, replacement) {
  const header = structuredClone(parsed.header);
  const entry = findEntry(header, filePath);
  const offset = Number(entry.offset);
  const size = Number(entry.size);

  // asar can deduplicate identical files: two entries may share bytes. If any
  // other entry points inside the region we rewrite, patching would corrupt it.
  walkPacked(header, (other) => {
    if (other === entry) return;
    const start = Number(other.offset);
    if (start >= offset && start < offset + size) throw new Error(`${filePath} shares its bytes with another file`);
  });

  const delta = replacement.length - size;
  walkPacked(header, (other) => {
    if (other !== entry && Number(other.offset) > offset) other.offset = String(Number(other.offset) + delta);
  });
  entry.size = replacement.length;
  if (entry.integrity) entry.integrity = fileIntegrity(replacement, entry.integrity.blockSize);

  const data = parsed.archive.subarray(parsed.dataOffset);
  const { prefix, json } = buildPrefix(header);
  const archive = Buffer.concat([prefix, data.subarray(0, offset), replacement, data.subarray(offset + size)]);
  return { archive, headerHash: sha256(json) };
}

// Append (or replace) the loader in the main script. `tag` identifies the
// loader build; when the archive already carries the same tag nothing changes.
export function patchAsar(archive, { tag, code }) {
  const parsed = parseAsar(archive);
  const mainPath = mainEntryPath(parsed);
  const source = readEntry(parsed, mainPath).toString("utf8");
  if (loaderTagOf(source) === tag) {
    return { archive, changed: false, mainPath, headerHash: headerHash(parsed) };
  }
  const original = stripLoader(source);
  if (!/\brequire\(/.test(original)) {
    throw new Error(`${mainPath} does not look like a CommonJS Electron entry point; refusing to patch blindly`);
  }
  const replacement = Buffer.from(original + loaderBlock(tag, code), "utf8");
  const result = replaceEntry(parsed, mainPath, replacement);
  return { archive: result.archive, changed: true, mainPath, headerHash: result.headerHash };
}

export function unpatchAsar(archive) {
  const parsed = parseAsar(archive);
  const mainPath = mainEntryPath(parsed);
  const source = readEntry(parsed, mainPath).toString("utf8");
  if (!loaderTagOf(source)) return { archive, changed: false, mainPath, headerHash: headerHash(parsed) };
  const result = replaceEntry(parsed, mainPath, Buffer.from(stripLoader(source), "utf8"));
  return { archive: result.archive, changed: true, mainPath, headerHash: result.headerHash };
}

export function inspectAsar(archive) {
  const parsed = parseAsar(archive);
  const mainPath = mainEntryPath(parsed);
  const source = readEntry(parsed, mainPath).toString("utf8");
  return { mainPath, loaderTag: loaderTagOf(source), headerHash: headerHash(parsed) };
}

// Check every packed file against its integrity record (what Electron does
// lazily at runtime). Returns the number of files checked; throws on mismatch.
export function verifyAsar(archive) {
  const parsed = parseAsar(archive);
  let checked = 0;
  walkPacked(parsed.header, (entry, filePath) => {
    if (!entry.integrity) return;
    const start = parsed.dataOffset + Number(entry.offset);
    const content = parsed.archive.subarray(start, start + Number(entry.size));
    const expected = fileIntegrity(content, entry.integrity.blockSize);
    if (expected.hash !== entry.integrity.hash || expected.blocks.join() !== entry.integrity.blocks.join()) {
      throw new Error(`integrity mismatch for ${filePath}`);
    }
    checked += 1;
  });
  return checked;
}
