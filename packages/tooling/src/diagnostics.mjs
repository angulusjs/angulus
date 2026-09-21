export function diagnostic(file, source, start, code, message, end = start + 1) {
  start = Math.max(0, Math.min(start, source.length));
  end = Math.max(start, Math.min(end, source.length));
  const before = source.slice(0, start);
  const line = before.split("\n").length;
  const column = start - before.lastIndexOf("\n");
  return { file, start, end, line, column, code, message, severity: "error" };
}

export class CompilationError extends Error {
  constructor(diagnostics) {
    super(diagnostics.map(d => `${d.file}:${d.line}:${d.column} ${d.code}: ${d.message}`).join("\n"));
    this.diagnostics = diagnostics;
  }
}

export function stderrLogger() {
  const write = message => process.stderr.write(`${message}\n`);
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
