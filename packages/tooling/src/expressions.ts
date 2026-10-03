import ts from "typescript";
import { CompilationError, diagnostic } from "./diagnostics.js";

const binary = new Set([
  ts.SyntaxKind.PlusToken, ts.SyntaxKind.MinusToken, ts.SyntaxKind.AsteriskToken, ts.SyntaxKind.SlashToken,
  ts.SyntaxKind.PercentToken, ts.SyntaxKind.AsteriskAsteriskToken, ts.SyntaxKind.LessThanToken,
  ts.SyntaxKind.LessThanEqualsToken, ts.SyntaxKind.GreaterThanToken, ts.SyntaxKind.GreaterThanEqualsToken,
  ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken,
  ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken, ts.SyntaxKind.AmpersandAmpersandToken,
  ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken, ts.SyntaxKind.InKeyword,
]);
const unary = new Set([ts.SyntaxKind.PlusToken, ts.SyntaxKind.MinusToken, ts.SyntaxKind.ExclamationToken, ts.SyntaxKind.TildeToken]);

export interface ExpressionOptions {
  file: string;
  source: string;
  start: number;
  locals?: Map<string, "plain" | "signal">;
  runtime?: boolean;
  event?: boolean;
}

interface Edit {
  start: number;
  end: number;
  text: string;
}

export function expression(raw: string, { file, source, start, locals = new Map<string, "plain" | "signal">(), runtime = false, event = false }: ExpressionOptions): { code: string; offsets: number[] } {
  const prefix = "const __expression = (";
  const ast = ts.createSourceFile("expression.ts", `${prefix}${raw}\n);`, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const fail = (message: string, offset = 0): never => { throw new CompilationError([diagnostic(file, source, start + offset, "F_EXPR", message)]); };
  const parseDiagnostics = (ast as ts.SourceFile & { parseDiagnostics: readonly ts.Diagnostic[] }).parseDiagnostics;
  if (parseDiagnostics.length) fail(ts.flattenDiagnosticMessageText(parseDiagnostics[0].messageText, "\n"), Math.max(0, parseDiagnostics[0].start! - prefix.length));
  const statement = ast.statements.find(ts.isVariableStatement);
  if (!statement || ast.statements.length !== 1) return fail("Expected one expression.");
  const declaration = statement.declarationList.declarations[0];
  if (!declaration.initializer || !ts.isParenthesizedExpression(declaration.initializer)) return fail("Expected one expression.");
  const initializer = declaration.initializer;
  const edits: Edit[] = [];
  const add = (node: ts.Node, text: string): void => { edits.push({ start: node.getStart(ast) - prefix.length, end: node.end - prefix.length, text }); };
  const qualify = (name: string): string => {
    if (name === "undefined" || name === "NaN" || name === "Infinity" || (name === "$event" && event)) return name;
    if (locals.has(name)) return runtime && locals.get(name) === "signal" ? `${name}()` : name;
    return `ctx.${name}`;
  };
  function visit(node: ts.Node): void {
    if (ts.isIdentifier(node)) { add(node, qualify(node.text)); return; }
    if (ts.isStringLiteral(node) || ts.isNumericLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) ||
        [ts.SyntaxKind.TrueKeyword, ts.SyntaxKind.FalseKeyword, ts.SyntaxKind.NullKeyword].includes(node.kind)) return;
    if (ts.isPropertyAccessExpression(node)) { visit(node.expression); return; }
    if (ts.isElementAccessExpression(node)) { visit(node.expression); visit(node.argumentExpression); return; }
    if (ts.isCallExpression(node)) {
      if (node.typeArguments?.length) fail("Type arguments are not supported in template expressions.", node.getStart(ast) - prefix.length);
      visit(node.expression); node.arguments.forEach(visit); return;
    }
    if (ts.isParenthesizedExpression(node)) { visit(node.expression); return; }
    if (ts.isConditionalExpression(node)) { visit(node.condition); visit(node.whenTrue); visit(node.whenFalse); return; }
    if (ts.isBinaryExpression(node) && binary.has(node.operatorToken.kind)) { visit(node.left); visit(node.right); return; }
    if (ts.isPrefixUnaryExpression(node) && unary.has(node.operator)) { visit(node.operand); return; }
    if (ts.isTypeOfExpression(node)) { visit(node.expression); return; }
    if (ts.isArrayLiteralExpression(node)) { node.elements.forEach(visit); return; }
    if (ts.isObjectLiteralExpression(node)) {
      for (const property of node.properties) {
        if (ts.isShorthandPropertyAssignment(property) && !property.objectAssignmentInitializer) {
          add(property, `${property.name.text}: ${qualify(property.name.text)}`);
        } else if (ts.isPropertyAssignment(property) && !ts.isComputedPropertyName(property.name)) visit(property.initializer);
        else fail("Object expressions support explicit properties and shorthand only.", property.getStart(ast) - prefix.length);
      }
      return;
    }
    fail(`Unsupported template expression: ${ts.SyntaxKind[node.kind]}. Use a public component method for complex logic.`, Math.max(0, node.getStart(ast) - prefix.length));
  }
  visit(initializer.expression);
  edits.sort((a, b) => a.start - b.start);
  let code = "";
  const offsets = [];
  let previous = 0;
  const appendOriginal = (from: number, to: number): void => {
    for (let i = from; i < to; i++) { offsets.push(start + i); code += raw[i]; }
  };
  for (const edit of edits) {
    appendOriginal(previous, edit.start);
    for (let i = 0; i < edit.text.length; i++) {
      const prefixLength = edit.text.startsWith("ctx.") ? 4 : 0;
      offsets.push(start + edit.start + Math.min(Math.max(0, i - prefixLength), edit.end - edit.start - 1));
      code += edit.text[i];
    }
    previous = edit.end;
  }
  appendOriginal(previous, raw.length);
  return { code, offsets };
}
