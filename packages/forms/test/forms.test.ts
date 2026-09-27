import { test } from "node:test";
import assert from "node:assert/strict";
import { Scope, effect, flushSync, signal, type WritableSignal } from "@angulus/core";
import {
  formControl, formGroup, Validators,
  type FormControl, type FormValue, type ValidatorFn,
} from "../src/index.js";

test("controls expose writable signals and synchronous validation", () => {
  const name = formControl("", { validators: [Validators.required, Validators.minLength(3)] });
  assert.equal(name.value(), "");
  assert.deepEqual(name.errors(), { required: true });
  assert.equal(name.invalid(), true);
  assert.equal(name.pristine(), true);
  assert.equal(name.untouched(), true);
  name.value.set("ab");
  assert.deepEqual(name.errors(), { minLength: { requiredLength: 3, actualLength: 2 } });
  assert.equal(name.dirty(), true);
  assert.equal(name.touched(), false);
  name.value.update(value => value + "c");
  assert.equal(name.value(), "abc");
  assert.equal(name.errors(), null);
  assert.equal(name.valid(), true);
});

test("dirty is sticky after changed writes; touch and pristine are explicit", () => {
  const field = formControl("initial");
  field.value.set("initial");
  field.value.update(value => value);
  assert.equal(field.dirty(), false);
  field.value.set("changed");
  field.value.set("initial");
  assert.equal(field.dirty(), true);
  field.markAsPristine();
  assert.equal(field.pristine(), true);
  field.markAsDirty();
  assert.equal(field.dirty(), true);
  field.markAsTouched();
  assert.equal(field.touched(), true);
  assert.equal(field.untouched(), false);
  field.markAsUntouched();
  assert.equal(field.untouched(), true);
});

test("reset restores initial value and state, or accepts an explicit value", () => {
  const field = formControl<string | undefined>("initial");
  field.value.set("edited");
  field.markAsTouched();
  field.reset(undefined);
  assert.equal(field.value(), undefined);
  assert.equal(field.pristine(), true);
  assert.equal(field.untouched(), true);
  field.reset("replacement");
  assert.equal(field.value(), "replacement");
  field.reset();
  assert.equal(field.value(), "initial");
});

test("object values use signal identity semantics and instances are independent", () => {
  const original = { name: "initial" };
  const field = formControl(original);
  const other = formControl("");
  field.value.set(original);
  assert.equal(field.pristine(), true);
  field.value.set({ name: "new" });
  assert.equal(field.dirty(), true);
  assert.equal(other.value(), "");
  assert.equal(other.pristine(), true);
  field.reset();
  assert.equal(field.value(), original);
});

test("all validators run, merge errors, and exceptions propagate", () => {
  const field = formControl("bad", {
    validators: [() => ({ first: true, shared: 1 }), () => ({}), () => ({ second: true, shared: 2 })],
  });
  assert.deepEqual(field.errors(), { first: true, shared: 2, second: true });
  const failure = formControl("", {
    validators: [value => { if (!value) throw new Error("validator failed"); return null; }],
  });
  assert.throws(() => failure.valid(), /validator failed/);
  failure.value.set("fixed");
  assert.equal(failure.valid(), true);
});

test("validator configuration is copied and external signal dependencies stay reactive", () => {
  const minimum = signal(3);
  const validators: ValidatorFn<string>[] = [value => value.length < minimum() ? { short: true } : null];
  const field = formControl("ab", { validators });
  validators.length = 0;
  assert.deepEqual(field.errors(), { short: true });
  minimum.set(2);
  assert.equal(field.valid(), true);
});

test("unsupported async and malformed validator results fail explicitly", () => {
  // @ts-expect-error Async validation is also rejected for untyped JavaScript callers at runtime.
  const asyncField = formControl("", { validators: [async () => null] });
  assert.throws(() => asyncField.valid(), /synchronous form validator/);
  // @ts-expect-error Validators must return null rather than undefined for success.
  const missingResult = formControl("", { validators: [() => undefined] });
  assert.throws(() => missingResult.errors(), /error map or null/);
});

