import test from "node:test";
import assert from "node:assert/strict";
import {
  api,
  APIError,
  authenticated,
  csrf,
  expireSession,
  protectUnsavedSession,
  sessionExpired,
} from "../src/api.ts";

test("ordinary expired sessions return to login", () => {
  authenticated.value = true;
  protectUnsavedSession.value = false;
  sessionExpired.value = false;
  expireSession();
  assert.equal(authenticated.value, false);
  assert.equal(sessionExpired.value, true);
});
test("background polling preserves a dirty editor on session expiry", () => {
  authenticated.value = true;
  protectUnsavedSession.value = true;
  sessionExpired.value = false;
  expireSession();
  assert.equal(authenticated.value, true);
  assert.equal(sessionExpired.value, true);
  protectUnsavedSession.value = false;
  expireSession();
  assert.equal(authenticated.value, false);
});
test("save rejection preserves the editor and exposes status for reauthentication", async () => {
  const original = globalThis.fetch;
  try {
    authenticated.value = true;
    protectUnsavedSession.value = true;
    sessionExpired.value = false;
    csrf.value = "test-only-token";
    globalThis.fetch = async (_url, options) => {
      assert.equal(
        new Headers(options?.headers).get("X-SRICS-CSRF"),
        "test-only-token",
      );
      return new Response(JSON.stringify({ error: "请先登录" }), {
        status: 401,
      });
    };
    await assert.rejects(
      api("/api/novels/test/chapters/test", { method: "PUT", body: "{}" }),
      (error: unknown) => error instanceof APIError && error.status === 401,
    );
    assert.equal(authenticated.value, true);
    assert.equal(sessionExpired.value, true);
  } finally {
    globalThis.fetch = original;
    protectUnsavedSession.value = false;
  }
});
test("transport failure remains a failed save without losing authentication", async () => {
  const original = globalThis.fetch;
  try {
    authenticated.value = true;
    globalThis.fetch = async () => {
      throw new TypeError("Failed to fetch");
    };
    await assert.rejects(
      api("/api/novels/test/chapters/test"),
      (error: unknown) =>
        error instanceof APIError &&
        error.status === 0 &&
        error.message.includes("无法连接"),
    );
    assert.equal(authenticated.value, true);
  } finally {
    globalThis.fetch = original;
  }
});
