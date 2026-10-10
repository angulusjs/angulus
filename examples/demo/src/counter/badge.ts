import { input, output } from "@angulus/core";

export class BadgeComponent {
  readonly value = input.required<number>();
  readonly reset = output<number>();
  clear(): void { this.reset.emit(0); }
}
