import test from "node:test";
import assert from "node:assert/strict";
import {
  forgetVault,
  vaultAPI,
  vaultImage,
  vaultOpen,
  vaultStatus,
} from "../src/vault.ts";

test("locking aborts private requests and rejects late plaintext replies", async () => {
  const original = globalThis.fetch;
  let reply!: (r: Response) => void;
  let signal: AbortSignal | undefined;
  try {
    forgetVault();
    vaultOpen.value = true;
    globalThis.fetch = async (_url, options) => {
      signal = options?.signal as AbortSignal;
      return await new Promise<Response>((resolve) => (reply = resolve));
    };
    const pending = vaultAPI("/api/vault/list", { method: "POST" });
    forgetVault();
    assert.equal(signal?.aborted, true);
    reply(
      new Response(JSON.stringify({ items: [{ name: "late private name" }] })),
    );
    await assert.rejects(
      pending,
      (e: unknown) => e instanceof DOMException && e.name === "AbortError",
    );
    assert.equal(vaultOpen.value, false);
  } finally {
    globalThis.fetch = original;
    forgetVault();
  }
});
test("stale status cannot reopen a locked vault", async () => {
  const original = globalThis.fetch;
  let reply!: (r: Response) => void;
  try {
    forgetVault();
    vaultOpen.value = true;
    globalThis.fetch = async () =>
      await new Promise<Response>((resolve) => (reply = resolve));
    const pending = vaultStatus();
    forgetVault();
    reply(
      new Response(
        JSON.stringify({
          configured: true,
          unlocked: true,
          expires: new Date(Date.now() + 600000).toISOString(),
          idleMinutes: 10,
        }),
      ),
    );
    await pending;
    assert.equal(vaultOpen.value, false);
  } finally {
    globalThis.fetch = original;
    forgetVault();
  }
});
test("locking revokes private photo blob URLs", async () => {
  const original = globalThis.fetch,
    revoke = URL.revokeObjectURL;
  const revoked: string[] = [];
  try {
    forgetVault();
    vaultOpen.value = true;
    URL.revokeObjectURL = (url) => {
      revoked.push(url);
      revoke(url);
    };
    globalThis.fetch = async () => new Response(new Uint8Array([1, 2, 3]));
    const url = await vaultImage("test-private-photo");
    assert.match(url, /^blob:/);
    forgetVault();
    assert.deepEqual(revoked, [url]);
  } finally {
    globalThis.fetch = original;
    URL.revokeObjectURL = revoke;
    forgetVault();
  }
});
test("server lock responses clear client private state", async () => {
  const original = globalThis.fetch;
  try {
    forgetVault();
    vaultOpen.value = true;
    globalThis.fetch = async () =>
      new Response(JSON.stringify({ error: "保险库已锁定" }), { status: 423 });
    await assert.rejects(vaultAPI("/api/vault/list"));
    assert.equal(vaultOpen.value, false);
  } finally {
    globalThis.fetch = original;
    forgetVault();
  }
});

test("a status poll cannot undo a local lock when its network request failed", async () => {
  const original = globalThis.fetch;
  try {
    forgetVault();
    globalThis.fetch = async () =>
      new Response(
        JSON.stringify({
          configured: true,
          unlocked: true,
          expires: new Date(Date.now() + 600000).toISOString(),
          idleMinutes: 10,
        }),
      );
    await vaultStatus();
    assert.equal(vaultOpen.value, false);
  } finally {
    globalThis.fetch = original;
    forgetVault();
  }
});
