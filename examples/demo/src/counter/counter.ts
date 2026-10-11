import { computed, signal } from "@angulus/core";

export class CounterComponent {
  readonly count = signal(0);
  readonly reachedLimit = computed(() => this.count() >= 10);
  readonly parity = computed(() => this.count() % 2 === 0 ? "even" : "odd");
  readonly name = signal("Angulus");
  readonly items = signal([
    { id: 1, label: "Signals" },
    { id: 2, label: "Go compiler" },
    { id: 3, label: "esbuild" },
  ]);

  increment(): void { this.count.update(value => value + 1); }
  reset(value: number): void { this.count.set(value); }
  reverse(): void { this.items.update(items => [...items].reverse()); }
  remove(): void { this.items.update(items => items.slice(0, -1)); }
}
