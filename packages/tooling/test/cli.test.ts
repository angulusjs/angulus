import assert from 'node:assert/strict';
import test from 'node:test';
import fs from 'node:fs';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { spawn, spawnSync, type SpawnSyncReturns } from 'node:child_process';
import { EventEmitter, once } from 'node:events';
import net from 'node:net';
import { fileURLToPath } from 'node:url';
import { generateComponent, parseArgs, readConfig } from '../src/cli.mjs';
import { angulus, isNavigationRequest } from '../src/vite.mjs';

const workspace = path.resolve(fileURLToPath(new URL('../../..', import.meta.url)));
const cli = path.join(workspace, 'packages/tooling/src/cli.mjs');
const workspaceBin = path.join(workspace, 'node_modules/.bin/angulus');
const fixtures = path.join(workspace, 'packages/tooling/test');

async function fixture(run: (root: string) => unknown) {
  const root = path.join(fixtures, `.cli-${randomUUID()}`);
  fs.mkdirSync(root, { recursive: true });
  try { await run(root); } finally { fs.rmSync(root, { recursive: true, force: true }); }
}

test('CLI validates options and application configuration', async () => {
  assert.equal(parseArgs(['serve', '--port', '5174']).port, 5174);
  assert.throws(() => parseArgs(['serve', '--port', '0']), /Port/);
  assert.throws(() => parseArgs(['serve', '--root']), /requires a value/);
  assert.throws(() => parseArgs(['serve', '--unknown']), /Unknown option/);
  await fixture((root) => {
    assert.deepEqual(readConfig(root), {});
    fs.writeFileSync(path.join(root, 'angulus.config.json'), '{"test":"npm test"}');
    assert.throws(() => readConfig(root), /argv array/);
  });
});

test('workspace bin symlink executes the CLI and propagates failures', () => {
  assert.ok(fs.lstatSync(workspaceBin).isSymbolicLink());
  const help = spawnSync(workspaceBin, ['--help'], { cwd: workspace, encoding: 'utf8' });
  assert.equal(help.status, 0, help.stderr);
  assert.match(help.stdout, /Angulus commands:/);
  const invalid = spawnSync(workspaceBin, ['unknown-command', '--json'], { cwd: workspace, encoding: 'utf8' });
  assert.equal(invalid.status, 1, invalid.stderr);
  assert.match(JSON.parse(invalid.stdout).message, /Unknown command/);
});

