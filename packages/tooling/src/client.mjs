import { spawn } from "node:child_process";
import { access, mkdir } from "node:fs/promises";
import { createInterface } from "node:readline";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const workspace = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");

export function run(command, args, options = {}) {
  return new Promise((resolveResult, reject) => {
    const child = spawn(command, args, { ...options, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", data => { stdout += data; });
    child.stderr.on("data", data => { stderr += data; });
    child.on("error", reject);
    child.on("close", (code, signal) => resolveResult({ code, signal, stdout, stderr }));
  });
}

let building;
async function compilerBinary() {
  const binary = resolve(workspace, ".angulus/bin", process.platform === "win32" ? "angulus-compiler.exe" : "angulus-compiler");
  try {
    await access(binary);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
    building ??= (async () => {
      await mkdir(dirname(binary), { recursive: true });
      const result = await run("go", ["build", "-o", binary, "./cmd/angulus-compiler"], { cwd: workspace });
      if (result.code !== 0) throw new Error(`Go compiler build failed:\n${result.stderr}`);
    })();
    await building;
  }
  return binary;
}

export class CompilerClient {
  #child;
  #pending = new Map();
  #sequence = 0;
  #closing = false;
  #exit;

  get pid() { return this.#child?.pid; }

  async start() {
    if (this.#child) return;
    this.#child = spawn(await compilerBinary(), [], { stdio: ["pipe", "pipe", "pipe"] });
    this.#child.stderr.on("data", data => process.stderr.write(data));
    const fail = error => {
      for (const { reject, cleanup } of this.#pending.values()) { cleanup(); reject(error); }
      this.#pending.clear();
    };
    this.#exit = new Promise(resolveExit => {
      this.#child.on("error", error => { fail(error); resolveExit(); });
      this.#child.on("close", (code, signal) => {
        fail(new Error(`Angulus compiler exited (code ${code}, signal ${signal ?? "none"})`));
        resolveExit();
      });
    });
    this.#child.stdin.on("error", fail);
    createInterface({ input: this.#child.stdout }).on("line", line => {
      let message;
      try { message = JSON.parse(line); }
      catch { fail(new Error(`Invalid compiler protocol: ${line}`)); this.#child.kill(); return; }
      if (message.version !== 1) { fail(new Error("Unsupported compiler protocol version")); this.#child.kill(); return; }
      const pending = this.#pending.get(message.id);
      if (!pending) return;
      this.#pending.delete(message.id);
      pending.cleanup();
      if (message.error) pending.reject(new Error(typeof message.error === "string" ? message.error : message.error.message));
      else pending.resolve(message.result);
    });
    await this.request("parse", { source: "", file: "<startup>" });
  }

  request(method, body = {}, signal) {
    if (!this.#child || this.#child.exitCode !== null || this.#child.signalCode !== null || this.#closing) {
      return Promise.reject(new Error("Angulus compiler is not running"));
    }
    if (signal?.aborted) return Promise.reject(signal.reason);
    const id = ++this.#sequence;
    return new Promise((resolveResult, reject) => {
      const abort = () => {
        this.#pending.delete(id);
        cleanup();
        this.#child.stdin.write(`${JSON.stringify({ version: 1, id: ++this.#sequence, method: "cancel", targetId: id })}\n`);
        reject(signal.reason);
      };
      const timer = setTimeout(() => {
        this.#pending.delete(id);
        cleanup();
        reject(new Error(`Angulus compiler request timed out: ${method}`));
        this.#child.kill();
      }, 30_000);
      const cleanup = () => { clearTimeout(timer); signal?.removeEventListener("abort", abort); };
      this.#pending.set(id, { resolve: resolveResult, reject, cleanup });
      signal?.addEventListener("abort", abort, { once: true });
      this.#child.stdin.write(`${JSON.stringify({ version: 1, id, method, ...body })}\n`);
    });
  }

  async close() {
    if (!this.#child || this.#closing) return;
    if (this.#child.exitCode === null && this.#child.signalCode === null) {
      this.#child.stdin.write(`${JSON.stringify({ version: 1, id: ++this.#sequence, method: "shutdown" })}\n`);
      this.#child.stdin.end();
    }
    this.#closing = true;
    const timer = setTimeout(() => this.#child.kill("SIGKILL"), 1500);
    await this.#exit;
    clearTimeout(timer);
  }
}