test("nested groups aggregate typed values, validity and interaction state", () => {
  const form = formGroup({
    name: formControl("", { validators: [Validators.required] }),
    address: formGroup({ city: formControl(""), zip: formControl(0) }),
  });
  assert.deepEqual(form.value(), { name: "", address: { city: "", zip: 0 } });
  assert.equal(form.errors(), null);
  assert.equal(form.invalid(), true);
  form.controls.name.value.set("Ada");
  form.controls.address.controls.city.value.set("London");
  form.controls.address.controls.zip.markAsTouched();
  assert.equal(form.valid(), true);
  assert.equal(form.dirty(), true);
  assert.equal(form.touched(), true);
  assert.deepEqual(form.value(), { name: "Ada", address: { city: "London", zip: 0 } });
  form.reset();
  assert.deepEqual(form.value(), { name: "", address: { city: "", zip: 0 } });
  assert.equal(form.pristine(), true);
  assert.equal(form.untouched(), true);
  assert.equal(form.invalid(), true);
});

test("group validators express cross-field rules independently of child errors", () => {
  const form = formGroup({
    password: formControl("", { validators: [Validators.required] }),
    confirmation: formControl(""),
  }, { validators: [value => value.password === value.confirmation ? null : { mismatch: true }] });
  assert.equal(form.errors(), null);
  assert.equal(form.invalid(), true);
  form.controls.password.value.set("secret");
  assert.deepEqual(form.errors(), { mismatch: true });
  form.controls.confirmation.value.set("secret");
  assert.equal(form.valid(), true);
  assert.equal(form.errors(), null);
});

test("group state operations propagate to every nested child", () => {
  const form = formGroup({ a: formControl(""), nested: formGroup({ b: formControl(0) }) });
  form.markAsTouched();
  form.markAsDirty();
  assert.equal(form.controls.a.touched(), true);
  assert.equal(form.controls.nested.controls.b.touched(), true);
  assert.equal(form.controls.nested.controls.b.dirty(), true);
  form.markAsUntouched();
  form.markAsPristine();
  assert.equal(form.untouched(), true);
  assert.equal(form.pristine(), true);
  form.controls.a.markAsDirty();
  form.controls.nested.controls.b.markAsDirty();
  assert.equal(form.dirty(), true);
  form.controls.a.markAsPristine();
  assert.equal(form.dirty(), true);
  form.controls.nested.controls.b.markAsPristine();
  assert.equal(form.dirty(), false);
});

test("empty groups are valid and group membership is fixed", () => {
  const empty = formGroup({});
  assert.deepEqual(empty.value(), {});
  assert.equal(empty.valid(), true);
  empty.markAsDirty();
  empty.markAsTouched();
  empty.reset();
  assert.equal(empty.pristine(), true);
  assert.equal(empty.untouched(), true);
  const controls = { name: formControl("original") };
  const form = formGroup(controls);
  controls.name = formControl("replacement");
  assert.equal(form.controls.name.value(), "original");
  assert.equal(Object.isFrozen(form.controls), true);
  const special = formGroup({ ["__proto__"]: formControl("safe") });
  assert.deepEqual(Object.keys(special.value()), ["__proto__"]);
  assert.equal(Object.getPrototypeOf(special.value()), Object.prototype);
});

test("forms participate in core batching and component scope disposal", () => {
  const scope = new Scope();
  let runs = 0;
  let snapshot = "";
  const form = scope.run(() => {
    const result = formGroup({ name: formControl("") });
    effect(() => {
      snapshot = `${result.value().name}:${result.dirty()}:${result.valid()}`;
      runs++;
    });
    return result;
  });
  assert.equal(runs, 1);
  form.controls.name.value.set("a");
  form.controls.name.value.set("b");
  assert.equal(runs, 1);
  flushSync();
  assert.equal(runs, 2);
  assert.equal(snapshot, "b:true:true");
  form.reset();
  flushSync();
  assert.equal(snapshot, ":false:true");
  scope.dispose();
  form.controls.name.value.set("disposed");
  flushSync();
  assert.equal(runs, 3);
});

