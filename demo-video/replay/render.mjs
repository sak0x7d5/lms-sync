// Render a replay of one real chat to video.
//
//   node render.mjs chat.json --out replay.mp4 [options]
//
// chat.json comes from extract-chat.py. The page (replay.html) decides what
// every frame shows; this script only steps it frame by frame in a headless
// Edge or Chrome and pipes the screenshots to ffmpeg. No npm packages: it
// speaks the DevTools protocol over Node's built-in WebSocket (Node 22+).
//
// Options
//   --scene F          captions, speed-ups and gap setting (scene.example.json)
//   --out F            output video (default replay.mp4)
//   --fps N            default 30
//   --keys F           a CC0 key-click sample (wav/mp3); default: a synthesised click
//   --send F           a sample for pressing Enter; default: a synthesised thock
//   --no-audio         silent video
//   --frame V          write the frame at video second V to a PNG and stop
//   --browser PATH     Edge/Chrome executable (default: Edge, then Chrome)
//   --allow-estimated  render even when a text block has no end timestamp
//                      (its streaming time is then a guess; the timer would lie)
//
// Writes OUT and OUT.timing.json (segments, real end, keystroke times), which
// the final edit uses to place captions against the real clock.
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync, existsSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const die = m => { console.error(`render.mjs: ${m}`); process.exit(1); };

// --- arguments ---
const argv = process.argv.slice(2);
const opt = { out: "replay.mp4", fps: 30 };
const pos = [];
for (let i = 0; i < argv.length; i++) {
  const a = argv[i];
  if (!a.startsWith("--")) { pos.push(a); continue; }
  const k = a.slice(2);
  if (["no-audio", "allow-estimated"].includes(k)) opt[k] = true;
  else opt[k] = argv[++i];
}
if (pos.length !== 1) die("usage: node render.mjs chat.json --out replay.mp4 [--scene scene.json]");
const chat = JSON.parse(readFileSync(pos[0], "utf8"));
const scene = opt.scene ? JSON.parse(readFileSync(opt.scene, "utf8")) : {};
const fps = Number(opt.fps);
const W = 1920, H = 1080, RATE = 48000;

// --- the page, with the chat baked in ---
const work = mkdtempSync(join(tmpdir(), "replay-"));
const inject = s => JSON.stringify(s).replace(/</g, "\\u003c");
const page0 = readFileSync(join(here, "replay.html"), "utf8");
const marker = /<script>\s*"use strict";/;
if (!marker.test(page0)) die("replay.html has no <script> \"use strict\" to inject the chat before");
const html = page0.replace(marker, m => `<script>window.CHAT=${inject(chat)};window.SCENE=${inject(scene)};</script>\n${m}`);
writeFileSync(join(work, "page.html"), html);

// --- a headless browser, driven over the DevTools protocol ---
const browser = opt.browser || [
  "C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe",
  "C:/Program Files/Microsoft/Edge/Application/msedge.exe",
  "C:/Program Files/Google/Chrome/Application/chrome.exe",
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/usr/bin/chromium", "/usr/bin/google-chrome",
].find(existsSync);
if (!browser) die("no Edge or Chrome found; pass --browser PATH");
const profile = join(work, "profile");
const proc = spawn(browser, [
  "--headless=new", "--remote-debugging-port=0", `--user-data-dir=${profile}`,
  "--disable-gpu", "--hide-scrollbars", "--force-device-scale-factor=1", "--no-first-run",
  "--disable-extensions", "--font-render-hinting=none", `--window-size=${W},${H}`, "about:blank",
], { stdio: "ignore" });
const cleanup = () => { try { proc.kill(); } catch {} setTimeout(() => { try { rmSync(work, { recursive: true, force: true }); } catch {} }, 500); };
process.on("exit", cleanup);

