// e2ee.js — vendored, zero-dependency client-side encryption for Drift Canvas
// apps. Copy this file straight into your app's JS. No npm install, no build
// step: it's a plain ES module over `globalThis.crypto.subtle`, so it works
// whether your app is bundled (esbuild/Vite/etc.) or loaded directly via
// <script type="module">.
//
// Backbone encrypts everything it stores at rest (NoSQL, Blobs, Queues, SQL)
// under a per-slice key the Slice operator never sees in the clear on disk —
// but the Slice process itself still holds that key in memory to serve your
// app's own requests. If your app needs a stronger guarantee — the person
// running the Slice can't make sense of the data even with full access to
// it — the content has to be encrypted client-side, under a key that never
// reaches the server at all. That's what this file does.
//
// This is a generalization of Splitwide Open's group-key encryption
// (labs/OSS/splitwideopen/canvas/js/crypto.js), proven live: every expense
// and receipt is encrypted under a per-group key before it ever leaves the
// browser, and that key is handed to other group members only via the
// invite-link URL fragment — never through the API, so Drift only ever
// stores ciphertext. See README.md next to this file for the usage pattern.

const subtle = globalThis.crypto.subtle;
const te = new TextEncoder();
const td = new TextDecoder();

// ---- hex / base64 helpers --------------------------------------------------

export function toHex(bytes) {
  let s = "";
  for (const b of bytes) s += b.toString(16).padStart(2, "0");
  return s;
}

export function fromHex(hex) {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.substr(i * 2, 2), 16);
  return out;
}

// base64 <-> bytes, for shipping ciphertext through a JSON API.
export function bytesToB64(bytes) {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s);
}

export function b64ToBytes(b64) {
  const s = atob(b64);
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}

// ---- key generation ---------------------------------------------------

// A fresh 256-bit content key, hex-encoded. Generate this client-side and
// never send it to your Slice — distribute it out-of-band instead (an
// invite-link URL fragment, a QR code, etc.). Anyone who has this key can
// read everything encrypted under it; anyone who doesn't, including the
// Slice operator, sees only opaque ciphertext.
export function generateKey() {
  const k = new Uint8Array(32);
  globalThis.crypto.getRandomValues(k);
  return toHex(k);
}

// ---- AES-256-GCM content encryption ---------------------------------------

// Encrypt a JSON-serializable value under a hex content key. Returns an
// object safe to store/transmit as-is (iv and ciphertext are both hex).
export async function encryptJSON(keyHex, obj) {
  const key = await subtle.importKey("raw", fromHex(keyHex), { name: "AES-GCM" }, false, ["encrypt"]);
  const iv = new Uint8Array(12);
  globalThis.crypto.getRandomValues(iv);
  const ct = await subtle.encrypt({ name: "AES-GCM", iv }, key, te.encode(JSON.stringify(obj)));
  return { alg: "A256GCM", iv: toHex(iv), ct: toHex(new Uint8Array(ct)) };
}

export async function decryptJSON(keyHex, enc) {
  const key = await subtle.importKey("raw", fromHex(keyHex), { name: "AES-GCM" }, false, ["decrypt"]);
  const pt = await subtle.decrypt({ name: "AES-GCM", iv: fromHex(enc.iv) }, key, fromHex(enc.ct));
  return JSON.parse(td.decode(pt));
}

// Binary E2EE for files (e.g. receipt images): encrypt raw bytes under the
// content key. The 12-byte IV is prepended to the ciphertext, so the result
// is one opaque blob — store/transmit it exactly as returned.
export async function encryptBytes(keyHex, bytes) {
  const key = await subtle.importKey("raw", fromHex(keyHex), { name: "AES-GCM" }, false, ["encrypt"]);
  const iv = new Uint8Array(12);
  globalThis.crypto.getRandomValues(iv);
  const ct = new Uint8Array(await subtle.encrypt({ name: "AES-GCM", iv }, key, bytes));
  const out = new Uint8Array(iv.length + ct.length);
  out.set(iv);
  out.set(ct, iv.length);
  return out;
}

