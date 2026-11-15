import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { InputField } from "./InputField";
import { PasswordField } from "./PasswordField";

describe("InputField", () => {
  it("associates its native constraint, description, and error", () => {
    const markup = renderToStaticMarkup(
      <InputField
        id="email"
        label="Email address"
        name="email"
        description="Use your institution address."
        errorMessage="Enter your email address."
        required
      />,
    );

    expect(markup).toContain('<label for="email">Email address</label>');
    expect(markup).not.toContain("data-required-indicator");
    expect(markup).toContain('required=""');
    expect(markup).toContain('aria-invalid="true"');
    expect(markup).toContain(
      'aria-describedby="email-description email-error"',
    );
  });

  it("marks a read-only value with a lock", () => {
    const markup = renderToStaticMarkup(
      <InputField id="invited-email" label="Email" name="email" readOnly defaultValue="a@example.edu" />,
    );

    expect(markup).toContain("readOnly");
    expect(markup).toContain('data-proctor-icon="readOnly"');
  });

  it("renders a named icon-only password disclosure", () => {
    const markup = renderToStaticMarkup(
      <PasswordField
        id="password"
        label="Password"
        name="password"
        hidePasswordLabel="Hide password"
        showPasswordLabel="Show password"
      />,
    );

    expect(markup).toContain('type="password"');
    expect(markup).toContain('aria-label="Show password"');
    expect(markup).toContain('title="Show password"');
    expect(markup).toContain('data-proctor-icon="showPassword"');
    expect(markup).not.toContain(">Show password</button>");
  });
});
