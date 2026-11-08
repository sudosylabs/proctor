import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { Fact, FactList } from "./FactList";

describe("FactList", () => {
  it("renders labelled facts as one description list in document order", () => {
    const markup = renderToStaticMarkup(
      <FactList aria-label="Invitation">
        <Fact label="Invited as">Student</Fact>
        <Fact label="Expires">Saturday 14 November 2026</Fact>
      </FactList>,
    );

    expect(markup).toMatch(/^<dl [^>]*aria-label="Invitation"/);
    expect(markup).toContain("data-proctor-fact-list");
    expect(markup.match(/<dt/g)).toHaveLength(2);
    expect(markup.match(/<dd/g)).toHaveLength(2);
    expect(markup.indexOf("Invited as")).toBeLessThan(markup.indexOf("Expires"));
    expect(markup).toMatch(/<dt[^>]*>Invited as<\/dt><dd[^>]*>Student<\/dd>/);
  });

  it("sets only code values in the data face and forwards layout classes", () => {
    const markup = renderToStaticMarkup(
      <FactList className="local-layout">
        <Fact label="Device">Laptop</Fact>
        <Fact label="Code" mono>R7KQ-2MVD</Fact>
      </FactList>,
    );

    expect(markup).toContain("local-layout");
    const values = markup.match(/<dd class="([^"]*)"/g) ?? [];
    expect(values).toHaveLength(2);
    expect(values[0]).not.toMatch(/mono/);
    expect(values[1]).toMatch(/mono/);
  });

  it("escapes user-controlled values instead of rendering markup", () => {
    const markup = renderToStaticMarkup(
      <FactList>
        <Fact label="Name">{"<b>Ada</b>"}</Fact>
      </FactList>,
    );

    expect(markup).toContain("&lt;b&gt;Ada&lt;/b&gt;");
  });
});
