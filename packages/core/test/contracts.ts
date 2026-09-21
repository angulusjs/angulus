import { Component, input, output, signal, type Input, type InputValue, type OutputValue } from "../src/index.js";

function checkInput<I extends Input<unknown>>(_input: I, _value: InputValue<NoInfer<I>>): void {}

function verifyContracts(): void {
  const name = input.required<string>();
  const count = input(0);
  const changed = output<{ id: number }>();
  checkInput(name, "valid");
  checkInput(count, 1);
  // @ts-expect-error The value must not widen the inferred input type.
  checkInput(name, 42);
  // @ts-expect-error Ordinary signals are not component inputs.
  checkInput(signal("not an input"), "value");
  // @ts-expect-error Inputs are read-only.
  name.set("no");
  const event: OutputValue<typeof changed> = { id: 1 };
  changed.emit(event);
  // @ts-expect-error Outputs preserve their payload type.
  const badEvent: OutputValue<typeof changed> = "invalid";
  void badEvent;
  Component({ selector: "test-contract", templateUrl: "./test.html", customElements: ["third-party-widget"] })(class {});
}
void verifyContracts;
