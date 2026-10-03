import { spawn, type ChildProcessByStdio } from "node:child_process";
import { access, mkdir } from "node:fs/promises";
import { createInterface } from "node:readline";
import type { Readable, Writable } from "node:stream";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { compilerLocation } from "./binary.js";

export const workspace = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");

export interface RunResult {
  code: number | null;
  signal: NodeJS.Signals | null;
  stdout: string;
  stderr: string;
}

export function run(command: string, args: string[], options: import("node:child_process").SpawnOptions = {}): Promise<RunResult> {
  return new Promise<RunResult>((resolveResult, reject) => {
    const child = spawn(command, args, { ...options, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (data: Buffer) => { stdout += data; });
    child.stderr.on("data", (data: Buffer) => { stderr += data; });
    child.on("error", reject);
    child.on("close", (code, signal) => resolveResult({ code, signal, stdout, stderr }));
  });
}

let building: Promise<void> | undefined;
type CompilerChild = ChildProcessByStdio<Writable, Readable, Readable>;
interface CompilerResponse {
  id?: number;
  version?: number;
  error?: string | { message: string };
  result: {
    diagnostics?: { start: number; end: number; code: string; message: string }[];
    nodes?: unknown[];
    code?: string;
    mappings?: { generated: number; source: number }[];
  };
}
interface PendingRequest {
  resolve(value: CompilerResponse["result"]): void;
  reject(reason?: unknown): void;
  cleanup(): void;
}

async function compilerBinary() {
  const { binary, sourceRoot } = await compilerLocation();
  if (!sourceRoot) return binary;
  try {
    await access(binary);
  } catch (error) {
    if (!(error instanceof Error) || !("code" in error) || error.code !== "ENOENT") throw error;
    building ??= (async () => {
      await mkdir(dirname(binary), { recursive: true });
      const result = await run("go", ["build", "-o", binary, "./cmd/angulus-compiler"], { cwd: sourceRoot });
      if (result.code !== 0) throw new Error(`Go compiler build failed:\n${result.stderr}`);
    })();
    await building;
  }
  return binary;
}

export class CompilerClient {
  #child: CompilerChild | undefined;
  #pending = new Map<number, PendingRequest>();
  #sequence = 0;
  #closing = false;
  #exit: Promise<void> | undefined;
  #starting: Promise<void> | undefined;
  #shutdown: Promise<void> | undefined;

  get pid() { return this.#child?.pid; }

  start(): Promise<void> {
    if (this.#shutdown) return Promise.reject(new Error("Angulus compiler has been closed"));
    this.#starting ??= this.#start();
    return this.#starting;
  }

  async #start(): Promise<void> {
    const child = this.#child = spawn(await compilerBinary(), [], { stdio: ["pipe", "pipe", "pipe"] });
    child.stderr.on("data", data => process.stderr.write(data));
    const fail = (error: Error) => {
      for (const { reject, cleanup } of this.#pending.values()) { cleanup(); reject(error); }
      this.#pending.clear();
    };
    this.#exit = new Promise<void>(resolveExit => {
      child.on("error", error => { fail(error); resolveExit(); });
      child.on("close", (code, signal) => {
        fail(new Error(`Angulus compiler exited (code ${code}, signal ${signal ?? "none"})`));
        resolveExit();
      });
    });
    child.stdin.on("error", fail);
    createInterface({ input: child.stdout }).on("line", line => {
      let message: CompilerResponse;
      try { message = JSON.parse(line); }
      catch { fail(new Error(`Invalid compiler protocol: ${line}`)); child.kill(); return; }
      if (message.version !== 1) { fail(new Error("Unsupported compiler protocol version")); child.kill(); return; }
      if (typeof message.id !== "number") { fail(new Error("Invalid compiler protocol message id")); child.kill(); return; }
      const pending = this.#pending.get(message.id);
      if (!pending) return;
      this.#pending.delete(message.id);
      pending.cleanup();
      if (message.error) pending.reject(new Error(typeof message.error === "string" ? message.error : message.error.message));
      else pending.resolve(message.result);
    });
    await this.request("parse", { source: "", file: "<startup>" });
  }

  request<T extends CompilerResponse["result"] = CompilerResponse["result"]>(
    method: string,
    body: Record<string, unknown> = {},
    signal?: AbortSignal,
  ): Promise<T> {
    if (!this.#child || this.#child.exitCode !== null || this.#child.signalCode !== null || this.#closing) {
      return Promise.reject(new Error("Angulus compiler is not running"));
    }
    if (signal?.aborted) return Promise.reject(signal.reason);
    const child = this.#child;
    if (!child) return Promise.reject(new Error("Angulus compiler is not running"));
    const id = ++this.#sequence;
    return new Promise<T>((resolveResult, reject) => {
      const abort = () => {
        this.#pending.delete(id);
        cleanup();
        child.stdin.write(`${JSON.stringify({ version: 1, id: ++this.#sequence, method: "cancel", targetId: id })}\n`);
        reject(signal?.reason);
      };
      const timer = setTimeout(() => {
        this.#pending.delete(id);
        cleanup();
        reject(new Error(`Angulus compiler request timed out: ${method}`));
        child.kill();
      }, 30_000);
      const cleanup = () => { clearTimeout(timer); signal?.removeEventListener("abort", abort); };
      this.#pending.set(id, { resolve: value => resolveResult(value as T), reject, cleanup });
      signal?.addEventListener("abort", abort, { once: true });
      child.stdin.write(`${JSON.stringify({ version: 1, id, method, ...body })}\n`);
    });
  }

  close(): Promise<void> {
    this.#shutdown ??= this.#close();
    return this.#shutdown;
  }

  async #close(): Promise<void> {
    // Startup may still be resolving/building the binary without a child to stop.
    try { await this.#starting; }
    finally {
      this.#closing = true;
      const child = this.#child;
      if (child) {
        if (child.exitCode === null && child.signalCode === null) {
          child.stdin.write(`${JSON.stringify({ version: 1, id: ++this.#sequence, method: "shutdown" })}\n`);
          child.stdin.end();
        }
        const timer = setTimeout(() => child.kill("SIGKILL"), 1500);
        try { await this.#exit; }
        finally { clearTimeout(timer); }
      }
    }
  }
}
