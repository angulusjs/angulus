#!/usr/bin/env node
import { spawn } from "node:child_process";
import { access } from "node:fs/promises";
import { cliLocation } from "./binary.mjs";

async function main() {
  const { binary, sourceRoot } = await cliLocation();
  try {
    await access(binary);
  } catch (error) {
    if (error.code !== "ENOENT" || !sourceRoot) throw error;
    throw new Error(`Angulus source CLI is not built. Run npm run compiler from ${sourceRoot}.`);
  }

  const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });
  const signals = new Map([["SIGINT", 130], ["SIGTERM", 143]]);
  const forward = signal => {
    if (child.exitCode === null && child.signalCode === null) child.kill(signal);
  };
  const handlers = new Map([...signals.keys()].map(signal => [signal, () => forward(signal)]));
  for (const [signal, handler] of handlers) process.once(signal, handler);
  try {
    await new Promise((resolve, reject) => {
      child.once("error", reject);
      child.once("close", (code, signal) => {
        process.exitCode = code ?? signals.get(signal) ?? 1;
        resolve();
      });
    });
  } finally {
    for (const [signal, handler] of handlers) process.removeListener(signal, handler);
  }
}

main().catch(error => {
  console.error(error.message);
  process.exitCode = 1;
});
