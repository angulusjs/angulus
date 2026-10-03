export interface Diagnostic {
  file: string;
  start: number;
  end: number;
  line: number;
  column: number;
  code: string;
  message: string;
  severity: "error";
}

export function diagnostic(file: string, source: string, start: number, code: string, message: string, end = start + 1): Diagnostic {
  start = Math.max(0, Math.min(start, source.length));
  end = Math.max(start, Math.min(end, source.length));
  const before = source.slice(0, start);
  const line = before.split("\n").length;
  const column = start - before.lastIndexOf("\n");
  return { file, start, end, line, column, code, message, severity: "error" };
}

export class CompilationError extends Error {
  diagnostics: Diagnostic[];

  constructor(diagnostics: Diagnostic[]) {
    super(diagnostics.map(d => `${d.file}:${d.line}:${d.column} ${d.code}: ${d.message}`).join("\n"));
    this.diagnostics = diagnostics;
  }
}

export function stderrLogger() {
  const write = (message: string): void => { process.stderr.write(`${message}\n`); };
  const warned = new Set();
  return {
    hasWarned: false,
    info: write,
    warn(message: string) { this.hasWarned = true; write(message); },
    warnOnce(message: string) { if (!warned.has(message)) { warned.add(message); this.warn(message); } },
    error: write,
    clearScreen() {},
    hasErrorLogged() { return false; },
  };
}
