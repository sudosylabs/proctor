import { describe, expect, it } from "vitest";

import { formatPasswordLength, passwordMinimumLengthFrom } from "./passwordPolicy";
import type { PublicAccessDiscovery } from "./PublicAccessDiscovery";

const discovery = {
  discovery_version: 1,
  password_minimum_length: 14,
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
  providers: [],
} satisfies PublicAccessDiscovery;

describe("passwordMinimumLengthFrom", () => {
  it("reads the discovered minimum only from a ready document", () => {
    expect(passwordMinimumLengthFrom({ kind: "ready", discovery })).toBe(14);
    expect(passwordMinimumLengthFrom(undefined)).toBeUndefined();
    expect(passwordMinimumLengthFrom({ kind: "unavailable" })).toBeUndefined();
    expect(passwordMinimumLengthFrom({ kind: "origin_mismatch" })).toBeUndefined();
  });

  it("formats the count as a whole number", () => {
    expect(formatPasswordLength(12)).toBe("12");
  });
});
