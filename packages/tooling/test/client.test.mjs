import { test } from "node:test";
import assert from "node:assert/strict";
import { CompilerClient } from "../src/client.mjs";

function assertStopped(pid) {
  assert.equal(typeof pid, "number");
  assert.throws(() => process.kill(pid, 0), { code: "ESRCH" });
}

test("compiler close waits for in-flight startup and process termination", async () => {
  const client = new CompilerClient();
  try {
    const starting = client.start();
    const closing = client.close();
    assert.equal(client.close(), closing);
    await closing;
    await starting;
    assertStopped(client.pid);
    await assert.rejects(client.start(), /closed/);
    await assert.rejects(client.request("parse", { source: "", file: "test.html" }), /not running/);
  } finally { await client.close(); }
});

test("concurrent starts share one compiler and readiness handshake", async () => {
  const client = new CompilerClient();
  try {
    const first = client.start();
    assert.equal(client.start(), first);
    await first;
    const pid = client.pid;
    assert.deepEqual((await client.request("parse", { source: "", file: "test.html" })).diagnostics, []);
    await Promise.all([client.close(), client.close()]);
    assertStopped(pid);
  } finally { await client.close(); }
});

test("closing before startup prevents a later orphan compiler", async () => {
  const client = new CompilerClient();
  await client.close();
  await assert.rejects(client.start(), /closed/);
  assert.equal(client.pid, undefined);
});
