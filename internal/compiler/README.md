# Go template compiler

Run `go run ./cmd/angulus-compiler` from the repository root. Standard input/output
carry one JSON record per line; stdout never carries logging. Protocol version is
`1`, request IDs are integers, and responses may arrive out of order.

- Parse: `{version:1,id,method:"parse",source,file}` returns
  `{version:1,id,result:{nodes,diagnostics}}`.
- Generate: `{version:1,id,method:"generate",nodes,scopeId,components,file?}` returns
  `{version:1,id,result:{code,diagnostics,mappings?}}`. `components` maps selectors to
  imported JavaScript constructor identifiers.
- Cancel: `{version:1,id,method:"cancel",targetId}` returns
  `{version:1,id,result:{cancelled:boolean}}`. An in-flight cancelled request
  receives `error:{code:"CANCELLED",message}`; completed requests are unaffected.
- Shutdown: `{version:1,id,method:"shutdown"}` cancels outstanding requests,
  waits for their termination, acknowledges `{shutdown:true}`, and exits.
  EOF drains pending work. Invalid protocol requests return `error:{code,message}`.

Source diagnostics have `{file,start,end,code,message,severity:"error"}`.
Ranges are zero-based, end-exclusive UTF-16 offsets, matching JavaScript strings.
Protocol generation diagnostics use the optional request `file`; standalone
Go `Generate` diagnostics have an empty file for the caller to populate.
Invalid generation returns an empty code string, not partially valid code.
The transport accepts records up to 16 MiB and nesting up to 256 levels.

`mappings` is an optional array of `{generated:number,source:number}` anchors.
Both offsets are zero-based UTF-16: `generated` is relative to the returned
factory `code`, and `source` is relative to the original template. Anchors are
sorted by generated offset and cover node starts, binding/static attribute
values, and expression starts (including collection/track/case expressions).
Repeated emission of an expression, such as a two-way binding, has an anchor at
each occurrence. The frontend preserves AST source offsets while qualifying
expressions, then rebases generated offsets when embedding the factory in its
module and combines the anchors with its TypeScript source map. These are
node/expression anchors, not character-by-character maps of rewritten
expressions. Empty or invalid output has no mappings.

## AST

Nodes contain `kind,start,end` and optional fields:

```
text:          text
interpolation: expression,exprStart
element:       tag,attributes,children
if:            expression,exprStart,children,otherwise
for:           item,expression,exprStart,track,trackStart,children
switch:        expression,exprStart,cases
```

Attributes are `{name,value,start,valueStart}`; names preserve `[property]`,
`(event)`, and `[(value)]` delimiters. Static HTML entities are decoded.
Cases are `{expression,exprStart,children}`; the default case has `expression:""`.
Optional empty fields may be omitted. Text preserves whitespace.

The handwritten parser handles nested elements/control flow and nested
expression delimiters, strings, and comments. Expressions are retained as
source, not evaluated or interpreted as TypeScript. The frontend validates its
supported expression grammar and resolves names before calling generate.

The generated expression is `(ctx,host,scope)=>{...}` and references the
externally supplied `__f` runtime namespace. Loop render bindings (`item` and
`$index`) are signals; the frontend qualifies reads accordingly. Track callbacks
receive plain item/index values. `ctx`, `__f`, `$index`, `arguments`, and `eval`
are reserved loop-item identifiers. Internal DOM/scope variables are unique.

Supported template syntax: interpolation, property/event bindings, text-input
two-way bindings, `@if/@else if/@else`, tracked `@for`, and `@switch` with a final
optional `@default`. Children on components (other than whitespace) are rejected:
content projection is not implemented. Two-way bindings accept only `[(value)]`
on `<input>` with omitted type or static `type="text"`.

Raw HTML properties, inline `on*` attributes, active-content elements, and
executable static URLs are rejected. Interpolation uses text nodes.
Dynamic DOM URL safety is a runtime responsibility. The frontend is responsible
for expression type checking, component contracts, unknown custom-element
diagnostics, and resolving imports.

Run `go test ./internal/compiler ./cmd/angulus-compiler` (and `-race`) to cover
parsing, malformed input, UTF-16 spans, executable generated JavaScript, and the
persistent protocol.
