import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { HostedInstitutionProvider } from "../../auth/HostedInstitution";
import type {
  PublicAccessDiscoveryResult,
  SharedPublicAccessDiscovery,
} from "../../auth/PublicAccessDiscovery";
import { ProductThemeProvider, resolveProductTheme } from "../../theme/ProductTheme";
import { AccessPageShell, type AccessPageShellProps } from "./AccessPageShell";

function fixedDiscovery(result?: PublicAccessDiscoveryResult): SharedPublicAccessDiscovery {
  return {
    current: () => result,
    load: async () => result ?? { kind: "unavailable" },
    subscribe: () => () => undefined,
  };
}

const ready: PublicAccessDiscoveryResult = {
  kind: "ready",
  discovery: {
    discovery_version: 1,
    canonical_origin: "https://proctor.example",
    initialized: true,
    capabilities: {
      local_login: true,
      public_registration: false,
      invitation_admission: true,
      desktop_authorization: true,
    },
    desktop_authorization: {
      protocol: "proctor-desktop-authorization",
      minimum_version: 1,
      maximum_version: 1,
    },
    institution: { id: "i-1", name: "lagoon", display_name: "Lagoon City University" },
    providers: [],
  },
};

function render(
  props: Partial<AccessPageShellProps> = {},
  result?: PublicAccessDiscoveryResult,
) {
  return renderToStaticMarkup(
    <ProductThemeProvider value={resolveProductTheme("light", "light")}>
      <HostedInstitutionProvider discovery={fixedDiscovery(result)}>
        <AccessPageShell skipLabel="Skip to main content" {...props}>
          <h1>Task</h1>
        </AccessPageShell>
      </HostedInstitutionProvider>
    </ProductThemeProvider>,
  );
}

describe("AccessPageShell", () => {
  it("keeps the skip link, one focusable main landmark, masthead, and footer", () => {
    const markup = render();

    expect(markup).toMatch(/^<a class="proctor-skip-link" href="#main-content">Skip to main content<\/a>/);
    expect(markup.match(/<main /g)).toHaveLength(1);
    expect(markup).toContain('<main id="main-content"');
    expect(markup).toContain('tabindex="-1"');
    expect(markup).toMatch(/<header[^>]*><img[^>]*alt="Proctor"/);
    expect(markup).toContain("<footer");
  });

  it("omits the Institution until discovery names it", () => {
    const markup = render();

    expect(markup).not.toContain("data-proctor-shell-institution");
    expect(markup).toContain("Need help? Contact your institution.");
    expect(markup).not.toContain("Signed in as");
  });

  it("names the Institution in the masthead and help line", () => {
    const markup = render({}, ready);

    expect(markup).toMatch(/data-proctor-shell-institution="">Lagoon City University</);
    expect(markup).toContain("Need help? Contact Lagoon City University.");
  });

  it("shows the page-supplied signed-in identity and sign-out control", () => {
    const markup = render({
      signedIn: { name: "Adaeze <Okonkwo>", signOut: <button type="button">Sign out</button> },
    });

    expect(markup).toContain("Signed in as Adaeze &lt;Okonkwo&gt;");
    expect(markup).toContain('<button type="button">Sign out</button>');
  });
});
