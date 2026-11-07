import { describe, expect, it } from "vitest";

import {
  createPublicAccessDiscoveryLoader,
  createSharedPublicAccessDiscovery,
  resolvePublicAccessDiscovery,
  type PublicAccessDiscovery,
  type PublicAccessDiscoveryResult,
  type PublicAccessDiscoveryTransport,
} from "./PublicAccessDiscovery";

const discovery = {
  discovery_version: 1,
  canonical_origin: "https://proctor.example",
  initialized: true,
  capabilities: {
    local_login: true,
    public_registration: true,
    invitation_admission: true,
    desktop_authorization: true,
  },
  desktop_authorization: {
    protocol: "proctor-desktop-authorization",
    minimum_version: 1,
    maximum_version: 1,
  },
  providers: [
    { id: "university-oidc", display_name: "University SSO", type: "oidc" },
  ],
} as const;

describe("resolvePublicAccessDiscovery", () => {
  it("returns one validated same-origin document", () => {
    expect(
      resolvePublicAccessDiscovery(discovery, "https://proctor.example"),
    ).toEqual({ kind: "ready", discovery });
  });

  it("accepts an omitted optional Institution presentation", () => {
    expect(
      resolvePublicAccessDiscovery(discovery, "https://proctor.example"),
    ).toEqual({ kind: "ready", discovery });
  });

  it("fails closed for invalid and mismatched canonical origins", () => {
    expect(
      resolvePublicAccessDiscovery(
        { ...discovery, canonical_origin: "not a URL" },
        "https://proctor.example",
      ),
    ).toEqual({ kind: "unavailable" });
    expect(
      resolvePublicAccessDiscovery(discovery, "https://other.example"),
    ).toEqual({ kind: "origin_mismatch" });
  });

  it.each([
    [{ ...discovery, capabilities: {} }],
    [{ ...discovery, desktop_authorization: {} }],
    [{ ...discovery, providers: [{}] }],
    [{ ...discovery, institution: { display_name: "Incomplete" } }],
    [{ ...discovery, discovery_version: 2 }],
  ])("rejects malformed documents", (value) => {
    expect(
      resolvePublicAccessDiscovery(value, "https://proctor.example"),
    ).toEqual({ kind: "unavailable" });
  });
});

describe("createPublicAccessDiscoveryLoader", () => {
  it("uses the feature-facing interface with an in-memory transport", async () => {
    const transport: PublicAccessDiscoveryTransport = {
      request: async () => ({ data: discovery, status: 200 }),
    };
    await expect(
      createPublicAccessDiscoveryLoader(transport)("https://proctor.example"),
    ).resolves.toEqual({ kind: "ready", discovery });
  });

  it("maps unexpected statuses and rejected requests to unavailability", async () => {
    const unexpected = createPublicAccessDiscoveryLoader({
      request: async () => ({ data: discovery, status: 204 }),
    });
    await expect(unexpected("https://proctor.example")).resolves.toEqual({
      kind: "unavailable",
    });

    const rejected = createPublicAccessDiscoveryLoader({
      request: async () => {
        throw new Error("offline");
      },
    });
    await expect(rejected("https://proctor.example")).resolves.toEqual({
      kind: "unavailable",
    });
  });
});

const sharedDiscovery = discovery as unknown as PublicAccessDiscovery;

describe("createSharedPublicAccessDiscovery", () => {
  it("shares one request and publishes its result", async () => {
    let requests = 0;
    const shared = createSharedPublicAccessDiscovery(async () => {
      requests += 1;
      return { kind: "ready", discovery: sharedDiscovery } as const;
    });
    let notifications = 0;
    shared.subscribe(() => {
      notifications += 1;
    });

    const [first, second] = await Promise.all([
      shared.load("https://proctor.example"),
      shared.load("https://proctor.example"),
    ]);

    expect(requests).toBe(1);
    expect(first).toBe(second);
    expect(shared.current()).toEqual({ kind: "ready", discovery });
    expect(notifications).toBe(1);
    await shared.load("https://proctor.example");
    expect(requests).toBe(1);
  });

  it("does not retain unavailability, so a retry requests again", async () => {
    const results: PublicAccessDiscoveryResult[] = [
      { kind: "unavailable" },
      { kind: "ready", discovery: sharedDiscovery },
    ];
    let requests = 0;
    const shared = createSharedPublicAccessDiscovery(async () => {
      requests += 1;
      return results.shift() ?? { kind: "unavailable" };
    });

    expect(await shared.load("https://proctor.example")).toEqual({ kind: "unavailable" });
    expect(await shared.load("https://proctor.example")).toEqual({ kind: "ready", discovery });
    expect(requests).toBe(2);
    expect(shared.current()).toEqual({ kind: "ready", discovery });
  });
});
