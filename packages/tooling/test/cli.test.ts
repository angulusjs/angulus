import assert from 'node:assert/strict';
import test from 'node:test';
import fs from 'node:fs';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import net from 'node:net';
import { fileURLToPath } from 'node:url';
import { cliLocation } from '../src/binary.mjs';

const workspace = path.resolve(fileURLToPath(new URL('../../..', import.meta.url)));
const cli = path.join(workspace, 'packages/tooling/src/cli.mjs');
const workspaceBin = path.join(workspace, 'node_modules/.bin/angulus');
const fixtures = path.join(workspace, 'packages/tooling/test');

async function fixture(run: (root: string) => unknown) {
  const root = path.join(fixtures, `.cli-${randomUUID()}`);
  fs.mkdirSync(root, { recursive: true });
  try { await run(root); } finally { fs.rmSync(root, { recursive: true, force: true }); }
}

function writeApplication(root: string) {
  fs.mkdirSync(path.join(root, 'src'), { recursive: true });
  fs.writeFileSync(path.join(root, 'package.json'), JSON.stringify({
    name: 'angulus-cli-fixture',
    private: true,
    type: 'module',
    scripts: { test: "node -e \"console.log('application tests ran')\"" },
  }));
  fs.writeFileSync(path.join(root, 'src/counter.component.json'), JSON.stringify({
    selector: 'app-counter',
    module: './counter.ts',
    export: 'Counter',
    template: './counter.html',
  }));
  fs.writeFileSync(path.join(root, 'src/counter.ts'), 'export class Counter { count = 1; }\n');
  fs.writeFileSync(path.join(root, 'src/counter.html'), '<p>{{ count }}</p>\n');
  fs.writeFileSync(path.join(root, 'src/main.ts'), 'import "./counter.component.generated";\n');
  fs.writeFileSync(path.join(root, 'index.html'), '<!doctype html><script type="module" src="./src/main.ts"></script>\n');
}

test('source workspace launcher resolves the Go CLI binary without compiling it', async () => {
  const location = await cliLocation();
  assert.equal(location.sourceRoot, workspace);
  assert.equal(location.binary, path.join(workspace, '.angulus/bin', process.platform === 'win32' ? 'angulus.exe' : 'angulus'));
  assert.ok(fs.existsSync(location.binary), 'npm pretest must build the Go CLI before tooling tests');
});

test('npm bin command runs the Go CLI with inherited stdio', async () => {
  await fixture(async root => {
    writeApplication(root);
    const result = spawnSync(workspaceBin, ['check', '--root', root], {
      cwd: workspace, encoding: 'utf8', shell: process.platform === 'win32',
    });
    assert.equal(result.status, 0, result.error?.message ?? result.stderr);
    assert.match(result.stdout, /Checked 1 component/);
  });
});

test('CLI launcher delegates descriptor check, ESM build and application tests', async () => {
  await fixture(async root => {
    writeApplication(root);
    const check = spawnSync(process.execPath, [cli, 'check', '--root', root], { cwd: workspace, encoding: 'utf8' });
    assert.equal(check.status, 0, check.stderr);
    assert.match(check.stdout, /Checked 1 component/);

    const build = spawnSync(process.execPath, [cli, 'build', '--root', root], { cwd: workspace, encoding: 'utf8' });
    assert.equal(build.status, 0, build.stderr);
    assert.match(build.stdout, /as ESM/);
    assert.ok(fs.existsSync(path.join(root, 'dist/index.html')));
    assert.ok(fs.existsSync(path.join(root, 'dist/src/main.js')));

    const applicationTest = spawnSync(process.execPath, [cli, 'test', '--root', root], { cwd: workspace, encoding: 'utf8' });
    assert.equal(applicationTest.status, 0, applicationTest.stderr);
    assert.match(applicationTest.stdout, /application tests ran/);
  });
});

test('CLI reports invalid descriptor projects through stdio and a failing exit code', async () => {
  await fixture(async root => {
    const result = spawnSync(process.execPath, [cli, 'check', '--root', root], { cwd: workspace, encoding: 'utf8' });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /No \*\.component\.json component descriptors were found/);
  });
});

test('CLI library mode validates the native library manifest', async () => {
  await fixture(async root => {
    writeApplication(root);
    const result = spawnSync(process.execPath, [cli, 'build', '--lib', '--root', root], {
      cwd: workspace, encoding: 'utf8',
    });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /library package.json must declare name and version/);
  });
});

test('CLI launcher forwards SIGINT and SIGTERM with conventional exit statuses', async t => {
  if (process.platform === 'win32') {
    t.skip('Windows does not provide POSIX SIGTERM process delivery');
    return;
  }
  await fixture(async root => {
    writeApplication(root);
    for (const [signal, expected] of [['SIGINT', 130], ['SIGTERM', 143]] as const) {
      const socket = net.createServer();
      await new Promise<void>(resolve => socket.listen(0, '127.0.0.1', resolve));
      const address = socket.address();
      if (!address || typeof address === 'string') throw new Error('Could not reserve a test port');
      const port = address.port;
      await new Promise<void>(resolve => socket.close(() => resolve()));

      const child = spawn(process.execPath, [cli, 'serve', '--root', root, '--host', '127.0.0.1', '--port', String(port)], {
        cwd: workspace,
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      let output = '';
      child.stdout.on('data', chunk => { output += chunk; });
      child.stderr.on('data', chunk => { output += chunk; });
      const ready = new Promise<void>((resolve, reject) => {
        const timeout = setTimeout(() => reject(new Error(`CLI did not start: ${output}`)), 30_000);
        const poll = () => {
          if (output.includes('Angulus dev server:')) {
            clearTimeout(timeout);
            resolve();
          } else if (child.exitCode !== null) {
            clearTimeout(timeout);
            reject(new Error(`CLI exited before listening (${child.exitCode}): ${output}`));
          } else setTimeout(poll, 20);
        };
        poll();
      });
      try {
        await ready;
        child.kill(signal);
        const [code, actualSignal] = await once(child, 'close');
        assert.equal(code, expected, `exit signal ${String(actualSignal)}; ${output}`);
      } finally {
        if (child.exitCode === null) child.kill('SIGKILL');
      }
    }
  });
});
