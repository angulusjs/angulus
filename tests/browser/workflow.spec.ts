import { test, expect } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
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

function launch(command: string, root: string, port: number) {
  const child = spawn(process.execPath, [cli, command, "--root", root, "--host", "127.0.0.1", "--port", String(port)], {
    cwd: workspace, stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  child.stdout.on("data", data => { output += data; });
  child.stderr.on("data", data => { output += data; });
  return { child, output: () => output };
}

async function stop(child: ChildProcess): Promise<void> {
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

test("Go serve recovers an initial compilation error without restarting", async ({ page, request }) => {
  const root = await mkdtemp(resolve(workspace, "examples/.go-recovery-"));
  await cp(resolve(workspace, "examples/demo"), root, {
    recursive: true,
    filter: path => !path.split(/[\\/]/).some(part => part === "dist" || part === ".angulus"),
  });
  const htmlFile = resolve(root, "src/counter/counter.html");
  const originalHTML = await readFile(htmlFile, "utf8");
  await writeFile(htmlFile, "<section><h1>Broken startup template</h1>\n");
  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const server = launch("serve", root, port);
  try {
    await expect.poll(async () => {
      if (server.child.exitCode !== null) throw new Error(server.output());
      try { return (await request.get(url, { headers: { accept: "text/html" } })).status(); } catch { return 0; }
    }).toBe(500);
    await page.goto(url);
    await expect(page.locator("#__angulus-error")).toContainText(htmlFile);
    await page.reload();
    await expect(page.locator("#__angulus-error")).toContainText(htmlFile);
    await writeFile(htmlFile, originalHTML);
    await expect(page.locator("h1")).toHaveText("Angulus counter");
    await expect(page.locator("#__angulus-error")).toHaveCount(0);
    await page.locator("#increment").click();
    await expect(page.locator("#count")).toHaveText("Count: 1");
  } finally {
    await stop(server.child);
    await rm(root, { recursive: true, force: true });
  }
});

test("Go compiler serves router, reactive HTTP resources, and forms", async ({ page, request }) => {
  const root = resolve(workspace, "examples/demo");
  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const server = launch("serve", root, port);
  const pageErrors: string[] = [];
  page.on("pageerror", error => pageErrors.push(error.message));
  try {
    await expect.poll(async () => {
      if (server.child.exitCode !== null) throw new Error(server.output());
      try { return (await request.get(url, { headers: { accept: "text/html" } })).status(); } catch { return 0; }
    }).toBe(200);
    await page.goto(`${url}/http`);
    await expect(page.locator("h1")).toHaveText("HTTP resources");
    await expect(page.locator("#http-user")).toHaveText("User 1: Ada Lovelace");
    await page.getByRole("button", { name: "User 2", exact: true }).click();
    await expect(page.locator("#http-user")).toHaveText("User 2: Grace Hopper");
    await page.getByRole("button", { name: "Missing user" }).click();
    await expect(page.getByRole("alert")).toHaveText("HTTP 404: unable to load user.");
    await page.getByRole("button", { name: "Pause", exact: true }).click();
    await expect(page.locator("#http-state")).toHaveText("Status: idle");
    await page.getByRole("link", { name: "Forms", exact: true }).click();
    await page.getByLabel("Display name").fill("Ada");
    await page.getByLabel("Email", { exact: true }).fill("ada@example.com");
    await page.getByLabel("Confirm email").fill("ada@example.com");
    await page.getByRole("button", { name: "Save profile" }).click();
    await expect(page.getByRole("status")).toHaveText("Saved locally: ada@example.com");
    expect(pageErrors).toEqual([]);
  } finally {
    await stop(server.child);
  }
});

test("Go serve preserves state for CSS updates and reloads changed templates", async ({ page, request }) => {
  const root = await mkdtemp(resolve(workspace, "examples/.go-e2e-"));
  await cp(resolve(workspace, "examples/demo"), root, {
    recursive: true,
    filter: path => !path.split(/[\\/]/).some(part => part === "dist" || part === ".angulus"),
  });
  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const server = launch("serve", root, port);
  const pageErrors: string[] = [];
  const requestedURLs: string[] = [];
  page.on("pageerror", error => pageErrors.push(error.message));
  page.on("request", request => requestedURLs.push(request.url()));
  try {
    await expect.poll(async () => {
      if (server.child.exitCode !== null) throw new Error(server.output());
      try { return (await request.get(url, { headers: { accept: "text/html" } })).status(); } catch { return 0; }
    }).toBe(200);
    await page.goto(url);
    await expect(page).toHaveTitle("Angulus demo");
    await expect(page.locator("h1")).toHaveText("Angulus counter");
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

    const cssFile = resolve(root, "src/counter/counter.css");
    await writeFile(cssFile, `${await readFile(cssFile, "utf8")}\n.counter h1 { color: rgb(123, 45, 67); }\n`);
    await expect(page.locator("h1")).toHaveCSS("color", "rgb(123, 45, 67)");
    await expect(page.locator("#count")).toHaveText("Count: 2");

    const htmlFile = resolve(root, "src/counter/counter.html");
    const originalHTML = await readFile(htmlFile, "utf8");
    await writeFile(htmlFile, originalHTML.replace("Angulus counter", "Updated template"));
    await expect(page.locator("h1")).toHaveText("Updated template");
    await expect(page.locator("#count")).toHaveText("Count: 0");

    const lazyFile = resolve(root, "src/products/product.html");
    const originalLazy = await readFile(lazyFile, "utf8");
    expect(requestedURLs.some(requested => requested.includes("/src/products/product.component.generated"))).toBe(false);
    expect(requestedURLs.some(requested => requested.includes("/src/products/product.component.styles.generated.css"))).toBe(false);
    await writeFile(lazyFile, "<section><h1>Broken lazy template</h1>\n");
    const failedCheck = await command(["check"], root);
    expect(failedCheck.code, failedCheck.output).not.toBe(0);
    expect(failedCheck.output).toContain(lazyFile);
    await expect(page.locator("#__angulus-error")).toContainText(lazyFile);
    await writeFile(lazyFile, originalLazy);
    await expect(page.locator("#__angulus-error")).toHaveCount(0);

    await page.getByRole("link", { name: "Lazy product" }).click();
    await expect(page).toHaveURL(`${url}/products/42`);
    await expect(page.locator("h1")).toHaveText("Lazy product 42");
    await expect(page.locator("article")).toHaveCSS("border-left-width", "4px");
    expect(requestedURLs.some(requested => requested.includes("/src/products/product.component.styles.generated.css"))).toBe(true);
    await page.goBack();
    await expect(page.locator("#count")).toHaveText("Count: 0");
    await page.goForward();
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
    const previewPort = await freePort();
    const preview = launch("preview", root, previewPort);
    try {
      const previewURL = `http://127.0.0.1:${previewPort}`;
      await expect.poll(async () => {
        if (preview.child.exitCode !== null) throw new Error(preview.output());
        try { return (await request.get(previewURL, { headers: { accept: "text/html" } })).status(); } catch { return 0; }
      }).toBe(200);
      await page.goto(`${previewURL}/products/42`);
      await expect(page.locator("h1")).toHaveText("Lazy product 42");
      await page.getByRole("link", { name: "Back to counter" }).click();
      await page.locator("#increment").click();
      await expect(page.locator("#count")).toHaveText("Count: 1");
      expect((await request.get(`${previewURL}/missing.js`, { headers: { accept: "text/html" } })).status()).toBe(404);
      expect((await request.get(`${previewURL}/api/missing`, { headers: { accept: "text/html" } })).status()).toBe(404);
      expect(pageErrors).toEqual([]);
    } finally {
      await stop(preview.child);
    }
  } catch (error) {
    console.error(server.output());
    throw error;
  } finally {
    await stop(server.child);
    await rm(root, { recursive: true });
  }
});
