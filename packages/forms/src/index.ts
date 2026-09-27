import { computed, signal, untracked, type Signal, type WritableSignal } from "@angulus/core";

export type ValidationErrors = Readonly<Record<string, unknown>>;
export type ValidatorFn<T> = (value: T) => ValidationErrors | null;
export interface FormOptions<T> {
  readonly validators?: readonly ValidatorFn<T>[];
}
export interface FormState<T> {
  readonly value: Signal<T>;
  readonly errors: Signal<ValidationErrors | null>;
  readonly valid: Signal<boolean>;
  readonly invalid: Signal<boolean>;
  readonly dirty: Signal<boolean>;
  readonly pristine: Signal<boolean>;
  readonly touched: Signal<boolean>;
  readonly untouched: Signal<boolean>;
  markAsDirty(): void;
  markAsPristine(): void;
  markAsTouched(): void;
  markAsUntouched(): void;
  reset(): void;
}
export interface FormControl<T> extends FormState<T> {
  readonly value: WritableSignal<T>;
  reset(...value: [] | [T]): void;
}
export type FormControls = Readonly<Record<string, FormState<unknown>>>;
export type FormValue<C extends FormControls> = {
  -readonly [K in keyof C]: ReturnType<C[K]["value"]>;
};
export interface FormGroup<C extends FormControls> extends FormState<FormValue<C>> {
  readonly controls: Readonly<C>;
}

function validate<T>(value: T, validators: readonly ValidatorFn<T>[]): ValidationErrors | null {
  const entries = validators.flatMap(validator => {
    const result = validator(value);
    if (result === null) return [];
    if (typeof result !== "object" || Array.isArray(result) || typeof result.then === "function") {
      throw new TypeError("A synchronous form validator must return an error map or null");
    }
    return Object.entries(result);
  });
  return entries.length ? Object.fromEntries(entries) : null;
}

function state<T>(
  value: Signal<T>,
  dirty: Signal<boolean>,
  touched: Signal<boolean>,
  validators: readonly ValidatorFn<T>[],
  childrenValid: () => boolean = () => true,
) {
  const errors = computed(() => validate(value(), validators));
  const valid = computed(() => errors() === null && childrenValid());
  return {
    value, errors, valid,
    invalid: computed(() => !valid()),
    dirty, pristine: computed(() => !dirty()),
    touched, untouched: computed(() => !touched()),
  };
}

export function formControl<T>(initial: T, options: FormOptions<NoInfer<T>> = {}): FormControl<T> {
  const current = signal(initial);
  const dirty = signal(false);
  const touched = signal(false);
  const value: WritableSignal<T> = Object.assign(() => current(), {
    set(next: T): void {
      if (!Object.is(untracked(current), next)) {
        current.set(next);
        dirty.set(true);
      }
    },
    update(update: (value: T) => T): void {
      value.set(update(untracked(current)));
    },
  });
  return {
    ...state(value, dirty, touched, [...(options.validators ?? [])]),
    value,
    markAsDirty: () => dirty.set(true),
    markAsPristine: () => dirty.set(false),
    markAsTouched: () => touched.set(true),
    markAsUntouched: () => touched.set(false),
    reset(...next: [] | [T]): void {
      current.set(next.length ? next[0] : initial);
      dirty.set(false);
      touched.set(false);
    },
  };
}

export function formGroup<C extends FormControls>(
  controls: C,
  options: FormOptions<NoInfer<FormValue<C>>> = {},
): FormGroup<C> {
  const members = Object.freeze({ ...controls });
  const children = Object.values(members);
  // Object.fromEntries loses the relationship between each control key and value.
  const value = computed(() => Object.fromEntries(
    Object.entries(members).map(([name, control]) => [name, control.value()]),
  ) as FormValue<C>);
  const dirty = computed(() => children.some(control => control.dirty()));
  const touched = computed(() => children.some(control => control.touched()));
  return {
    ...state(value, dirty, touched, [...(options.validators ?? [])], () => children.every(control => control.valid())),
    controls: members,
    markAsDirty: () => { for (const child of children) child.markAsDirty(); },
    markAsPristine: () => { for (const child of children) child.markAsPristine(); },
    markAsTouched: () => { for (const child of children) child.markAsTouched(); },
    markAsUntouched: () => { for (const child of children) child.markAsUntouched(); },
    reset: () => { for (const child of children) child.reset(); },
  };
}

function lengthLimit(name: string, limit: number): void {
  if (!Number.isSafeInteger(limit) || limit < 0) throw new RangeError(`${name} must be a non-negative safe integer`);
}

function numericLimit(name: string, limit: number): void {
  if (!Number.isFinite(limit)) throw new RangeError(`${name} must be finite`);
}

export const Validators = {
  required(value: unknown): ValidationErrors | null {
    return value == null || value === "" || Array.isArray(value) && value.length === 0
      ? { required: true } : null;
  },
  requiredTrue(value: boolean): ValidationErrors | null {
    return value === true ? null : { requiredTrue: true };
  },
  minLength(minimum: number): ValidatorFn<string> {
    lengthLimit("minLength", minimum);
    return value => value.length > 0 && value.length < minimum
      ? { minLength: { requiredLength: minimum, actualLength: value.length } } : null;
  },
  maxLength(maximum: number): ValidatorFn<string> {
    lengthLimit("maxLength", maximum);
    return value => value.length > maximum
      ? { maxLength: { requiredLength: maximum, actualLength: value.length } } : null;
  },
  min(minimum: number): ValidatorFn<number> {
    numericLimit("min", minimum);
    return value => Number.isFinite(value) && value >= minimum
      ? null : { min: { min: minimum, actual: value } };
  },
  max(maximum: number): ValidatorFn<number> {
    numericLimit("max", maximum);
    return value => Number.isFinite(value) && value <= maximum
      ? null : { max: { max: maximum, actual: value } };
  },
  pattern(pattern: RegExp): ValidatorFn<string> {
    const expression = new RegExp(pattern.source, pattern.flags);
    return value => {
      expression.lastIndex = 0;
      return value === "" || expression.test(value)
        ? null : { pattern: { requiredPattern: expression.toString(), actualValue: value } };
    };
  },
  email(value: string): ValidationErrors | null {
    return value === "" || /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)
      ? null : { email: true };
  },
};
