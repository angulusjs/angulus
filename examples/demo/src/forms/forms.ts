import { Component, signal } from "@angulus/core";
import { formControl, formGroup, Validators } from "@angulus/forms";

@Component({
  selector: "app-forms",
  templateUrl: "./forms.html",
  styleUrl: "./forms.css",
})
export class FormsComponent {
  readonly form = formGroup({
    name: formControl("", { validators: [Validators.required, Validators.minLength(2)] }),
    email: formControl("", { validators: [Validators.required, Validators.email] }),
    confirmation: formControl("", { validators: [Validators.required] }),
  }, {
    validators: [value => value.email === value.confirmation ? null : { emailMismatch: true }],
  });
  readonly submitted = signal("");

  submit(event: Event): void {
    event.preventDefault();
    this.form.markAsTouched();
    if (this.form.invalid()) {
      this.submitted.set("");
      return;
    }
    this.submitted.set(this.form.value().email);
  }

  reset(): void {
    this.form.reset();
    this.submitted.set("");
  }
}
