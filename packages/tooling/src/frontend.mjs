import ts from "typescript";
import { readFile } from "node:fs/promises";
import { dirname, resolve, extname } from "node:path";
import { diagnostic, CompilationError } from "./diagnostics.mjs";

export async function metadata(file) {
  const source = await readFile(file, "utf8");
  const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const errors = [];
  const error = (node, message) => errors.push(diagnostic(file, source, node.getStart(ast), "F_META", message, node.end));
  const imports = new Map();
  const decorators = new Set();
  const namespaces = new Set();
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
  const components = [];
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
      const data = { file, source, ast, name: statement.name.text, imports, dependencies: [], customElements: [], decoratorStart: decorator.getStart(ast), decoratorEnd: decorator.end };
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
          else data[key] = value.text;
        } else if (key === "imports") {
          if (!ts.isArrayLiteralExpression(value)) error(value, "imports must be an array of named imported component references.");
          else for (const item of value.elements) {
            if (!ts.isIdentifier(item) || !imports.has(item.text) || imports.get(item.text).exported === "default") {
              error(item, "Each component dependency must be a named import.");
            } else data.dependencies.push({ local: item.text, ...imports.get(item.text) });
          }
        } else if (key === "customElements") {
          if (!ts.isArrayLiteralExpression(value) || value.elements.some(item => !ts.isStringLiteral(item) || !item.text.includes("-"))) {
            error(value, "customElements must be an array of literal custom-element names.");
          } else data.customElements = value.elements.map(item => item.text);
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

export function resolveImport(from, specifier, options = {}) {
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
