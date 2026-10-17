import test from "node:test";
import assert from "node:assert/strict";
import { api, APIError } from "../src/api.ts";

for (const stage of ["connection", "response-body"] as const) {
  test(`authentication times out during ${stage} and can be retried`, async (t) => {
    const original = globalThis.fetch;
    t.after(() => { globalThis.fetch = original; });
    t.mock.timers.enable({ apis: ["setTimeout"] });
    globalThis.fetch = async (_url, options) => {
      const signal = options?.signal;
      const stalled = () => new Promise<never>((_resolve, reject) => {
        signal?.addEventListener("abort", () => reject(signal.reason), { once: true });
      });
      if (stage === "connection") return stalled();
      return { ok: true, status: 200, json: stalled } as Response;
    };
    const pending = api("/api/auth/login", { method: "POST", body: "{}" });
    const rejected = assert.rejects(pending, error => error instanceof APIError && error.status === 0 && /超时.*访问地址/.test(error.message));
    // Let a received response enter its body read before advancing the clock.
    await Promise.resolve();
    t.mock.timers.tick(20_000);
    await rejected;

    globalThis.fetch = async () => Response.json({ authenticated: true });
    assert.deepEqual(await api("/api/auth/login", { method: "POST", body: "{}" }), { authenticated: true });
  });
}

test("an explicit authentication cancellation is not reported as a timeout", async (t) => {
  const original = globalThis.fetch;
  t.after(() => { globalThis.fetch = original; });
  globalThis.fetch = async (_url, options) => new Promise((_resolve, reject) => {
    const signal = options?.signal;
    if (signal?.aborted) reject(signal.reason);
    else signal?.addEventListener("abort", () => reject(signal.reason), { once: true });
  });
  const controller = new AbortController();
  const pending = api("/api/auth", { signal: controller.signal });
  const rejected = assert.rejects(pending, error => error instanceof Error && error.name === "AbortError");
  controller.abort();
  await rejected;
  await assert.rejects(api("/api/auth", { signal: controller.signal }), error => error instanceof Error && error.name === "AbortError");
});

test("the authentication deadline does not cancel a long file upload", async (t) => {
  const original = globalThis.fetch;
  t.after(() => { globalThis.fetch = original; });
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const controller = new AbortController();
  let finish!: (response: Response) => void;
  globalThis.fetch = async (_url, options) => {
    assert.equal(options?.signal, controller.signal);
    return new Promise(resolve => { finish = resolve; });
  };
  const pending = api("/api/uploads/test/files/0", { method: "PUT", signal: controller.signal });
  t.mock.timers.tick(60_000);
  assert.equal(controller.signal.aborted, false);
  finish(Response.json({ ok: true }));
  assert.deepEqual(await pending, { ok: true });
});
