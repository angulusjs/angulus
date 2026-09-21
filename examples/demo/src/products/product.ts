import { Component, input } from "@angulus/core";

@Component({
  selector: "app-product",
  templateUrl: "./product.html",
  styleUrl: "./product.css",
})
export class ProductComponent {
  readonly id = input.required<string>();
}
