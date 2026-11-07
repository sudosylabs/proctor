import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { hostedInstitutionFrom, useHostedInstitution } from "./HostedInstitution";
import type { PublicAccessDiscovery } from "./PublicAccessDiscovery";

const discovery = {
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
} satisfies PublicAccessDiscovery;

describe("hostedInstitutionFrom", () => {
  it("exposes the validated Institution display name", () => {
    expect(hostedInstitutionFrom({ kind: "ready", discovery })).toEqual({
      displayName: "Lagoon City University",
    });
  });

  it("omits the name while loading, after failure, or when it is absent", () => {
    expect(hostedInstitutionFrom(undefined)).toEqual({});
    expect(hostedInstitutionFrom({ kind: "unavailable" })).toEqual({});
    expect(hostedInstitutionFrom({ kind: "origin_mismatch" })).toEqual({});
    expect(
      hostedInstitutionFrom({
        kind: "ready",
        discovery: { ...discovery, institution: undefined },
      }),
    ).toEqual({});
  });

  it("defaults to no Institution outside a provider", () => {
    function Probe() {
      return <>{JSON.stringify(useHostedInstitution())}</>;
    }
    expect(renderToStaticMarkup(<Probe />)).toBe("{}");
  });
});