test("required and requiredTrue distinguish empty values from false and zero", () => {
  for (const value of [null, undefined, "", []]) assert.deepEqual(Validators.required(value), { required: true });
  for (const value of [0, false, " ", [1], {}]) assert.equal(Validators.required(value), null);
  assert.deepEqual(Validators.requiredTrue(false), { requiredTrue: true });
  assert.equal(Validators.requiredTrue(true), null);
});

test("string validators allow optional empty strings and report error details", () => {
  for (const validator of [Validators.minLength(2), Validators.maxLength(2), Validators.email, Validators.pattern(/^[a-z]+$/)]) {
    assert.equal(validator(""), null);
  }
  assert.equal(Validators.minLength(2)("ab"), null);
  assert.equal(Validators.maxLength(2)("ab"), null);
  assert.deepEqual(Validators.maxLength(2)("abc"), { maxLength: { requiredLength: 2, actualLength: 3 } });
  assert.equal(Validators.email("a+b@example.com"), null);
  for (const value of ["abc", "a@@example.com", "a@b", "a b@example.com"]) {
    assert.deepEqual(Validators.email(value), { email: true });
  }
  assert.deepEqual(Validators.pattern(/^\d+$/)("abc"), {
    pattern: { requiredPattern: "/^\\d+$/", actualValue: "abc" },
  });
});

test("pattern validation is deterministic for global and sticky expressions", () => {
  for (const expression of [/a/g, /a/y]) {
    expression.lastIndex = 1;
    const validator = Validators.pattern(expression);
    assert.equal(validator("a"), null);
    assert.equal(validator("a"), null);
    assert.equal(expression.lastIndex, 1);
    assert.notEqual(validator("b"), null);
  }
});

test("numeric validators enforce bounds and reject non-finite values", () => {
  assert.equal(Validators.min(2)(2), null);
  assert.equal(Validators.max(2)(2), null);
  assert.deepEqual(Validators.min(2)(1), { min: { min: 2, actual: 1 } });
  assert.deepEqual(Validators.max(2)(3), { max: { max: 2, actual: 3 } });
  for (const value of [NaN, Infinity, -Infinity]) {
    assert.notEqual(Validators.min(0)(value), null);
    assert.notEqual(Validators.max(0)(value), null);
  }
  for (const limit of [-1, 1.5, NaN, Infinity]) {
    assert.throws(() => Validators.minLength(limit), RangeError);
    assert.throws(() => Validators.maxLength(limit), RangeError);
  }
  assert.throws(() => Validators.min(NaN), RangeError);
  assert.throws(() => Validators.max(Infinity), RangeError);
});

// Compiled by the workspace type check, but never called at runtime.
function typeContracts(): void {
  const field: FormControl<string> = formControl("");
  const writable: WritableSignal<string> = field.value;
  writable.set("name");
  // @ts-expect-error String controls reject numbers.
  field.value.set(1);
  // @ts-expect-error Reset preserves the value type.
  field.reset(false);
  // @ts-expect-error Validators must accept the control's value type.
  formControl("", { validators: [Validators.min(1)] });
  const form = formGroup({ name: field, nested: formGroup({ age: formControl(0) }) });
  const value: FormValue<typeof form.controls> = { name: "", nested: { age: 10 } };
  const same: typeof value = form.value();
  void same;
  // @ts-expect-error Nested values retain their types.
  const wrong: FormValue<typeof form.controls> = { name: "", nested: { age: "10" } };
  // @ts-expect-error Group values are read-only signals.
  form.value.set(value);
  // @ts-expect-error Group membership cannot be reassigned.
  form.controls.name = formControl("");
  // @ts-expect-error Cross-field validators see the actual group shape.
  formGroup({ name: field }, { validators: [value => value.missing ? null : { invalid: true }] });
  void wrong;
}
