import { test } from "node:test";
import assert from "node:assert/strict";
import { CounterComponent } from "./counter";

test("counter instances own their state", () => {
  const first = new CounterComponent();
  const second = new CounterComponent();
  first.increment();
  assert.equal(first.count(), 1);
  assert.equal(second.count(), 0);
  assert.equal(first.reachedLimit(), false);
});
