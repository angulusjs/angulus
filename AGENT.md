Below is a ready-to-use prompt. It captures the agreed decisions and separates the first working version from later evolution so development does not end up as a pile of stubs.

```text
You are a lead engineer in compilers and frontend infrastructure.

Build Angulus: a lightweight SPA framework with Angular-like syntax, class-based components, separate HTML/CSS files, a Go compiler, and Vite integration.

Primary scenario: applications are mostly developed with AI assistance. Therefore, the top priorities are predictability, strict typing, explicit contracts, high-quality diagnostics, and a short feedback loop for changes.

A working implementation is required, not just design docs, pseudocode, or folder structure.

# 1. Workflow

First, study the current workspace, existing instructions, code, and available tools.

Do not overwrite existing files and do not refactor an unrelated application into this framework. If the workspace is occupied by an unrelated project, place Angulus in a separate subdirectory. If there is a location conflict, ask for clarification.

Record a short plan, then proceed with implementation.

Work in vertical stages:
1. A minimal component passes through the full path from source to browser.
2. Reactivity, events, and lifecycle.
3. Control-flow constructs and component composition.
4. Type checking and diagnostics.
5. CLI, HMR, and production build.
6. Minimal router and SPA scenario validation.

Validate each stage. Do not create dozens of empty packages, stubs, and disconnected APIs just for the appearance of completeness.

# 2. Fixed architectural decisions

Do not replace these decisions without agreement:

- Components are described with TypeScript classes.
- Metadata is defined via @Component.
- TypeScript, HTML, and CSS are in separate files.
- No custom .angulus format.
- The component compiler is written in Go.
- Vite remains the dev server and build infrastructure.
- Integration is done through a custom Vite plugin.
- Type checking uses a native TypeScript implementation in Go.
- Browser runtime uses signals and fine-grained DOM updates.
- No virtual DOM, runtime template compilation, zone.js, or global change detection.
- Node.js is allowed and expected for Vite and CLI.
- A single physical binary is not required: users need a single command.
- Do not build your own bundler and do not replace Vite with direct esbuild bundling.

# 3. Public component model

Expected API:

counter/
  counter.ts
  counter.html
  counter.css
  counter.test.ts

counter.ts:

import { Component, signal, computed } from "@angulus/core";

@Component({
  selector: "app-counter",
  templateUrl: "./counter.html",
  styleUrl: "./counter.css",
})
export class CounterComponent {
  readonly count = signal(0);
  readonly reachedLimit = computed(() => this.count() >= 10);

  increment(): void {
    this.count.update(value => value + 1);
  }
}

counter.html:

<section class="counter">
  <span>Value: {{ count() }}</span>

  <button (click)="increment()">
    Increment
  </button>

  @if (reachedLimit()) {
    <p>Reached ten</p>
  }
</section>

counter.css:

.counter {
  display: flex;
  align-items: center;
  gap: 12px;
}

Requirements:

- One class instance per component instance.
- Signals initialized in fields are not shared across instances.
- No setup(), defineComponent(), or manual return of fields to templates.
- No mandatory extends BaseComponent.
- The template only has access to public members of the instance.
- Ordinary fields do not become reactive automatically.
- Event methods are called with correct this.
- @Component is processed at compile time, without runtime reflection.
- templateUrl/styleUrl are resolved relative to the component file.
- Source HTML files are not fetched by the browser at runtime.
- Metadata is analyzed statically without executing user code.
- In the first version, allow a limited, documented metadata form: literals and explicitly supported references.

# 4. Template syntax

Support:

- {{ expression }} - text interpolation.
- [property]="expression" - DOM property or component input binding.
- (event)="handler($event)" - DOM event or component output handler.
- [(value)]="signal" - two-way binding for text inputs.
- @if / @else.
- @for (item of items(); track item.id).
- @switch / @case / @default.

Do not pretend [(value)] automatically solves form semantics for all elements. Checkbox, select, and other cases need separate, well-thought-out rules; unsupported variants must be diagnosed.

For @for:
- track is required;
- keyed reconciliation is used;
- reordering preserves corresponding component instances;
- item removal releases resources;
- duplicate keys are diagnosed explicitly.

Do not allow arbitrary statement execution in regular template expressions. Define and document the allowed grammar.

Interpolations create text, not unsafe HTML.
Do not add a raw HTML API in v1.

# 5. Component composition

Required:

- Explicit imports of used components.
- Local dependency declaration via imports in @Component.
- Typed inputs.
- Typed outputs.
- Required input checks.
- $event type checks.

Target API:

readonly product = input.required<Product>();
readonly add = output<{ productId: string }>();

Usage:

<product-card
  [product]="product"
  (add)="cart.add($event.productId)"
/>

Do not create a global registry of auto-discovered components.

An unknown component must produce an error, not silently become an HTML tag. Provide explicit support for real custom elements.

Define initialization order: required inputs are available before first render and onMount. Accessing a not-yet-set required input must not return silent undefined.

# 6. Reactivity and lifecycle

Implement:

- signal;
- computed;
- controlled effect;
- update scheduler;
- scope-based resource disposal.

A component updates only dependent DOM parts.

Cover:
- dynamic dependency changes;
- batched updates;
- no subscriptions from destroyed components;
- cleanup on @if branch changes;
- cleanup on @for item removal;
- cleanup on route changes.

Minimal lifecycle hooks:
- onMount;
- onDestroy.

Define call order and cleanup registration mechanism. Do not port the full Angular lifecycle.

# 7. Go compiler

Pipeline:

TypeScript + HTML + CSS
  -> parsing and static metadata
  -> template AST
  -> name/component resolution
  -> intermediate representation
  -> executable code + CSS + source maps
  -> type-check TypeScript representation

Separate parser, semantic analysis, and code generation.

Do not use regex as the primary parser for nested templates or TypeScript.

For TypeScript, use an available compatible parser or a justified limited frontend. First verify real dependency APIs and licenses. Do not write your own full TypeScript grammar implementation.

It is acceptable to preserve user TypeScript for later Vite processing if the compiler correctly extracts metadata and applies required transformations.

Source maps must map back to original TS/HTML/CSS.

Styles are local by default. Use a proper CSS parser/transformer. Do not break selectors and at-rules via string replacements.

Define boundaries of local styles between parent and child components. Global CSS is connected explicitly.

# 8. Native TypeScript

Before implementing the adapter, verify current:
- native TypeScript package and command;
- supported versions and platforms;
- diagnostics interfaces;
- watch/incremental capabilities;
- public API availability.

Do not assume TypeScript internal Go packages can be imported by third-party projects.

Preference for v1:
- pinned native TypeScript version;
- separate process;
- own adapter;
- no TypeScript fork.

If required interfaces are missing, explicitly explain the limitation and propose a working alternative. Do not invent APIs and do not replace type checking with transpilation.

Generate type-check TS for:
- template expressions;
- inputs/outputs;
- DOM events;
- loop scopes;
- type narrowing in conditions;
- class member accessibility checks.

Design import resolution, component contracts, and mapping between generated and source files.

Map errors back to source HTML/TS positions.

Errors in unopened lazy routes must also be found by full checks and production builds.

# 9. Vite integration

The Vite plugin launches a long-running Go process.

Communication:
- structured protocol via stdin/stdout;
- request IDs;
- protocol version;
- stdout for protocol only;
- stderr for logs;
- cancellation, crash, and shutdown handling;
- no orphaned processes after Vite stops.

Do not start a new compiler for every changed file.

Compiler returns required:
- modules;
- CSS;
- source maps;
- dependencies;
- diagnostics.

Plugin registers TS -> HTML/CSS dependencies.
Do not duplicate Vite graph responsibilities unnecessarily.

HMR:
- CSS updates without state reset;
- template updates preserve state only where correct;
- logic changes may trigger controlled remount;
- if safe HMR is impossible, do explicit full reload;
- old effects and handlers are disposed.

Do not promise state preservation for every change.

# 10. CLI

Unified commands:

angulus serve
angulus check
angulus build
angulus preview
angulus test
angulus generate component <name>

Behavior:

serve:
- Vite dev server;
- watch and HMR;
- diagnostics in terminal and browser;
- configurable API proxy;
- localhost by default;
- clear error on occupied port.

check:
- full code and template checks;
- non-zero exit code on errors;
- --json with documented diagnostics format.

build:
- mandatory full check;
- production build via Vite;
- non-zero exit code on errors;
- no successful build message when checks fail.

preview:
- local validation of built artifacts;
- not positioned as a production server.

test:
- runs configured application tests, not just framework tests.

generate:
- creates TS/HTML/CSS and test from working template;
- does not overwrite existing files without explicit permission.

For serve --json, provide an event stream with revision/build ID. Distinguish HTTP server readiness from successful validation of the latest project revision.

A regular project should work without manual Vite config. For advanced usage, provide an exported Vite plugin.

# 11. Minimal router and SPA

Implement:
- explicit route definitions;
- lazy loading via import();
- route params;
- navigation without full reload;
- back/forward;
- not-found;
- component tree disposal on route changes.

Validate direct opening of nested URLs.

SPA fallback applies to navigation:
- missing JS/CSS must not return HTML with status 200;
- /api must not fall into HTML fallback.

Route load errors must be visible and have a clear recovery path.

If client guards are added, they do not replace server authorization.

# 12. Scaling

In v1, establish:
- lazy routes;
- explicit public component contracts;
- incremental workflow;
- scoped cleanup;
- no mandatory global store.

Recommended app structure is by feature domains, not generic folders with hundreds of services and components.

Do not try to implement immediately:
- enterprise DI;
- complex forms system;
- query cache;
- micro-frontends;
- SSR/hydration;
- full DevTools;
- support for all Angular APIs.

Describe as next stages:
- nested layouts;
- route/application providers;
- forms and validation;
- query cache and server state;
- architecture boundary checks;
- multi-app workspace;
- inspect --json and check --affected;
- LSP;
- version migrations;
- localization.

Do not publish fake APIs for these capabilities.

# 13. Result validation

Add and run:

Go:
- parser tests;
- invalid template tests;
- diagnostic position tests;
- code generation tests;
- protocol tests.

Runtime:
- instance isolation;
- signals/computed/effects;
- DOM updates;
- correct this;
- disposal;
- keyed lists;
- inputs/outputs.

Integration:
- start serve and make HTTP request;
- browser counter;
- CSS and HTML hot updates via HMR;
- template type error with correct source file;
- error in unopened route;
- failed production build on errors;
- successful build and preview run;
- SPA navigation and direct nested URL;
- child compiler stopped with server.

Validate actual browser behavior, not only generated code snapshots.

Create a small demo SPA using only truly working public APIs.

Measure minimal runtime size after build and gzip. Target is up to 10 KiB gzip without router, app code, and dev code. This is a budget target, not a reason to hide real size or sacrifice correctness.

# 14. Documentation and completion

Add:
- quick start;
- component model description;
- template syntax;
- reactivity and lifecycle rules;
- CLI commands;
- compiler architecture;
- known limitations;
- reproducible build instructions.

Pin dependency versions and add required lockfiles.

In the final report include:
1. What is actually implemented.
2. How to run the example with one command after installing dependencies.
3. What checks were run and their results.
4. Measured runtime size.
5. What is not implemented or is limited.

Do not claim production readiness without sufficient validation.
Do not call the task complete if only a skeleton exists.
If blocked by infrastructure, state the exact reason and preserve a reproducible working state.

Start by exploring the workspace, then implement the first vertical slice: class + external HTML/CSS -> Go compiler -> Vite -> working browser counter.

