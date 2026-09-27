# @angulus/forms

Typed, signal-based forms for Angulus. Controls and nested groups use the
existing core reactivity and template bindings; no directives or compiler
extensions are required.

```sh
npm install @angulus/core @angulus/forms
```

Use matching Angulus package versions. This package requires a release containing
forms support.

## Quick start

```ts
import { Component, signal } from "@angulus/core";
import { formControl, formGroup, Validators } from "@angulus/forms";

@Component({ selector: "app-profile", templateUrl: "./profile.html" })
export class Profile {
  readonly form = formGroup({
    name: formControl("", {
      validators: [Validators.required, Validators.minLength(2)],
    }),
    email: formControl("", {
      validators: [Validators.required, Validators.email],
    }),
  });
  readonly saved = signal("");

  submit(event: Event): void {
    event.preventDefault();
    this.form.markAsTouched();
    if (this.form.invalid()) return;
    this.saved.set(this.form.value().name);
  }
}
```

```html
<form [noValidate]="true" (submit)="submit($event)">
  <label>
    Name
    <input [(value)]="form.controls.name.value"
           (blur)="form.controls.name.markAsTouched()" />
  </label>
  @if (form.controls.name.touched() && form.controls.name.invalid()) {
    <p role="alert">Enter a name with at least two characters.</p>
  }
  <label>
    Email
    <input type="text" inputmode="email" [(value)]="form.controls.email.value"
           (blur)="form.controls.email.markAsTouched()" />
  </label>
  @if (form.controls.email.touched() && form.controls.email.invalid()) {
    <p role="alert">Enter a valid email address.</p>
  }
  <button type="submit">Save</button>
  <button type="button" (click)="form.reset()">Reset</button>
</form>
<p>{{ saved() }}</p>
```

The example only records a local result; persistence is application-owned.
`noValidate` disables native browser validation so the submit handler can show
the form's own errors. Server-side validation is still required for submitted data.

## Controls and state

`formControl(initial, { validators })` infers the value type. Specify a type
argument for nullable values, for example `formControl<string | null>(null)`.

| Member | Contract |
| --- | --- |
| `value()` | Current typed value |
| `value.set(next)` / `value.update(fn)` | Change the value, including from `[(value)]` |
| `errors()` | Error map, or `null` when all validators pass |
| `valid()` / `invalid()` | Synchronous validation status |
| `dirty()` / `pristine()` | Whether a different value has been written since reset or marking pristine |
| `touched()` / `untouched()` | Explicit interaction state, normally marked on blur or submit |
| `markAsDirty()` / `markAsPristine()` | Set or clear dirty state without changing the value |
| `markAsTouched()` / `markAsUntouched()` | Set or clear touched state |
| `reset()` | Restore the original initial value and clear dirty/touched |
| `reset(value)` | Set an explicit value and clear dirty/touched |

All state properties are read-only signals except a control's `value`. Both
programmatic writes and template writes mark a control dirty when the value
changes by `Object.is`. Returning to the initial value does not clear dirty.
Writing the same value does not mark dirty. Touch state is never inferred from
value changes.

Resetting to an explicit value does not replace the original reset baseline.
Object values are not cloned: publish new references rather than mutating objects
in place, and treat the initial value as immutable.

Validation is lazy and cached through `computed`, but current on every read.
DOM effects remain microtask-batched. Create forms during component construction
so their computed dependencies belong to the component's disposal scope.

## Groups and cross-field validation

```ts
const form = formGroup({
  password: formControl("", { validators: [Validators.required] }),
  confirmation: formControl(""),
  address: formGroup({ city: formControl("") }),
}, {
  validators: [
    value => value.password === value.confirmation ? null : { mismatch: true },
  ],
});

form.controls.password.value.set("secret");
form.controls.address.controls.city.value.set("London");
form.value(); // { password: string, confirmation: string, address: { city: string } }
form.errors(); // { mismatch: true }
form.markAsTouched(); // Marks every descendant touched.
form.reset(); // Resets every descendant to its original initial value.
```

Group membership is a frozen shallow copy of the supplied controls. Nested groups
retain their inferred value types. Update individual controls; the aggregate
`value` is a read-only signal, not a second writable copy of the form data.

A group is valid only when its own validators and every descendant pass.
`group.errors()` contains **only group-level errors**; inspect child controls for
their errors. A group is dirty or touched when any descendant is dirty or touched.
Group marking methods propagate to all descendants. An empty group is pristine,
untouched, and valid unless its own validators fail.

## Synchronous validators

A `ValidatorFn<T>` accepts the typed value and returns a `ValidationErrors` map
or `null`. All validators run; their error maps are merged in order, with the last
validator winning for duplicate keys. Empty maps mean success. Exceptions
propagate instead of being converted into successful validation. Validator lists
are copied at creation; signals read inside a validator remain reactive.
Promises and malformed results such as `undefined` throw rather than silently
passing validation.

| Validator | Behavior |
| --- | --- |
| `Validators.required` | Rejects `null`, `undefined`, empty strings, and empty arrays; accepts `0`, `false`, and whitespace |
| `Validators.requiredTrue` | Accepts only `true`, for boolean consent fields |
| `Validators.minLength(n)` / `maxLength(n)` | String length bounds using JavaScript UTF-16 length |
| `Validators.min(n)` / `max(n)` | Inclusive numeric bounds; reject non-finite values |
| `Validators.pattern(regexp)` | Uses the supplied regular expression, including its flags; use anchors for full-string matching |
| `Validators.email` | Basic non-whitespace `local@domain.suffix` format check, not address verification |

String format/length validators accept empty strings so fields can be optional;
combine them with `required` when needed. Length bounds must be non-negative safe
integers and numeric bounds must be finite, otherwise construction throws.
Pattern validation does not mutate the caller's regex or alternate results with
global/sticky flags.

## Template integration and scope

Use `[(value)]="control.value"` for text inputs and
`(blur)="control.markAsTouched()"` to track interaction. Checkbox, select, and
numeric inputs use ordinary property/event bindings with typed component methods
that read or convert the DOM value before calling `control.value.set(...)`.
Two-way inputs must omit `type` or use `type="text"`; `inputmode="email"` can
provide an email keyboard while a validator checks the value. This package does
not extend the compiler's text-only two-way binding support.

This first version supports fixed control/group trees and synchronous validation.
Dynamic form arrays, async validators, disabled-control exclusion, automatic
submit/persistence state, and form directives are not implemented.

See the [working demo](../../examples/demo/src/forms/forms.ts) and its
[template](../../examples/demo/src/forms/forms.html).
