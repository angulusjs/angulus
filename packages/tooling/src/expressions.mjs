import ts from "typescript";
import { CompilationError, diagnostic } from "./diagnostics.mjs";

const binary = new Set([
  ts.SyntaxKind.PlusToken, ts.SyntaxKind.MinusToken, ts.SyntaxKind.AsteriskToken, ts.SyntaxKind.SlashToken,
  ts.SyntaxKind.PercentToken, ts.SyntaxKind.AsteriskAsteriskToken, ts.SyntaxKind.LessThanToken,
  ts.SyntaxKind.LessThanEqualsToken, ts.SyntaxKind.GreaterThanToken, ts.SyntaxKind.GreaterThanEqualsToken,
  ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken,
  ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken, ts.SyntaxKind.AmpersandAmpersandToken,
  ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken, ts.SyntaxKind.InKeyword,
]);
const unary = new Set([ts.SyntaxKind.PlusToken, ts.SyntaxKind.MinusToken, ts.SyntaxKind.ExclamationToken, ts.SyntaxKind.TildeToken]);

export function expression(raw, { file, source, start, locals = new Map(), runtime = false, event = false }) {
  const prefix = "const __expression = (";
  const ast = ts.createSourceFile("expression.ts", `${prefix}${raw}\n);`, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const fail = (message, offset = 0) => { throw new CompilationError([diagnostic(file, source, start + offset, "F_EXPR", message)]); };
  if (ast.parseDiagnostics.length) fail(ts.flattenDiagnosticMessageText(ast.parseDiagnostics[0].messageText, "\n"), Math.max(0, ast.parseDiagnostics[0].start - prefix.length));
  if (ast.statements.length !== 1 || !ts.isVariableStatement(ast.statements[0])) fail("Expected one expression.");
  const declaration = ast.statements[0].declarationList.declarations[0];
  if (!declaration.initializer || !ts.isParenthesizedExpression(declaration.initializer)) fail("Expected one expression.");
  const edits = [];
  const add = (node, text) => edits.push({ start: node.getStart(ast) - prefix.length, end: node.end - prefix.length, text });
  const qualify = name => {
    if (name === "undefined" || name === "NaN" || name === "Infinity" || (name === "$event" && event)) return name;
    if (locals.has(name)) return runtime && locals.get(name) === "signal" ? `${name}()` : name;
    return `ctx.${name}`;
  };
  function visit(node) {
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
  visit(declaration.initializer.expression);
  edits.sort((a, b) => a.start - b.start);
  let code = "";
  const offsets = [];
  let previous = 0;
  const appendOriginal = (from, to) => {
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
