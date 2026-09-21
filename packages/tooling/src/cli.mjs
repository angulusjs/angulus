#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { spawn } from 'node:child_process';
import { Project } from './project.mjs';
import { angulus } from './vite.mjs';

const help = `Angulus commands:
  angulus serve [--root path] [--host host] [--port port] [--json]
  angulus check [--root path] [--json]
  angulus build [--root path] [--json]
  angulus preview [--root path] [--host host] [--port port]
  angulus test [--root path]
  angulus generate component <kebab-case-name> [--root path] [--force]

Optional angulus.config.json: { "port": 5173, "host": "localhost",
  "proxy": { "/api": "http://localhost:3000" },
  "test": ["node", "--import", "tsx", "--test", "src/counter/counter.test.ts"] }
JSON output is NDJSON: {version:1,type,revision,...}. "listening" means HTTP
ready, not type-safe; "checked" contains diagnostics and may be stale.
`;

/**
 * @typedef {{ root: string, positional: string[], json: boolean, force: boolean,
 *   help?: boolean, host?: string, port?: number }} CLIOptions
 */

/**
 * @param {string[]} args
 * @returns {CLIOptions}
 */
export function parseArgs(args) {
  /** @type {CLIOptions} */
  const options = { root: process.cwd(), positional: [], json: false, force: false };
  for (let index = 0; index < args.length; index++) {
    const arg = args[index];
    if (arg === '--json') options.json = true;
    else if (arg === '--force') options.force = true;
    else if (arg === '--help' || arg === '-h') options.help = true;
    else if (['--root', '--host', '--port'].includes(arg)) {
      const value = args[++index];
      if (!value || value.startsWith('--')) throw new Error(`${arg} requires a value`);
      if (arg === '--port') options.port = parsePort(value);
      else if (arg === '--host') options.host = value;
      else options.root = value;
    } else if (arg.startsWith('-')) throw new Error(`Unknown option: ${arg}`);
    else options.positional.push(arg);
  }
  options.root = path.resolve(options.root);
  return options;
}

function parsePort(value) {
  const port = Number(value);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Port must be an integer between 1 and 65535');
  return port;
}

export function readConfig(root) {
  const file = path.join(root, 'angulus.config.json');
  const config = fs.existsSync(file) ? JSON.parse(fs.readFileSync(file, 'utf8')) : {};
  if (!config || typeof config !== 'object' || Array.isArray(config)) throw new Error('angulus.config.json must contain an object');
  if (config.port !== undefined) config.port = parsePort(config.port);
  if (config.host !== undefined && (typeof config.host !== 'string' || !config.host)) throw new Error('host must be a nonempty string');
  if (config.proxy !== undefined && (!config.proxy || typeof config.proxy !== 'object' || Array.isArray(config.proxy))) throw new Error('proxy must be an object');
  if (config.test !== undefined && (!Array.isArray(config.test) || !config.test.length || config.test.some((arg) => typeof arg !== 'string' || !arg))) {
    throw new Error('test must be a nonempty command argv array');
  }
  return config;
}

export function generateComponent(root, name, force = false) {
  if (!/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/.test(name ?? '')) {
    throw new Error('Component name must be kebab-case without paths (for example: product-card)');
  }
  const className = name.split('-').map((part) => part[0].toUpperCase() + part.slice(1)).join('') + 'Component';
  const directory = path.join(root, 'src', name);
  const files = {
    [`${name}.ts`]: `import { Component, signal } from '@angulus/core';

@Component({
  selector: 'app-${name}',
  templateUrl: './${name}.html',
  styleUrl: './${name}.css',
})
export class ${className} {
  readonly count = signal(0);

  increment(): void {
    this.count.update(value => value + 1);
  }
}
`,
    [`${name}.html`]: `<section class="counter">
  <span>Count: {{ count() }}</span>
  <button (click)="increment()">Increment</button>
</section>
`,
    [`${name}.css`]: `.counter {
  display: flex;
  align-items: center;
  gap: 12px;
}
`,
    [`${name}.test.ts`]: `import assert from 'node:assert/strict';
import test from 'node:test';
import { ${className} } from './${name}.ts';

test('${name} increments independently', () => {
  const first = new ${className}();
  const second = new ${className}();
  first.increment();
  assert.equal(first.count(), 1);
  assert.equal(second.count(), 0);
});
`,
  };
  const destinations = Object.keys(files).map((file) => path.join(directory, file));
  if (!force) {
    const existing = destinations.find((file) => fs.existsSync(file));
    if (existing) throw new Error(`Refusing to overwrite ${existing}; use --force`);
  }
  // Do not allow an existing symlink to redirect generated source outside the root.
  for (const file of [path.join(root, 'src'), directory, ...destinations]) {
    if (fs.lstatSync(file, { throwIfNoEntry: false })?.isSymbolicLink()) throw new Error(`Refusing to write through symlink: ${file}`);
  }
  fs.mkdirSync(directory, { recursive: true });
  for (const [file, contents] of Object.entries(files)) fs.writeFileSync(path.join(directory, file), contents, { flag: force ? 'w' : 'wx' });
  return destinations;
}

function formatDiagnostics(diagnostics) {
  return diagnostics.map((diagnostic) => `${diagnostic.file}:${diagnostic.line}:${diagnostic.column} ${diagnostic.severity} ${diagnostic.code}: ${diagnostic.message}`).join('\n');
}