test('generator creates executable public API test and protects every existing file', async () => {
  await fixture((root) => {
    const files = generateComponent(root, 'my-counter');
    assert.equal(files.length, 4);
    assert.ok(files.every((file: string) => fs.existsSync(file)));
    const result = spawnSync(process.execPath, ['--import', 'tsx', '--test', files[3]], { cwd: workspace, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr + result.stdout);
    fs.writeFileSync(files[1], 'keep me');
    fs.unlinkSync(files[0]);
    assert.throws(() => generateComponent(root, 'my-counter'), /Refusing to overwrite/);
    assert.equal(fs.existsSync(files[0]), false);
    assert.equal(fs.readFileSync(files[1], 'utf8'), 'keep me');
    generateComponent(root, 'my-counter', true);
    assert.match(fs.readFileSync(files[0], 'utf8'), /class MyCounterComponent/);
    for (const name of ['../escape', '/absolute', 'two/parts', 'Bad Name', '']) {
      assert.throws(() => generateComponent(root, name), /kebab-case/);
    }
  });
});

test('test command invokes configured application argv without a shell', async () => {
  await fixture((root) => {
    fs.writeFileSync(path.join(root, 'angulus.config.json'), JSON.stringify({
      test: [process.execPath, '-e', 'console.log(process.argv[1]); process.exit(7)', 'literal; echo unsafe'],
    }));
    const result = spawnSync(process.execPath, [cli, 'test', '--root', root, '--json'], { encoding: 'utf8' });
    assert.equal(result.status, 7, result.stderr);
    assert.match(result.stderr, /literal; echo unsafe/);
    assert.equal(result.stdout.trim().split('\n').length, 1);
    assert.deepEqual(JSON.parse(result.stdout), { version: 1, type: 'tested', revision: 1, code: 7 });
  });
});

test('SPA fallback is limited to HTML navigation, excluding APIs and assets', () => {
  const request = (url: string, accept = 'text/html', method = 'GET') => ({ url, method, headers: { accept } });
  assert.equal(isNavigationRequest(request('/products/123')), true);
  for (const url of ['/api', '/api/missing', '/%61pi/missing', '/api%2Fmissing', '/missing.js', '/missing.css', '/logo.svg']) {
    assert.equal(isNavigationRequest(request(url)), false);
  }
  assert.equal(isNavigationRequest(request('/route', '*/*')), false);
  assert.equal(isNavigationRequest(request('/route', 'text/html', 'POST')), false);
});

test('occupied port exits promptly with a failure in text and JSON modes', async () => {
  await fixture(async root => {
    fs.writeFileSync(path.join(root, 'index.html'), '<!doctype html><title>Port test</title>');
    const socket = net.createServer();
    await new Promise<void>(resolve => socket.listen(0, '127.0.0.1', resolve));
    const address = socket.address();
    assert.ok(address && typeof address !== 'string');
    try {
      for (const json of [false, true]) {
        for (let attempt = 0; attempt < 3; attempt++) {
          const result: SpawnSyncReturns<string> = spawnSync(process.execPath, [
            cli, 'serve', '--root', root, '--host', '127.0.0.1', '--port', String(address.port),
            ...(json ? ['--json'] : []),
          ], { cwd: workspace, encoding: 'utf8', timeout: 10_000, killSignal: 'SIGKILL' });
          assert.ifError(result.error);
          assert.equal(result.signal, null, result.stdout + result.stderr);
          assert.equal(result.status, 1, result.stdout + result.stderr);
          assert.match(result.stdout + result.stderr, /port .*already in use/i);
          if (json) {
            const events = result.stdout.trim().split('\n').map(line => JSON.parse(line));
            assert.ok(events.some(event => event.type === 'error' && /already in use/.test(event.message)));
            assert.ok(events.every(event => event.type !== 'listening'));
          }
        }
      }
    } finally {
      await new Promise<void>((resolve, reject) => socket.close(error => error ? reject(error) : resolve()));
    }
  });
});

function deferred() {
  let release!: () => void;
  const promise = new Promise<void>(resolve => { release = resolve; });
  return { promise, release };
}

for (const stage of ['startup', 'checking']) {
  test(`plugin shutdown waits for ${stage} without restarting watches`, { timeout: 5000 }, async () => {
    const entered = deferred();
    const work = deferred();
    let closes = 0;
    let checks = 0;
    let watched = 0;
    let closed = false;
    const events: { type: string }[] = [];
    const project = {
      dependencies: new Map<string, Set<string>>(),
      async start() {
        if (stage === 'startup') { entered.release(); await work.promise; }
      },
      async check() {
        checks++;
        if (stage === 'checking') { entered.release(); await work.promise; }
        return [];
      },
      invalidate() {},
      async close() { closes++; },
    };
    const watcher = Object.assign(new EventEmitter(), { add() { watched++; } });
    const plugin = angulus({ project, onEvent: (event: { type: string }) => events.push(event) });
    plugin.configResolved({ root: workspace, command: 'serve' });
    plugin.configureServer({
      watcher, middlewares: { use() {} },
      ws: { send() { assert.fail('No browser updates are allowed after shutdown'); } },
    });
    try {
      await entered.promise;
      const closing = plugin.closeBundle();
      assert.equal(plugin.closeBundle(), closing);
      const finished = closing.then(() => { closed = true; });
      await Promise.resolve();
      assert.equal(closed, false);
      assert.equal(closes, 0);
      work.release();
      await finished;
      assert.equal(closes, 1);
      assert.equal(checks, stage === 'startup' ? 0 : 1);
      assert.equal(watched, 0);
      assert.equal(watcher.listenerCount('all'), 0);
      assert.deepEqual(events.map(event => event.type), ['checking']);
      await assert.rejects(plugin.buildStart(), /closed/);
    } finally {
      work.release();
      await plugin.closeBundle();
    }
  });
}

test('plugin shares one project, watches dependencies, uses CSS pipeline and reloads templates', async () => {
  const file = path.join(workspace, 'component.ts');
  const css = path.join(workspace, 'component.css');
  const html = path.join(workspace, 'component.html');
  let starts = 0;
  let closes = 0;
  let checks = 0;
  let activeChecks = 0;
  let maxChecks = 0;
  const project = {
    dependencies: new Map([[file, new Set([css, html])]]),
    async start() { starts++; },
    async compile() { return { code: 'compiled', map: {}, dependencies: [html, css] }; },
    async style() { return { code: '.counter{}', map: {} }; },
    async check() {
      checks++;
      maxChecks = Math.max(maxChecks, ++activeChecks);
      await new Promise((resolve) => setTimeout(resolve, 45));
      activeChecks--;
      return [];
    },
    invalidate() {},
    async close() { closes++; },
  };
  const events: any[] = [];
  const plugin = angulus({ project, onEvent: (event: any) => events.push(event) });
  plugin.configResolved({ root: workspace, command: 'serve' });
  const watched: string[] = [];
  const context = { addWatchFile(file: string) { watched.push(file); } };
  const transformed = await plugin.transform.call(context, '', file);
  assert.ok(transformed);
  assert.equal(transformed.code, 'compiled');
  assert.ok(watched.includes(html));
  assert.ok(!watched.includes(css));
  const styleId = plugin.resolveId(file + '?angulus-style.css', file);
  assert.ok(styleId);
  assert.equal(plugin.resolveId(file + '?angulus-style', file), styleId);
  assert.ok(styleId.endsWith('.css'));
  const style = await plugin.load.call(context, styleId);
  assert.ok(style);
  assert.equal(style.code, '.counter{}');
  assert.ok(watched.includes(css) && watched.includes(html));
  assert.equal(starts, 1);
  const cssModule = { id: styleId };
  const scriptModule = { id: file };
  const sent: any[] = [];
  const invalidated: any[] = [];
  const watcher = Object.assign(new EventEmitter(), {
    add(files: string[]) { watched.push(...files); },
  });
  const server = {
    watcher, middlewares: { use() {} }, ws: { send(message: any) { sent.push(message); } },
    moduleGraph: {
      getModuleById(id: string) { return id === styleId ? cssModule : id === file ? scriptModule : undefined; },
      invalidateModule(module: any) { invalidated.push(module); },
    },
  };
  plugin.configureServer(server);
  const cssUpdates = plugin.handleHotUpdate({ file: css, modules: [], server });
  assert.deepEqual(cssUpdates, [cssModule]);
  assert.equal(sent.some((event) => event.type === 'full-reload'), false);
  plugin.handleHotUpdate({ file: html, modules: [], server });
  assert.ok(sent.some((event) => event.type === 'full-reload'));
  assert.ok(invalidated.includes(scriptModule));
  const messagesBeforeGeneratedChange = sent.length;
  assert.deepEqual(plugin.handleHotUpdate({ file: path.join(workspace, '.angulus/check/generated.ts'), modules: [], server }), []);
  assert.equal(sent.length, messagesBeforeGeneratedChange);
  while (checks === 0) await new Promise((resolve) => setTimeout(resolve, 5));
  watcher.emit('all', 'change', path.join(workspace, 'unopened.ts'));
  await new Promise((resolve) => setTimeout(resolve, 140));
  assert.equal(maxChecks, 1);
  assert.equal(checks, 2);
  assert.ok(events.some((event) => event.type === 'checked' && event.revision === 2 && event.valid === true && !event.stale));
  await plugin.closeBundle();
  assert.equal(closes, 1);
  assert.equal(watcher.listenerCount('all'), 0);
});

test('preview serves nested routes but never HTML for missing assets or API routes', async () => {
  await fixture(async (root) => {
    fs.mkdirSync(path.join(root, 'dist'));
    fs.writeFileSync(path.join(root, 'dist/index.html'), '<!doctype html><h1>Angulus preview</h1>');
    const socket = net.createServer();
    await new Promise<void>((resolve) => socket.listen(0, '127.0.0.1', resolve));
    const port = (socket.address() as net.AddressInfo).port;
    await new Promise<void>((resolve) => socket.close(() => resolve()));
    const child = spawn(process.execPath, [cli, 'preview', '--root', root, '--host', '127.0.0.1', '--port', String(port), '--json'], {
      cwd: workspace, stdio: ['ignore', 'pipe', 'pipe'],
    });

    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (data) => { stdout += data; });
    child.stderr.on('data', (data) => { stderr += data; });
    try {
      for (let index = 0; !stdout.includes('"listening"') && index < 150 && child.exitCode === null; index++) {
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
      assert.match(stdout, /"listening"/, stderr + stdout);
      assert.equal(JSON.parse(stdout.trim()).version, 1);
      const base = `http://127.0.0.1:${port}`;
      const navigation = await fetch(`${base}/nested/route`, { headers: { Accept: 'text/html' } });
      assert.equal(navigation.status, 200);
      assert.match(await navigation.text(), /Angulus preview/);
      for (const url of ['/missing.js', '/missing.css', '/api', '/api/missing']) {
        const response = await fetch(base + url, { headers: { Accept: 'text/html' } });
        assert.equal(response.status, 404, url);
        assert.doesNotMatch(await response.text(), /Angulus preview/);
      }
      const notNavigation = await fetch(base + '/unknown', { headers: { Accept: '*/*' } });
      assert.equal(notNavigation.status, 404);
    } finally {
      if (child.exitCode === null) {
        const exited = once(child, 'exit');
        child.kill('SIGTERM');
        await exited;
      }
    }
  });
});

test('Vite actually processes component styles through its CSS pipeline', async () => {
  await fixture(async (root) => {
    const file = path.join(root, 'component.ts');
    const css = path.join(root, 'component.css');
    fs.writeFileSync(file, 'export const count = 1');
    fs.writeFileSync(css, '.counter { color: red; }');
    fs.writeFileSync(path.join(root, 'index.html'), '<script type="module" src="/component.ts"></script>');
    const map = { version: 3, sources: [file], sourcesContent: [''], names: [], mappings: '' };
    let starts = 0;
    let closes = 0;
    const project = {
      dependencies: new Map([[file, new Set([css])]]),
      async start() { starts++; },
      async compile(id: string) {
        return id === file ? { code: `import ${JSON.stringify(file + '?angulus-style.css')}; export const count: number = 1;`, map, dependencies: [css] } : null;
      },
      async style(id: string) {
        assert.equal(id, file);
        return { code: fs.readFileSync(css, 'utf8'), map };
      },
      async check() { return []; },
      invalidate() {},
      async close() { closes++; },
    };
    const { createServer } = await import('vite');
    const server = await createServer({
      root, configFile: false, logLevel: 'silent',
      plugins: [angulus({ project })],
      server: { host: '127.0.0.1', port: 0 },
    });
    try {
      await server.listen();
      const port = (server.httpServer!.address() as net.AddressInfo).port;
      const source = await (await fetch(`http://127.0.0.1:${port}/component.ts`)).text();
      assert.match(source, /angulus-style\.css/);
      assert.doesNotMatch(source, /count: number/);
      const style = await (await fetch(`http://127.0.0.1:${port}/component.ts.angulus-style.css`)).text();
      assert.match(style, /updateStyle/);
      assert.match(style, /color: red/);
      assert.equal(starts, 1);
    } finally { await server.close(); }
    assert.equal(closes, 1);
  });
});

test('exported Vite plugin fails production builds on full-project diagnostics', async () => {
  let checked = false;
  const plugin = angulus({
    project: {
      async start() {},
      async check() {
        checked = true;
        return [{ severity: 'error', file: 'lazy.html', line: 2, column: 4, code: 'TS2339', message: 'Missing member' }];
      },
      async close() {},
    },
  });
  plugin.configResolved({ root: workspace, command: 'build' });
  await assert.rejects(
    plugin.buildStart.call({ error(message: string) { throw new Error(message); } }),
    /lazy\.html:2:4 TS2339: Missing member/,
  );
  assert.equal(checked, true);
  await plugin.closeBundle();
});
