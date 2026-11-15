import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { FormGroup } from "./FormGroup";

describe("FormGroup", () => {
  it("names a fieldset by its numbered legend", () => {
    const markup = renderToStaticMarkup(
      <FormGroup number={1} headingID="group-institution" heading="Institution">
        <input aria-label="Name" />
      </FormGroup>,
    );

    expect(markup).toMatch(/^<fieldset[^>]*aria-labelledby="group-institution"/);
    expect(markup).toContain("<legend");
    expect(markup).toContain('id="group-institution"');
    expect(markup).toContain('aria-hidden="true">01</span>');
    expect(markup).toContain("<span>Institution</span>");
  });

  it("renders a step as a section with an h2", () => {
    const markup = renderToStaticMarkup(
      <FormGroup as="section" number={2} headingID="step-code" heading="Enter the code it shows">
        <p>Code</p>
      </FormGroup>,
    );

    expect(markup).toMatch(/^<section[^>]*aria-labelledby="step-code"/);
    expect(markup).toMatch(/<h2[^>]*id="step-code"[^>]*><span[^>]*aria-hidden="true">02<\/span>/);
    expect(markup).not.toContain("<legend");
  });
});