function installShutdown(close) {
  let stopping;
  const shutdown = (signal) => {
    if (stopping) return;
    stopping = Promise.resolve().then(close).finally(() => {
      process.exitCode = signal === 'SIGINT' ? 130 : 143;
      process.removeListener('SIGINT', onInterrupt);
      process.removeListener('SIGTERM', onTerminate);
    });
    stopping.catch((error) => { process.stderr.write(`${error.message}\n`); });
  };
  const onInterrupt = () => shutdown('SIGINT');
  const onTerminate = () => shutdown('SIGTERM');
  process.once('SIGINT', onInterrupt);
  process.once('SIGTERM', onTerminate);
  return () => {
    process.removeListener('SIGINT', onInterrupt);
    process.removeListener('SIGTERM', onTerminate);
  };
}

function stderrLogger() {
  const write = (message) => process.stderr.write(`${message}\n`);
  const warned = new Set();
  return {
    hasWarned: false,
    info: write,
    warn(message) { this.hasWarned = true; write(message); },
    warnOnce(message) { if (!warned.has(message)) { warned.add(message); this.warn(message); } },
    error: write,
    clearScreen() {},
    hasErrorLogged() { return false; },
  };
}

export async function main(args = process.argv.slice(2)) {
  const options = parseArgs(args);
  const [command, ...rest] = options.positional;
  const emit = (event) => {
    if (options.json) process.stdout.write(`${JSON.stringify({ version: 1, ...event })}\n`);
    else if (event.type === 'checked') {
      const text = formatDiagnostics(event.diagnostics);
      if (text) process.stderr.write(`${text}\n`);
      else process.stdout.write(`Check passed (revision ${event.revision}).\n`);
    } else if (event.type === 'listening') {
      process.stdout.write(`Angulus ${command}: ${event.url}\n`);
    }
  };
  if (options.help || !command) { process.stdout.write(help); return 0; }
  if (!['serve', 'check', 'build', 'preview', 'test', 'generate'].includes(command)) throw new Error(`Unknown command: ${command}`);
  if (command === 'generate') {
    if (rest.length !== 2 || rest[0] !== 'component') throw new Error('Usage: angulus generate component <name>');
    const files = generateComponent(options.root, rest[1], options.force);
    if (options.json) emit({ type: 'generated', revision: 1, files });
    else process.stdout.write(files.map((file) => `Created ${file}`).join('\n') + '\n');
    return 0;
  }
  if (rest.length) throw new Error(`Unexpected arguments: ${rest.join(' ')}`);
  if (!fs.existsSync(options.root) || !fs.statSync(options.root).isDirectory()) throw new Error(`Project root does not exist: ${options.root}`);
  const config = readConfig(options.root);
  if (command === 'test') {
    if (!config.test) throw new Error('Configure the application test command as a "test" argv array in angulus.config.json');
    const child = spawn(config.test[0], config.test.slice(1), {
      cwd: options.root, shell: false,
      stdio: options.json ? ['inherit', 'pipe', 'pipe'] : 'inherit',
      env: { ...process.env, PATH: `${path.join(options.root, 'node_modules', '.bin')}${path.delimiter}${process.env.PATH ?? ''}` },
    });
    if (options.json) {
      child.stdout.pipe(process.stderr);
      child.stderr.pipe(process.stderr);
    }
    const unhook = installShutdown(() => { child.kill('SIGTERM'); });
    try {
      const code = await new Promise((resolve, reject) => {
        child.once('error', reject);
        child.once('exit', (exitCode, signal) => resolve(exitCode ?? (signal === 'SIGINT' ? 130 : 143)));
      });
      emit({ type: 'tested', revision: 1, code });
      return code;
    } finally { unhook(); }
  }
  const project = new Project(options.root);
  let resource;
  const close = async () => {
    try {
      if (resource?.close) await resource.close();
      else if (resource?.httpServer) await new Promise((resolve, reject) => resource.httpServer.close((error) => error ? reject(error) : resolve()));
    } finally { await project.close(); }
  };
  const unhook = installShutdown(close);
  try {
    if (command === 'check' || command === 'build') {
      await project.start();
      const diagnostics = await project.check();
      emit({
        type: 'checked', revision: 1, diagnostics,
        valid: !diagnostics.some((diagnostic) => diagnostic.severity === 'error'), stale: false,
      });
      if (diagnostics.some((diagnostic) => diagnostic.severity === 'error')) return 1;
      if (command === 'check') return 0;
    }
    const vite = await import('vite');
    const common = {
      root: options.root, configFile: false, clearScreen: false,
      customLogger: options.json ? stderrLogger() : undefined,
      plugins: [angulus({ project, onEvent: emit, checkBuild: false })],
      appType: 'spa',
    };
    if (command === 'build') {
      await vite.build(common);
      emit({ type: 'built', revision: 1, directory: path.join(options.root, 'dist') });
      return 0;
    }
    const network = {
      host: options.host ?? config.host ?? 'localhost',
      port: options.port ?? config.port ?? 5173,
      strictPort: true,
      proxy: config.proxy,
    };
    if (command === 'serve') {
      resource = await vite.createServer({ ...common, server: network });
      await resource.listen();
    } else {
      if (!fs.existsSync(path.join(options.root, 'dist', 'index.html'))) throw new Error('No built dist/index.html found; run angulus build first');
      resource = await vite.preview({ ...common, preview: network });
    }
    const url = resource.resolvedUrls?.local[0] ?? resource.resolvedUrls?.network[0];
    emit({ type: 'listening', revision: 0, url, compilerPid: project.pid });
    return 0;
  } catch (error) {
    await close();
    unhook();
    throw error;
  } finally {
    if (!resource) { unhook(); await project.close(); }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(fs.realpathSync(process.argv[1])).href) {
  main().then((code) => { if (!process.exitCode) process.exitCode = code; }).catch((error) => {
    if (process.argv.includes('--json')) process.stdout.write(JSON.stringify({ version: 1, type: 'error', revision: 0, message: error.message }) + '\n');
    else process.stderr.write(`Angulus: ${error.message}\n`);
    process.exitCode = 1;
  });
}