export async function decryptBytes(keyHex, blob) {
  const key = await subtle.importKey("raw", fromHex(keyHex), { name: "AES-GCM" }, false, ["decrypt"]);
  const pt = await subtle.decrypt({ name: "AES-GCM", iv: blob.slice(0, 12) }, key, blob.slice(12));
  return new Uint8Array(pt);
}

// ---- passphrase-based key backup (PBKDF2) ----------------------------------
// Recover content keys on a new device: encrypt a small object (e.g. your
// keyring of content keys) under a key derived from a passphrase. Store only
// the returned ciphertext server-side — the passphrase never leaves the
// device, so the Slice can't decrypt the backup.

const DEFAULT_PBKDF2_ITERS = 310000;

async function passphraseKey(passphrase, saltBytes, iters) {
  const base = await subtle.importKey("raw", te.encode(passphrase), "PBKDF2", false, ["deriveKey"]);
  return subtle.deriveKey(
    { name: "PBKDF2", salt: saltBytes, iterations: iters, hash: "SHA-256" },
    base, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]
  );
}

export async function encryptWithPassphrase(passphrase, obj, iters = DEFAULT_PBKDF2_ITERS) {
  const salt = new Uint8Array(16);
  globalThis.crypto.getRandomValues(salt);
  const iv = new Uint8Array(12);
  globalThis.crypto.getRandomValues(iv);
  const key = await passphraseKey(passphrase, salt, iters);
  const ct = await subtle.encrypt({ name: "AES-GCM", iv }, key, te.encode(JSON.stringify(obj)));
  return { v: 1, kdf: "PBKDF2-SHA256", iters, salt: toHex(salt), iv: toHex(iv), ct: toHex(new Uint8Array(ct)) };
}

export async function decryptWithPassphrase(passphrase, blob) {
  const key = await passphraseKey(passphrase, fromHex(blob.salt), blob.iters || DEFAULT_PBKDF2_ITERS);
  const pt = await subtle.decrypt({ name: "AES-GCM", iv: fromHex(blob.iv) }, key, fromHex(blob.ct));
  return JSON.parse(td.decode(pt));
}

// ---- seed-based key backup (HKDF) ------------------------------------------
// Recover content keys from a high-entropy seed (e.g. a 6-word recovery
// phrase) instead of a passphrase — no PBKDF2 stretching needed, since a
// seed isn't a low-entropy human secret. `domain` namespaces the derivation
// so the same seed produces different keys for different purposes/apps;
// pick a fixed, unique string for your app (e.g. "myapp-keyvault-v1") and
// never change it, or existing backups won't decrypt.

async function seedKey(seedHex, domain) {
  const base = await subtle.importKey("raw", fromHex(seedHex), "HKDF", false, ["deriveKey"]);
  return subtle.deriveKey(
    { name: "HKDF", hash: "SHA-256", salt: new Uint8Array(0), info: te.encode(domain) },
    base, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]
  );
}

export async function encryptWithSeed(seedHex, domain, obj) {
  const iv = new Uint8Array(12);
  globalThis.crypto.getRandomValues(iv);
  const key = await seedKey(seedHex, domain);
  const ct = await subtle.encrypt({ name: "AES-GCM", iv }, key, te.encode(JSON.stringify(obj)));
  return { v: 1, kdf: "HKDF-SHA256", iv: toHex(iv), ct: toHex(new Uint8Array(ct)) };
}

export async function decryptWithSeed(seedHex, domain, blob) {
  const key = await seedKey(seedHex, domain);
  const pt = await subtle.decrypt({ name: "AES-GCM", iv: fromHex(blob.iv) }, key, fromHex(blob.ct));
  return JSON.parse(td.decode(pt));
}
