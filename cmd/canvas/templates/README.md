# templates/ — vendored client-side helpers for Canvas apps

`e2ee.js` is a zero-dependency ES module implementing client-side
(end-to-end) encryption for Canvas apps. Unlike `cmd/atomic/cmd/deploy/default/`,
nothing here is embedded into the `drift` binary yet — there is no
`drift canvas new --with-e2ee` (or similar) scaffold command. For now this
is a **vendor-by-copy** template: grab `e2ee.js` from this directory (or
straight from [github.com/ondrift/cli](https://github.com/ondrift/cli), the
`cli` repo is public) and drop it into your app's own JS alongside your
other source. A scaffold command that does this automatically is a natural
follow-up, not yet built.

## Why this exists

Backbone encrypts everything it stores at rest — NoSQL, Blobs, Queues, SQL —
under a key that's opaque to whoever has raw disk/backup access to a Slice.
That protects against disk theft or an unauthorized backup restore. It does
**not** protect against the person running the Slice: the Slice process
itself holds the decryption key in memory to serve your app's own requests,
same as any server holding the keys to its own database.

If your app needs the stronger guarantee — the Slice operator can't make
sense of the data even with full access to it — the data has to be
encrypted before it ever reaches the Slice, under a key the Slice never
sees. `e2ee.js` is that: a thin wrapper over `globalThis.crypto.subtle`
(WebCrypto, built into every browser — no npm install, no build step) for
generating a content key client-side, encrypting/decrypting JSON or raw
bytes under it, and backing the key up (passphrase- or seed-derived) so
users can recover it on a new device without the server ever holding it in
the clear.

This is a generalization of the pattern
[Splitwide Open](../../../../labs/OSS/splitwideopen) proved out live: a
per-group content key minted in the browser, handed to other group members
only through the invite-link URL fragment (never through the API), used to
encrypt every expense and receipt before it leaves the device.

## Usage pattern

```js
import { generateKey, encryptJSON, decryptJSON } from "./e2ee.js";

// Once, when a user creates a new "space" (group / vault / whatever your
// app calls its shared context):
const key = generateKey(); // 32 random bytes, hex-encoded

// Share `key` out-of-band — e.g. append it to an invite link's URL
// fragment (#key=...), which browsers never send to a server — never as
// a normal request body/param, or your Slice sees it.

// Before writing anything sensitive to Backbone:
const encrypted = await encryptJSON(key, { amount: 42.5, note: "dinner" });
await fetch("/api/expenses", { method: "POST", body: JSON.stringify(encrypted) });

// After reading it back:
const row = await (await fetch("/api/expenses/123")).json();
const plain = await decryptJSON(key, row);
```

For files (e.g. receipt photos), use `encryptBytes` / `decryptBytes`
instead — same key, same guarantee, works on any `Uint8Array`.

For key recovery, use `encryptWithPassphrase` / `decryptWithPassphrase` (a
human-chosen passphrase, PBKDF2-stretched) or `encryptWithSeed` /
`decryptWithSeed` (a high-entropy seed, e.g. from a recovery phrase —
`domain` namespaces the derivation so pick one fixed string per app, e.g.
`"myapp-keyvault-v1"`, and never change it). Either way, store only the
returned ciphertext object server-side; the passphrase/seed itself never
leaves the device.

## What this doesn't do

- It doesn't replace Backbone's at-rest encryption — that's still there,
  still default, and still worth having (defense in depth: even the
  ciphertext this module produces benefits from not sitting on a plaintext
  disk).
- It doesn't manage key distribution for you. Getting the content key to
  the right people without ever sending it through your Slice is your
  app's problem to solve (invite links, QR codes, out-of-band exchange —
  whatever fits). `e2ee.js` only does the encrypt/decrypt/derive math.
- It isn't a replacement for the verifiable-log / tamper-evidence machinery
  Splitwide Open's own `crypto.js` also has (canonical JSON, Merkle proofs,
  Ed25519 signing). That's a separate, app-specific feature — this template
  only covers the reusable encryption half.
