import { Component, computed, signal } from "@angulus/core";
import { HttpError, httpResource } from "@angulus/http";

interface User { id: number; name: string }

function parseUser(value: unknown): User {
  if (typeof value !== "object" || value === null ||
      !("id" in value) || typeof value.id !== "number" ||
      !("name" in value) || typeof value.name !== "string") {
    throw new TypeError("Invalid user response");
  }
  return { id: value.id, name: value.name };
}

@Component({
  selector: "app-http",
  templateUrl: "./http.html",
  styleUrl: "./http.css",
})
export class HttpComponent {
  readonly selected = signal<number | undefined>(1);
  readonly user = httpResource(() => {
    const id = this.selected();
    return id === undefined ? undefined : `/data/users/${id}.json`;
  }, { parse: parseUser });
  readonly errorMessage = computed(() => {
    const error = this.user.error();
    if (error instanceof HttpError) return `HTTP ${error.status}: unable to load user.`;
    return error instanceof Error ? error.message : String(error);
  });

  select(id: number): void { this.selected.set(id); }
  pause(): void { this.selected.set(undefined); }
}
