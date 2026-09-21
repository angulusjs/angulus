import { test, expect } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { createServer as createHttpServer } from "node:http";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const workspace = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const cli = resolve(workspace, "packages/tooling/src/cli.mjs");

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>(resolveReady => server.listen(0, "127.0.0.1", resolveReady));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("No test port");
  await new Promise<void>((resolveClosed, reject) => server.close(error => error ? reject(error) : resolveClosed()));
  return address.port;
}

type ServerEvent = { type?: string; compilerPid?: number; diagnostics?: { file: string; line: number; message: string }[]; [key: string]: unknown };
function launch(command: string, root: string, port: number) {
  const events: ServerEvent[] = [];
  const child = spawn(process.execPath, [cli, command, "--root", root, "--host", "127.0.0.1", "--port", String(port), "--json"], {
    cwd: workspace, stdio: ["ignore", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  let pending = "";
  child.stdout.on("data", data => {
    stdout += data;
    pending += data;
    let end: number;
    while ((end = pending.indexOf("\n")) >= 0) {
      const line = pending.slice(0, end);
      pending = pending.slice(end + 1);
      if (line.trim().startsWith("{")) events.push(JSON.parse(line));
    }
  });
  child.stderr.on("data", data => { stderr += data; });
  return { child, events, output: () => stdout + stderr };
}

async function stop(child: ChildProcess) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  child.kill("SIGTERM");
  const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
  await new Promise<void>(resolveExit => child.once("exit", () => resolveExit()));
  clearTimeout(timer);
}

function command(args: string[], root: string): Promise<{ code: number | null; output: string }> {
  return new Promise((resolveExit, reject) => {
    const child = spawn(process.execPath, [cli, ...args, "--root", root], { cwd: workspace, stdio: ["ignore", "pipe", "pipe"] });
    let output = "";
    const timer = setTimeout(() => { child.kill("SIGKILL"); reject(new Error(`CLI timed out: ${args.join(" ")}\n${output}`)); }, 60_000);
    child.stdout.on("data", data => { output += data; });
    child.stderr.on("data", data => { output += data; });
    child.on("error", error => { clearTimeout(timer); reject(error); });
    child.on("exit", code => { clearTimeout(timer); resolveExit({ code, output }); });
  });
}

test("compiler -> Vite -> browser, HMR, diagnostics, SPA, build, preview and shutdown", async ({ page, request }) => {
  const root = await mkdtemp(resolve(workspace, "examples/.e2e-"));
  await cp(resolve(workspace, "examples/demo"), root, { recursive: true, filter: path => !path.split(/[\\/]/).some(part => part === "dist" || part === ".angulus") });
  const backend = createHttpServer((request, response) => {
    response.setHeader("Content-Type", "application/json");
    response.statusCode = request.url === "/api/ping" ? 200 : 404;
    response.end(JSON.stringify({ ok: request.url === "/api/ping" }));
  });
  await new Promise<void>(resolveReady => backend.listen(0, "127.0.0.1", resolveReady));
  const backendAddress = backend.address();
  if (!backendAddress || typeof backendAddress === "string") throw new Error("No backend test port");
  const configFile = resolve(root, "angulus.config.json");
  const config = JSON.parse(await readFile(configFile, "utf8"));
  config.proxy = { "/api": `http://127.0.0.1:${backendAddress.port}` };
  await writeFile(configFile, JSON.stringify(config));
  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const server = launch("serve", root, port);
  let preview: ReturnType<typeof launch> | undefined;
  const pageErrors: string[] = [];
  const socketMessages: string[] = [];
  const requestedModules: string[] = [];
  page.on("pageerror", error => pageErrors.push(error.message));
  page.on("request", request => requestedModules.push(request.url()));
  page.on("websocket", socket => socket.on("framereceived", ({ payload }) => socketMessages.push(String(payload))));
  try {
    await expect.poll(async () => {
      if (server.child.exitCode !== null) throw new Error(server.output());
      try { return (await request.get(url, { headers: { accept: "text/html" } })).status(); } catch { return 0; }
    }).toBe(200);
    await page.goto(url);
    await expect(page).toHaveTitle("Angulus demo");
    await expect(page.locator("h1")).toHaveText("Angulus counter");
    expect(await (await request.get(`${url}/api/ping`)).json()).toEqual({ ok: true });
    const occupied = await command(["serve", "--host", "127.0.0.1", "--port", String(port)], root);
    expect(occupied.code, occupied.output).not.toBe(0);
    expect(occupied.output).toMatch(/port.*(?:use|occupied)/i);
    await expect(page.locator("#count")).toHaveText("Count: 0");
    await page.locator("#increment").click();
    await page.locator("#increment").click();
    await expect(page.locator("#count")).toHaveText("Count: 2");
    await expect(page.locator("#reset")).toHaveText("Reset 2");
    await page.getByLabel("Name").fill("browser");
    await expect(page.locator("#greeting")).toHaveText("Hello, browser!");
    await page.locator("li input").first().fill("preserved DOM");
    await page.locator("#reverse").click();
    await expect(page.locator("li input").last()).toHaveValue("preserved DOM");
    await page.locator("#remove").click();
    await expect(page.locator("li")).toHaveCount(2);
    await page.locator("#reset").click();
    await expect(page.locator("#count")).toHaveText("Count: 0");
    for (let i = 0; i < 10; i++) await page.locator("#increment").click();
    await expect(page.locator("#limit")).toHaveText("Reached ten!");
    await expect(page.locator("#parity")).toHaveText("Even");

    const cssFile = resolve(root, "src/counter/counter.css");
    await writeFile(cssFile, `${await readFile(cssFile, "utf8")}\nh1 { color: rgb(123, 45, 67); }\n`);
    await expect(page.locator("h1")).toHaveCSS("color", "rgb(123, 45, 67)");
    await expect(page.locator("#count")).toHaveText("Count: 10");
    const htmlFile = resolve(root, "src/counter/counter.html");
    const html = await readFile(htmlFile, "utf8");
    await writeFile(htmlFile, html.replace("Angulus counter", "Updated template"));
    await expect(page.locator("h1")).toHaveText("Updated template");
    // Template HMR deliberately reloads rather than promising unsafe state retention.
    await expect(page.locator("#count")).toHaveText("Count: 0");

    const lazyFile = resolve(root, "src/products/product.html");
    const originalLazy = await readFile(lazyFile, "utf8");
    expect(requestedModules.some(url => url.includes("/src/products/product"))).toBe(false);
    await writeFile(lazyFile, "<h1>{{ missingLazyMember }}</h1>");
    const failedCheck = await command(["check", "--json"], root);
    expect(failedCheck.code, failedCheck.output).not.toBe(0);
    expect(failedCheck.output).toContain(lazyFile);
    expect(failedCheck.output).toContain("missingLazyMember");
    expect(JSON.parse(failedCheck.output).diagnostics).toContainEqual(expect.objectContaining({
      file: lazyFile, line: 1, column: 8, code: "TS2339",
    }));
    await expect.poll(() => server.events.some(event => event.diagnostics?.some(item => item.file === lazyFile && item.message.includes("missingLazyMember"))), { message: "watch must check unopened lazy templates" }).toBe(true);
    await expect(page.locator("vite-error-overlay")).toHaveCount(1);
    const failedRevision = Number(server.events.find(event => event.diagnostics?.some(item => item.file === lazyFile))?.revision);
    const failedBuild = await command(["build"], root);
    expect(failedBuild.code, failedBuild.output).not.toBe(0);
    expect(failedBuild.output).not.toMatch(/built in|build successful/i);
    await writeFile(lazyFile, originalLazy);
    await expect.poll(() => server.events.some(event => event.type === "checked" && event.valid === true && Number(event.revision) > failedRevision)).toBe(true);
    await expect(page.locator("vite-error-overlay")).toHaveCount(0);

    await page.getByRole("link", { name: "Lazy product" }).click();
    await expect(page).toHaveURL(`${url}/products/42`);
    await expect(page.locator("h1")).toHaveText("Lazy product 42");
    await page.goBack();
    await expect(page.locator("#count")).toHaveText("Count: 0");
    await page.goForward();
    await expect(page.locator("h1")).toHaveText("Lazy product 42");
    await page.reload();
    await expect(page.locator("h1")).toHaveText("Lazy product 42");
    await page.getByRole("link", { name: "Not found", exact: true }).click();
    await expect(page.locator("h1")).toHaveText("Page not found");
    for (const path of ["/missing.js", "/missing.css", "/api/missing"]) {
      const response = await request.get(url + path, { headers: { accept: "text/html" } });
      expect(response.status()).toBe(404);
      expect(await response.text()).not.toContain('<main id="app">');
    }

    const success = await command(["build"], root);
    expect(success.code, success.output).toBe(0);
    const appTests = await command(["test"], root);
    expect(appTests.code, appTests.output).toBe(0);
    expect(appTests.output).toContain("counter instances own their state");

    const previewPort = await freePort();
    preview = launch("preview", root, previewPort);
    const previewUrl = `http://127.0.0.1:${previewPort}`;
    await expect.poll(async () => {
      if (preview?.child.exitCode !== null) throw new Error(preview?.output());
      try { return (await request.get(previewUrl, { headers: { accept: "text/html" } })).status(); } catch { return 0; }
    }).toBe(200);
    await page.goto(`${previewUrl}/products/42`);
    await expect(page.locator("h1")).toHaveText("Lazy product 42");
    await page.getByRole("link", { name: "Back to counter" }).click();
    await page.locator("#increment").click();
    await expect(page.locator("#count")).toHaveText("Count: 1");
    expect((await request.get(`${previewUrl}/missing.js`, { headers: { accept: "text/html" } })).status()).toBe(404);
    expect((await request.get(`${previewUrl}/api/missing`, { headers: { accept: "text/html" } })).status()).toBe(404);
    expect(pageErrors).toEqual([]);

    const compilerPid = server.events.find(event => event.compilerPid)?.compilerPid;
    expect(compilerPid, server.output()).toBeTruthy();
    await stop(server.child);
    await expect.poll(() => {
      try { process.kill(compilerPid!, 0); return true; }
      catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
        return false;
      }
    }).toBe(false);
  } catch (error) {
    console.error(server.output(), JSON.stringify(socketMessages, null, 2));
    throw error;
  } finally {
    await stop(server.child);
    if (preview) await stop(preview.child);
    await new Promise<void>((resolveClosed, reject) => backend.close(error => error ? reject(error) : resolveClosed()));
    await rm(root, { recursive: true });
  }
});
