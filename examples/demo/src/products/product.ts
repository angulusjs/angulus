import { input } from "@angulus/core";

export class ProductComponent {
  readonly id = input.required<string>();
}
