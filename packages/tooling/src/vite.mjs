import path from 'node:path';
import { Project } from './project.mjs';

const styleQuery = '?angulus-style.css';
const legacyStyleQuery = '?angulus-style';
const styleSuffix = '.angulus-style.css';

export function isNavigationRequest(req) {
  let pathname;
  try { pathname = decodeURIComponent(new URL(req.url ?? '/', 'http://localhost').pathname); }
  catch { return false; }
  return (req.method === 'GET' || req.method === 'HEAD')
    && (req.headers.accept ?? '').includes('text/html')
    && pathname !== '/api' && !pathname.startsWith('/api/')
    && !path.posix.extname(pathname);
}

// Run before Vite's HTML fallback: APIs and missing assets must remain 404s.
export function navigationFallbackGuard(req, _res, next) {
  if (!isNavigationRequest(req)) {
    req.headers.accept = 'application/octet-stream';
    next();
  } else {
    next();
  }
}

export function angulus(options = {}) {
  let project;
  let server;
  let startup;
  let closed = false;
  let revision = 0;
  let checkedRevision = 0;
  let checking;
  let timer;
  let root;
  let command;
  let logger;
  let watcherListener;
  let shutdown;

  const emit = (event) => {
    if (options.onEvent) options.onEvent({ version: 1, ...event });
    else if (event.type === 'checked' && !event.stale) {
      for (const diagnostic of event.diagnostics) {
        logger?.[diagnostic.severity === 'error' ? 'error' : 'warn'](
          `${diagnostic.file}:${diagnostic.line}:${diagnostic.column} ${diagnostic.code}: ${diagnostic.message}`,
        );
      }
    }
  };
  const start = () => {
    if (closed) return Promise.reject(new Error('Angulus Vite plugin has been closed'));
    return startup ??= project.start();
  };
  const ignored = file => /(^|[/\\])(node_modules|dist|\.angulus|\.git)([/\\]|$)/.test(path.relative(root, file));
  const tracked = file => [...project.dependencies.values()].some(dependencies => dependencies.has(file));
  const reportFailure = (error) => [{
    file: root, start: 0, end: 0, line: 1, column: 1,
    code: 'ANGULUS_TOOLING', severity: 'error', message: error.message ?? String(error),
  }];
  const runChecks = () => {
    if (checking || closed) return;
    checking = (async () => {
      while (!closed && checkedRevision < revision) {
        const current = revision;
        emit({ type: 'checking', revision: current });
        let diagnostics;
        try {
          await start();
          if (closed) break;
          diagnostics = await project.check();
          if (!closed) server?.watcher.add([...new Set([...project.dependencies.values()].flatMap(dependencies => [...dependencies]))]);
        } catch (error) {
          diagnostics = reportFailure(error);
        }
        checkedRevision = current;
        if (closed) break;
        emit({
          type: 'checked', revision: current, diagnostics,
          valid: !diagnostics.some((diagnostic) => diagnostic.severity === 'error'),
          stale: current !== revision,
        });
        if (current !== revision) continue;
        server?.ws.send({ type: 'custom', event: 'angulus:diagnostics', data: { revision: current, diagnostics } });
        const first = diagnostics.find((diagnostic) => diagnostic.severity === 'error');
        if (first) {
          server?.ws.send({
            type: 'error',
            err: {
              message: diagnostics.map((d) => `${d.file}:${d.line}:${d.column} ${d.code}: ${d.message}`).join('\n'),
              stack: '', plugin: 'angulus', id: first.file,
              loc: { file: first.file, line: first.line, column: first.column },
            },
          });
        }
      }
    })().finally(() => { checking = undefined; });
  };
  const scheduleCheck = () => {
    revision++;
    clearTimeout(timer);
    timer = setTimeout(runChecks, 25);
  };
  const close = () => {
    if (shutdown) return shutdown;
    closed = true;
    clearTimeout(timer);
    if (watcherListener) server?.watcher.off('all', watcherListener);
    shutdown = (async () => {
      try {
        await startup;
        await checking;
      } finally { await project?.close(); }
    })();
    return shutdown;
  };

  return {
    name: 'angulus',
    enforce: 'pre',
    config() {
      return {
        resolve: { dedupe: ['@angulus/core'] },
        optimizeDeps: { include: ['@angulus/core'] },
      };
    },
    configResolved(config) {
      root = config.root;
      command = config.command;
      logger = config.logger;
      project = options.project ?? new Project(root);
    },
    async buildStart() {
      await start();
      if (command === 'build' && options.checkBuild !== false) {
        const diagnostics = await project.check();
        emit({
          type: 'checked', revision: 1, diagnostics,
          valid: !diagnostics.some((diagnostic) => diagnostic.severity === 'error'), stale: false,
        });
        const errors = diagnostics.filter((diagnostic) => diagnostic.severity === 'error');
        if (errors.length) this.error(errors.map((d) => `${d.file}:${d.line}:${d.column} ${d.code}: ${d.message}`).join('\n'));
      }
    },
    configureServer(devServer) {
      server = devServer;
      server.middlewares.use(navigationFallbackGuard);
      watcherListener = (_event, file) => {
        const relative = path.relative(root, file);
        if (ignored(file) || (relative.startsWith('..') && !tracked(file))) return;
        if (!/\.(?:[cm]?tsx?|html|css|json)$/.test(file)) return;
        project.invalidate(file);
        scheduleCheck();
      };
      server.watcher.on('all', watcherListener);
      scheduleCheck();
    },
    configurePreviewServer(previewServer) {
      previewServer.middlewares.use(navigationFallbackGuard);
    },
    transformIndexHtml: {
      order: 'pre',
      handler() {
        if (!server) return;
        return [{
          tag: 'script', attrs: { type: 'module' }, injectTo: 'head',
          children: `if (import.meta.hot) import.meta.hot.on('angulus:diagnostics', ({diagnostics}) => {
  if (!diagnostics.some(d => d.severity === 'error')) document.querySelector('vite-error-overlay')?.remove();
});`,
        }];
      },
    },
    resolveId(source, importer) {
      const query = source.endsWith(styleQuery) ? styleQuery : source.endsWith(legacyStyleQuery) ? legacyStyleQuery : undefined;
      if (!query && !source.endsWith(styleSuffix)) return;
      const file = query ? source.slice(0, -query.length) + styleSuffix : source;
      return path.resolve(importer ? path.dirname(importer.split('?')[0]) : root, file);
    },
    async load(id) {
      if (!id.endsWith(styleSuffix)) return;
      await start();
      const file = id.slice(0, -styleSuffix.length);
      const result = await project.style(file);
      for (const dependency of project.dependencies.get(file) ?? []) {
        if (dependency.endsWith('.css')) this.addWatchFile(dependency);
      }
      return result;
    },
    async transform(_code, id) {
      if (id.includes('?') || !/\.tsx?$/.test(id) || id.includes('/node_modules/')) return;
      await start();
      const result = await project.compile(id);
      if (!result) return;
      // CSS dependencies belong to the self-accepting style module, not its TS importer.
      for (const dependency of result.dependencies) {
        if (!dependency.endsWith('.css')) this.addWatchFile(dependency);
      }
      return { code: result.code, map: result.map };
    },
    handleHotUpdate(context) {
      if (ignored(context.file)) return [];
      const owners = [];
      for (const [owner, dependencies] of project.dependencies) {
        if (owner === context.file || dependencies.has(context.file)) owners.push(owner);
      }
      project.invalidate(context.file);
      const cssOnly = context.file.endsWith('.css');
      const modules = new Set(context.modules);
      for (const owner of owners) {
        const styleModule = context.server.moduleGraph.getModuleById(owner + styleSuffix);
        const scriptModule = context.server.moduleGraph.getModuleById(owner);
        if (styleModule) {
          context.server.moduleGraph.invalidateModule(styleModule);
          modules.add(styleModule);
        }
        if (!cssOnly && scriptModule) context.server.moduleGraph.invalidateModule(scriptModule);
      }
      if (cssOnly) return [...modules].filter((module) => module.id?.endsWith(styleSuffix) || module.id?.split('?')[0].endsWith('.css'));
      if (owners.length || /\.(?:tsx?|html)$/.test(context.file)) {
        context.server.ws.send({ type: 'full-reload', path: '*' });
        return [];
      }
    },
    closeBundle: close,
  };
}

export default angulus;