const sleep = ms => new Promise(r => setTimeout(r, ms));
let port;
for (let i = 0; i < 100 && !port; i++) {
  await sleep(100);
  try { port = readFileSync(join(profile, "DevToolsActivePort"), "utf8").split("\n")[0]; } catch {}
}
if (!port) die("the browser did not start");
const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const page = targets.find(t => t.type === "page");
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((ok, bad) => { ws.onopen = ok; ws.onerror = bad; });
let nextId = 1; const pending = new Map(); const pageErrors = [];
ws.onmessage = e => {
  const m = JSON.parse(e.data);
  if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.bad(new Error(m.error.message)) : p.ok(m.result); }
  else if (m.method === "Runtime.exceptionThrown") pageErrors.push(m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text);
};
const cdp = (method, params = {}) => new Promise((ok, bad) => {
  const id = nextId++; pending.set(id, { ok, bad }); ws.send(JSON.stringify({ id, method, params }));
});
const evaluate = async expr => {
  const r = await cdp("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) die(`page error: ${r.exceptionDetails.exception?.description || r.exceptionDetails.text}`);
  return r.result.value;
};

await cdp("Emulation.setDeviceMetricsOverride", { width: W, height: H, deviceScaleFactor: 1, mobile: false });
await cdp("Page.enable");
await cdp("Runtime.enable");
await cdp("Page.navigate", { url: pathToFileURL(join(work, "page.html")).href });
for (let i = 0; i < 100; i++) { if (await evaluate("typeof REPLAY === 'object' && document.readyState === 'complete'")) break; await sleep(100); }
await evaluate("document.fonts.ready.then(() => true)");
const R = await evaluate("window.REPLAY");
if (!R) die("the page did not load the chat" + (pageErrors.length ? ":\n" + pageErrors.join("\n") : ""));
if (R.estimated.length && !opt["allow-estimated"])
  die(`no end timestamp for: ${R.estimated.join(", ")}. The streaming time would be a guess; pass --allow-estimated to render anyway.`);

const shot = async () => Buffer.from((await cdp("Page.captureScreenshot", { format: "png", fromSurface: true })).data, "base64");

if (opt.frame != null) {
  await evaluate(`seek(${Number(opt.frame)})`);
  writeFileSync(opt.out, await shot());
  console.log(`frame at ${opt.frame}s -> ${opt.out}`);
  process.exit(0);
}

// --- audio: one click per real keystroke, placed at its video time ---
function decode(file) {
  const r = spawnSync("ffmpeg", ["-v", "error", "-i", file, "-ac", "1", "-ar", String(RATE), "-f", "f32le", "-"], { maxBuffer: 1 << 26 });
  if (r.status) die(`ffmpeg could not read ${file}`);
  return new Float32Array(r.stdout.buffer, r.stdout.byteOffset, r.stdout.length / 4);
}
function synth(len, decay, thump, seed) {
  let s = seed >>> 0; const rnd = () => ((s = (s * 1664525 + 1013904223) >>> 0) / 2 ** 32) * 2 - 1;
  const n = Math.round(len * RATE), out = new Float32Array(n); let lp = 0;
  for (let i = 0; i < n; i++) {
    const t = i / RATE;
    lp += 0.35 * (rnd() * Math.exp(-t / decay) - lp);
    out[i] = lp + thump * Math.sin(2 * Math.PI * 170 * t) * Math.exp(-t / (decay * 2.5));
  }
  return out;
}
const total = Math.ceil(R.duration * RATE) + RATE;
if (!opt["no-audio"]) {
  const mix = new Float32Array(total);
  const key = opt.keys ? decode(opt.keys) : null;
  const send = opt.send ? decode(opt.send) : synth(0.08, 0.012, 0.6, 99);
  let s = 12345; const rnd = () => (s = (s * 1664525 + 1013904223) >>> 0) / 2 ** 32;
  const place = (clip, at, gain) => { const o = Math.round(at * RATE); for (let i = 0; i < clip.length && o + i < total; i++) if (o + i >= 0) mix[o + i] += clip[i] * gain; };
  R.keys.forEach((t, i) => place(key || synth(0.04, 0.005, 0.25, i + 1), t, 0.55 + rnd() * 0.3));
  R.sends.forEach(t => place(send, t, 0.8));
  let peak = 0; for (const x of mix) peak = Math.max(peak, Math.abs(x));
  const g = peak > 0 ? 0.7 / peak : 1;
  const pcm = Buffer.alloc(44 + total * 2);
  pcm.write("RIFF", 0); pcm.writeUInt32LE(36 + total * 2, 4); pcm.write("WAVEfmt ", 8);
  pcm.writeUInt32LE(16, 16); pcm.writeUInt16LE(1, 20); pcm.writeUInt16LE(1, 22); pcm.writeUInt32LE(RATE, 24);
  pcm.writeUInt32LE(RATE * 2, 28); pcm.writeUInt16LE(2, 32); pcm.writeUInt16LE(16, 34); pcm.write("data", 36); pcm.writeUInt32LE(total * 2, 40);
  mix.forEach((x, i) => pcm.writeInt16LE(Math.max(-32767, Math.min(32767, Math.round(x * g * 32767))), 44 + i * 2));
  writeFileSync(join(work, "keys.wav"), pcm);
}

// --- frames into ffmpeg ---
const out = resolve(opt.out);
const ff = spawn("ffmpeg", [
  "-hide_banner", "-loglevel", "error", "-y",
  "-f", "image2pipe", "-framerate", String(fps), "-c:v", "png", "-i", "-",
  ...(opt["no-audio"] ? [] : ["-i", join(work, "keys.wav")]),
  "-map", "0:v", ...(opt["no-audio"] ? ["-an"] : ["-map", "1:a", "-c:a", "aac", "-b:a", "160k", "-shortest"]),
  "-c:v", "libx264", "-preset", "medium", "-crf", "18", "-pix_fmt", "yuv420p", "-movflags", "+faststart", out,
], { stdio: ["pipe", "inherit", "inherit"] });
const done = new Promise((ok, bad) => ff.on("close", c => c ? bad(new Error(`ffmpeg exited ${c}`)) : ok()));

const frames = Math.ceil(R.duration * fps);
const t0 = Date.now();
for (let f = 0; f < frames; f++) {
  await evaluate(`seek(${f / fps})`);
  if (!ff.stdin.write(await shot())) await new Promise(r => ff.stdin.once("drain", r));
  if (f % (fps * 5) === 0) process.stderr.write(`\r${(f / fps).toFixed(0)}s / ${R.duration.toFixed(0)}s`);
}
ff.stdin.end();
await done;
process.stderr.write("\n");

const fast = R.segments.filter(s => s.x > 1);
writeFileSync(out + ".timing.json", JSON.stringify({
  chat: chat.name, fps, duration: R.duration, realEnd: R.realEnd,
  segments: R.segments, keys: R.keys, sends: R.sends,
}, null, 2));
console.log(`${out}: ${R.duration.toFixed(1)}s of video for ${R.realEnd.toFixed(1)}s of real time; ` +
  `${fast.length ? fast.map(s => `${s.x}x over real ${s.r0.toFixed(1)}–${s.r1.toFixed(1)}s`).join(", ") : "nothing sped up"}; ` +
  `rendered in ${((Date.now() - t0) / 1000).toFixed(0)}s`);
process.exit(0);
