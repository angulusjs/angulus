import { Component, input, output } from "@angulus/core";

@Component({
  selector: "demo-badge",
  templateUrl: "./badge.html",
  styleUrl: "./badge.css",
})
export class BadgeComponent {
  readonly value = input.required<number>();
  readonly reset = output<number>();
  clear(): void { this.reset.emit(0); }
}
