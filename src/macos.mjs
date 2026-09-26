// macOS plumbing: Info.plist integrity hashes, ad-hoc re-signing, backups,
// and quitting / relaunching Claude. Only the CLI calls into this module.

import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

export const BUNDLE_ID = "com.anthropic.claudefordesktop";
const PLIST_BUDDY = "/usr/libexec/PlistBuddy";
const INTEGRITY_KEY = ":ElectronAsarIntegrity:Resources/app.asar:hash";

const run = (exe, args, options = {}) =>
  execFileSync(exe, args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], ...options });
const attempt = (exe, args) => spawnSync(exe, args, { encoding: "utf8" });
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export const paths = (app) => ({
  app,
  asar: path.join(app, "Contents", "Resources", "app.asar"),
  resources: path.join(app, "Contents", "Resources"),
  info: path.join(app, "Contents", "Info.plist"),
});

export function appVersion(app) {
  return run(PLIST_BUDDY, ["-c", "Print :CFBundleShortVersionString", paths(app).info]).trim();
}

// Every Info.plist in the bundle that pins the app.asar header hash: the main
// app and, depending on the build, the Electron framework and the helpers.
export function integrityPlists(app) {
  const found = [];
  const candidates = [paths(app).info];
  const frameworks = path.join(app, "Contents", "Frameworks");
  try {
    for (const name of fs.readdirSync(frameworks)) {
      if (name.endsWith(".app")) candidates.push(path.join(frameworks, name, "Contents", "Info.plist"));
      if (name.endsWith(".framework")) candidates.push(path.join(frameworks, name, "Versions", "A", "Resources", "Info.plist"));
    }
  } catch {}
  for (const plist of candidates) {
    const result = attempt(PLIST_BUDDY, ["-c", `Print ${INTEGRITY_KEY}`, plist]);
    if (result.status === 0 && result.stdout.trim()) found.push({ plist, hash: result.stdout.trim() });
  }
  return found;
}

export function setIntegrity(plist, hash) {
  run(PLIST_BUDDY, ["-c", `Set ${INTEGRITY_KEY} ${hash}`, plist]);
}

// "developer-id" for Anthropic's own signature, "adhoc" once re-signed locally.
export function signatureInfo(app) {
  const details = attempt("/usr/bin/codesign", ["-dv", "--verbose=2", app]);
  const text = `${details.stdout}\n${details.stderr}`;
  const valid = attempt("/usr/bin/codesign", ["--verify", "--deep", "--strict", app]).status === 0;
  let kind = "unknown";
  if (/Signature=adhoc/.test(text)) kind = "adhoc";
  else if (/Authority=Developer ID Application/.test(text)) kind = "developer-id";
  else if (/not signed/i.test(text)) kind = "unsigned";
  const team = /TeamIdentifier=(\S+)/.exec(text)?.[1];
  return { kind, valid, team: team && team !== "not" ? team : undefined };
}

// Patching app.asar invalidates Anthropic's signature, so the bundle is
// re-signed locally (ad-hoc). Nested code first (--deep), then the app itself
// with the entitlements Claude needs (microphone, camera, the Cowork VM...).
export function resign(app, entitlements) {
  attempt("/usr/bin/xattr", ["-cr", app]);
  run("/usr/bin/codesign", ["--force", "--deep", "--sign", "-", "--timestamp=none", app]);
  run("/usr/bin/codesign", ["--force", "--sign", "-", "--timestamp=none", "--entitlements", entitlements, app]);
  run("/usr/bin/codesign", ["--verify", "--deep", "--strict", app]);
}

// Full copy of the bundle, signature included (ditto keeps everything).
export function copyBundle(from, to) {
  fs.mkdirSync(path.dirname(to), { recursive: true });
  run("/usr/bin/ditto", [from, to]);
}

// ---------------------------------------------------------------- processes

function claudeProcesses(app) {
  const main = [];
  const helpers = [];
  const listing = attempt("/bin/ps", ["-axo", "pid=,command="]).stdout || "";
  for (const line of listing.split("\n")) {
    const match = /^\s*(\d+)\s+(.*)$/.exec(line);
    if (!match) continue;
    if (match[2].startsWith(`${app}/Contents/MacOS/`)) main.push(Number(match[1]));
    else if (match[2].startsWith(`${app}/Contents/`)) helpers.push(Number(match[1]));
  }
  return { main, helpers };
}

export const isRunning = (app) => claudeProcesses(app).main.length > 0;

// Quitting Claude from a terminal that Claude itself runs (Claude Code inside
// the desktop app) would kill this very process halfway through.
export function runningInside(app) {
  let pid = process.pid;
  for (let i = 0; i < 40 && pid > 1; i += 1) {
    const out = (attempt("/bin/ps", ["-o", "ppid=,command=", "-p", String(pid)]).stdout || "").trim();
    const match = /^(\d+)\s+(.*)$/.exec(out);
    if (!match) return false;
    if (match[2].startsWith(`${app}/Contents/`)) return true;
    pid = Number(match[1]);
  }
  return false;
}

const signal = (pids, name) => {
  for (const pid of pids) {
    try {
      process.kill(pid, name);
    } catch {}
  }
};

export async function quit(app) {
  if (!isRunning(app)) return false;
  attempt("/usr/bin/osascript", ["-e", `tell application id "${BUNDLE_ID}" to quit`]);
  for (let i = 0; i < 60; i += 1) {
    if (!isRunning(app)) return true;
    await sleep(250);
  }
  signal(claudeProcesses(app).main, "SIGTERM");
  for (let i = 0; i < 40; i += 1) {
    if (!isRunning(app)) return true;
    await sleep(250);
  }
  const left = claudeProcesses(app);
  signal([...left.main, ...left.helpers], "SIGKILL");
  for (let i = 0; i < 20; i += 1) {
    if (!isRunning(app)) return true;
    await sleep(250);
  }
  throw new Error("Claude refuse de se fermer : quitte-le à la main (⌘Q) puis relance la commande.");
}

export function launch(app) {
  attempt("/usr/bin/open", ["-a", app]);
}

// ---------------------------------------------------------------- images

export const hasSips = () => fs.existsSync("/usr/bin/sips");

export function imageSize(file) {
  const out = run("/usr/bin/sips", ["-g", "pixelWidth", "-g", "pixelHeight", file]);
  return [Number(/pixelWidth:\s*(\d+)/.exec(out)?.[1]), Number(/pixelHeight:\s*(\d+)/.exec(out)?.[1])];
}

// JPEG, longest side at most `max` pixels (never upscaled).
export function toJpeg(input, output, max = 2560) {
  const [w, h] = imageSize(input);
  const args = ["-s", "format", "jpeg", "-s", "formatOptions", "85"];
  if (Math.max(w, h) > max) args.push("-Z", String(max));
  run("/usr/bin/sips", [...args, input, "--out", output]);
  return imageSize(output);
}
