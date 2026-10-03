import ts from "typescript";
import { readFile } from "node:fs/promises";
import { dirname, resolve, extname } from "node:path";
import { diagnostic, CompilationError } from "./diagnostics.js";

interface ImportBinding {
  from: string;
  exported: string;
}

export interface ComponentDependency extends ImportBinding {
  local: string;
}

export interface ComponentMetadata {
  file: string;
  source: string;
  ast: ts.SourceFile;
  name: string;
  imports: Map<string, ImportBinding>;
  dependencies: ComponentDependency[];
  customElements: string[];
  decoratorStart: number;
  decoratorEnd: number;
  selector: string;
  templateUrl: string;
  styleUrl?: string;
}

export interface ExportedComponent {
  file: string;
  name: string;
  selector: string;
}

export async function metadata(file: string): Promise<ComponentMetadata | null> {
  const source = await readFile(file, "utf8");
  const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const errors = [];
  const error = (node: ts.Node, message: string): void => { errors.push(diagnostic(file, source, node.getStart(ast), "F_META", message, node.end)); };
  const imports = new Map<string, ImportBinding>();
  const decorators = new Set<string>();
  const namespaces = new Set<string>();
  for (const statement of ast.statements) {
    if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
    const from = statement.moduleSpecifier.text;
    const clause = statement.importClause;
    if (clause?.name) imports.set(clause.name.text, { from, exported: "default" });
    if (from === "@angulus/core" && clause?.namedBindings && ts.isNamespaceImport(clause.namedBindings)) namespaces.add(clause.namedBindings.name.text);
    if (clause?.namedBindings && ts.isNamedImports(clause.namedBindings)) {
      for (const item of clause.namedBindings.elements) {
        const exported = (item.propertyName ?? item.name).text;
        imports.set(item.name.text, { from, exported });
        if (from === "@angulus/core" && exported === "Component") decorators.add(item.name.text);
      }
    }
  }
  const components: ComponentMetadata[] = [];
  for (const statement of ast.statements) {
    if (!ts.isClassDeclaration(statement)) continue;
    for (const decorator of ts.getDecorators(statement) ?? []) {
      const call = decorator.expression;
      if (!ts.isCallExpression(call) || !ts.isIdentifier(call.expression) || !decorators.has(call.expression.text)) {
        const target = ts.isCallExpression(call) ? call.expression : call;
        if (ts.isPropertyAccessExpression(target) && ts.isIdentifier(target.expression) && namespaces.has(target.expression.text) && target.name.text === "Component") {
          error(decorator, "Import Component as a named import from @angulus/core.");
        } else if (ts.isIdentifier(target) && decorators.has(target.text)) error(decorator, "@Component requires a metadata object literal.");
        continue;
      }
      if (!statement.name || !statement.modifiers?.some(m => m.kind === ts.SyntaxKind.ExportKeyword) ||
          statement.modifiers?.some(m => m.kind === ts.SyntaxKind.DefaultKeyword)) {
        error(statement, "A component must be a named exported class (default exports are not supported).");
        continue;
      }
      for (const member of statement.members) {
        if (!ts.isPropertyDeclaration(member) || !member.initializer || !ts.isCallExpression(member.initializer)) continue;
        const target = member.initializer.expression;
        const identifier = ts.isPropertyAccessExpression(target) ? target.expression : target;
        const imported = ts.isIdentifier(identifier) ? imports.get(identifier.text) : undefined;
        if (imported?.from === "@angulus/core" && ["input", "output"].includes(imported.exported) &&
            (ts.isPrivateIdentifier(member.name) || member.modifiers?.some(modifier => modifier.kind === ts.SyntaxKind.PrivateKeyword || modifier.kind === ts.SyntaxKind.ProtectedKeyword))) {
          error(member, "Component inputs and outputs must be public.");
        }
      }
      if (call.arguments.length !== 1 || !ts.isObjectLiteralExpression(call.arguments[0])) {
        error(decorator, "@Component expects one object literal; metadata is never executed.");
        continue;
      }
      const data: ComponentMetadata = { file, source, ast, name: statement.name.text, imports, dependencies: [], customElements: [], selector: "", templateUrl: "", decoratorStart: decorator.getStart(ast), decoratorEnd: decorator.end };
      const seen = new Set();
      for (const property of call.arguments[0].properties) {
        if (!ts.isPropertyAssignment(property) || !property.name || (!ts.isIdentifier(property.name) && !ts.isStringLiteral(property.name))) {
          error(property, "Component metadata supports only explicit, non-computed properties.");
          continue;
        }
        const key = property.name.text;
        if (seen.has(key)) error(property, `Duplicate metadata property '${key}'.`);
        seen.add(key);
        const value = property.initializer;
        if (["selector", "templateUrl", "styleUrl"].includes(key)) {
          if (!ts.isStringLiteral(value)) error(value, `${key} must be a string literal.`);
          else data[key as "selector" | "templateUrl" | "styleUrl"] = value.text;
        } else if (key === "imports") {
          if (!ts.isArrayLiteralExpression(value)) error(value, "imports must be an array of named imported component references.");
          else for (const item of value.elements) {
            const binding = ts.isIdentifier(item) ? imports.get(item.text) : undefined;
            if (!binding || binding.exported === "default") {
              error(item, "Each component dependency must be a named import.");
            } else if (ts.isIdentifier(item)) data.dependencies.push({ local: item.text, ...binding });
          }
        } else if (key === "customElements") {
          if (!ts.isArrayLiteralExpression(value) || value.elements.some(item => !ts.isStringLiteral(item) || !item.text.includes("-"))) {
            error(value, "customElements must be an array of literal custom-element names.");
          } else data.customElements = value.elements.map(item => (item as ts.StringLiteral).text);
        } else error(property, `Unsupported component metadata '${key}'.`);
      }
      if (!data.selector || !/^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$/.test(data.selector)) error(decorator, "selector must be a lowercase hyphenated element name.");
      if (!data.templateUrl) error(decorator, "templateUrl is required.");
      if (data.templateUrl && !data.templateUrl.startsWith(".")) error(decorator, "templateUrl must be relative to the component.");
      if (data.styleUrl && !data.styleUrl.startsWith(".")) error(decorator, "styleUrl must be relative to the component.");
      components.push(data);
    }
  }
  if (components.length > 1) errors.push(diagnostic(file, source, 0, "F_META", "Only one component per TypeScript module is supported."));
  if (errors.length) throw new CompilationError(errors);
  return components[0] ?? null;
}

