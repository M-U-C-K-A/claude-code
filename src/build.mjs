// Assembles the loader that goes into app.asar: src/loader.js with the page
// script (src/page.js) and the version inlined. The tag written in the asar
// markers carries a hash of the result, so any change to either file is seen
// as "loader out of date" and re-installed by `claude-backdrop install`.

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const SRC = path.dirname(fileURLToPath(import.meta.url));
export const VERSION = JSON.parse(fs.readFileSync(path.join(SRC, "..", "package.json"), "utf8")).version;

export function buildLoader() {
  const template = fs.readFileSync(path.join(SRC, "loader.js"), "utf8");
  const page = fs.readFileSync(path.join(SRC, "page.js"), "utf8");
  const hash = crypto.createHash("sha256").update(template).update("\0").update(page).digest("hex").slice(0, 10);
  const tag = `${VERSION}-${hash}`;
  const code = template
    .replace("const VERSION = __CB_VERSION__;", () => `const VERSION = ${JSON.stringify(tag)};`)
    .replace("const PAGE_SCRIPT = __CB_PAGE_SCRIPT__;", () => `const PAGE_SCRIPT = ${JSON.stringify(page)};`);
  if (code.includes("__CB_")) throw new Error("loader template still has unfilled placeholders");
  return { tag, code };
}