export function resolveImport(from: string, specifier: string, options: ts.CompilerOptions = {}): string {
  const result = ts.resolveModuleName(specifier, from, {
    moduleResolution: ts.ModuleResolutionKind.Bundler,
    module: ts.ModuleKind.ESNext,
    ...options,
  }, ts.sys).resolvedModule;
  if (result) return result.resolvedFileName;
  const candidate = resolve(dirname(from), specifier);
  if (specifier.startsWith(".") && !extname(candidate) && ts.sys.fileExists(`${candidate}.ts`)) return `${candidate}.ts`;
  throw new Error(`Cannot resolve '${specifier}' imported by ${from}`);
}

// Published declarations carry compiler metadata, never executable decorators.
export async function exportedComponent(
  file: string,
  name: string,
  options: ts.CompilerOptions = {},
  visited = new Set<string>(),
  files = new Set<string>(),
): Promise<ExportedComponent | null> {
  files.add(file);
  const key = `${file}:${name}`;
  if (visited.has(key)) return null;
  visited = new Set(visited).add(key);
  const source = await readFile(file, "utf8");
  const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const fail = (message: string): never => { throw new CompilationError([diagnostic(file, source, 0, "F_LIBRARY", message)]); };
  if (/\.d\.[cm]?ts$/.test(file)) {
    let text;
    try { text = await readFile(`${file}.angulus.json`, "utf8"); }
    catch (error) {
      if (!(error instanceof Error) || !("code" in error) || error.code !== "ENOENT") throw error;
    }
    if (text !== undefined) {
      files.add(`${file}.angulus.json`);
      let data: unknown;
      try { data = JSON.parse(text); }
      catch { fail(`Invalid Angulus metadata: ${file}.angulus.json`); }
      if (!data || typeof data !== "object") fail(`Invalid Angulus metadata: ${file}.angulus.json`);
      const libraryData = data as { version?: unknown; name?: unknown; selector?: unknown };
      if (libraryData.version !== 1 || typeof libraryData.name !== "string" ||
          typeof libraryData.selector !== "string" || !/^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$/.test(libraryData.selector)) {
        fail(`Unsupported or invalid Angulus library metadata: ${file}.angulus.json`);
      }
      if (libraryData.name === name && ast.statements.some(statement =>
        ts.isClassDeclaration(statement) && statement.name?.text === name &&
        statement.modifiers?.some(modifier => modifier.kind === ts.SyntaxKind.ExportKeyword))) {
        return { file, name, selector: libraryData.selector as string };
      }
    }
  } else {
    const component = await metadata(file);
    if (component?.name === name) return component;
  }
  for (const statement of ast.statements) {
    if (!ts.isExportDeclaration(statement) || statement.isTypeOnly) continue;
    const specifier = statement.moduleSpecifier;
    const from = specifier && ts.isStringLiteral(specifier) ? resolveImport(file, specifier.text, options) : file;
    if (statement.exportClause && ts.isNamedExports(statement.exportClause)) {
      const item = statement.exportClause.elements.find(item => !item.isTypeOnly && item.name.text === name);
      if (!item) continue;
      const original = (item.propertyName ?? item.name).text;
      if (from !== file) return exportedComponent(from, original, options, visited, files);
      // export { ImportedComponent as PublicName };
      for (const declaration of ast.statements) {
        if (!ts.isImportDeclaration(declaration) || !ts.isStringLiteral(declaration.moduleSpecifier)) continue;
        const bindings = declaration.importClause?.namedBindings;
        if (declaration.importClause?.isTypeOnly || !bindings || !ts.isNamedImports(bindings)) continue;
        const imported = bindings.elements.find(item => !item.isTypeOnly && item.name.text === original);
        if (imported) return exportedComponent(resolveImport(file, declaration.moduleSpecifier.text, options),
          (imported.propertyName ?? imported.name).text, options, visited, files);
      }
      if (original !== name) return exportedComponent(file, original, options, visited, files);
    }
  }
  const matches: ExportedComponent[] = [];
  for (const statement of ast.statements) {
    if (!ts.isExportDeclaration(statement) || statement.isTypeOnly || statement.exportClause ||
        !statement.moduleSpecifier || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
    const match = await exportedComponent(resolveImport(file, statement.moduleSpecifier.text, options), name, options, visited, files);
    if (match && !matches.some(item => item.file === match.file && item.name === match.name)) matches.push(match);
  }
  if (matches.length > 1) fail(`Ambiguous component export '${name}'. Use an explicit named re-export.`);
  return matches[0] ?? null;
}
