import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { safeReturnPath, type ExternalSecondFactorContext } from "../external-second-factor/ExternalSecondFactorApi";
import { createLoginVerifyJourney, loginVerifyAPI, type LoginVerifySnapshot } from "./LoginVerifyJourney";
import { LoginVerifyContent } from "./LoginVerifyPage";

const paused: ExternalSecondFactorContext = {
  purpose: "login", name: "Ada Candidate", provider: "University SSO", expiresAt: "2026-08-01T09:10:00Z",
};

function setup() {
  const api = {
    context: vi.fn<typeof loginVerifyAPI.context>().mockResolvedValue({ kind: "ready", context: paused }),
    complete: vi.fn<typeof loginVerifyAPI.complete>().mockResolvedValue({ kind: "signed_in", returnTo: "/authorization/complete" }),
  };
  const navigate = vi.fn<(url: string) => void>();
  const journey = createLoginVerifyJourney(api, navigate);
  return { api, journey, navigate };
}

function render(snapshot: LoginVerifySnapshot) {
  const actions = { verify: vi.fn(), retry: vi.fn() };
  return renderToStaticMarkup(<LoginVerifyContent snapshot={snapshot} actions={actions} />);
}

describe("sign-in code after an external provider", () => {
  it("asks for the code and names the provider that signed the person in", async () => {
    const { journey } = setup();
    await journey.start();
    const markup = render(journey.getSnapshot());
    expect(markup).toContain("Enter your code");
    expect(markup).toContain("Signed in with University SSO");
    expect(markup).toContain("Open your authenticator app and enter the 6-digit code for Proctor to finish signing in.");
    expect(markup).toContain("Use a recovery code instead");
    expect(markup).toContain('href="/login"');
  });

  it("signs in and replaces the page with where the server sent it", async () => {
    const { api, journey, navigate } = setup();
    await journey.start();
    await journey.verify("123456");
    expect(api.complete).toHaveBeenCalledWith("login", "123456");
    expect(journey.getSnapshot().view.kind).toBe("signing_in");
    expect(navigate).toHaveBeenCalledWith("/authorization/complete");
  });

  it("keeps the code step after a wrong code, and reports too many attempts", async () => {
    const { api, journey, navigate } = setup();
    await journey.start();
    api.complete.mockResolvedValueOnce({ kind: "code_invalid" });
    await journey.verify("000000");
    expect(render(journey.getSnapshot())).toContain("That code isn’t right. Check your authenticator app and try again.");
    api.complete.mockResolvedValueOnce({ kind: "rate_limited" });
    await journey.verify("111111");
    expect(render(journey.getSnapshot())).toContain("Too many attempts. Wait a moment, then try again.");
    expect(journey.getSnapshot().view.kind).toBe("code");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("sends a timed-out sign-in back to the start", async () => {
    const { api, journey } = setup();
    api.context.mockResolvedValueOnce({ kind: "expired" });
    await journey.start();
    const markup = render(journey.getSnapshot());
    expect(markup).toContain("This sign-in took too long");
    expect(markup).toContain("Start again");
  });

  it("explains when two-step verification is turned off", async () => {
    const { api, journey } = setup();
    await journey.start();
    api.complete.mockResolvedValueOnce({ kind: "mfa_unavailable" });
    await journey.verify("123456");
    const markup = render(journey.getSnapshot());
    expect(markup).toContain("You can’t finish signing in right now");
    expect(markup).toContain("turned off for your institution");
    expect(markup).toContain("Back to sign-in");
  });

  it("treats a Desktop pause as not this page's sign-in", async () => {
    const { api, journey } = setup();
    api.context.mockResolvedValueOnce({ kind: "ready", context: { ...paused, purpose: "desktop_authorization" } });
    await journey.start();
    expect(journey.getSnapshot().view.kind).toBe("expired");
  });

  it("recovers from an unavailable check by retrying", async () => {
    const { api, journey } = setup();
    api.context.mockResolvedValueOnce({ kind: "unavailable" });
    await journey.start();
    expect(render(journey.getSnapshot())).toContain("We couldn’t check your sign-in");
    journey.retry();
    await vi.waitFor(() => expect(journey.getSnapshot().view.kind).toBe("code"));
  });

  it("sends only one code at a time", async () => {
    const { api, journey } = setup();
    await journey.start();
    let finish!: () => void;
    api.complete.mockReturnValueOnce(new Promise((resolve) => {
      finish = () => resolve({ kind: "code_invalid" });
    }));
    const first = journey.verify("123456");
    await journey.verify("654321");
    finish();
    await first;
    expect(api.complete).toHaveBeenCalledOnce();
  });
});

describe("the finished sign-in's destination", () => {
  it("follows a same-origin path the server chose", () => {
    expect(safeReturnPath("/authorization/complete")).toBe("/authorization/complete");
    expect(safeReturnPath("/account/security?tab=two-step")).toBe("/account/security?tab=two-step");
  });

  it("never follows another origin or an ambiguous path", () => {
    for (const value of [undefined, "", "https://evil.example/", "//evil.example/", "/\\evil.example", "javascript:alert(1)", "relative", "/a\nb"]) {
      expect(safeReturnPath(value)).toBe("/authorization/complete");
    }
  });
});
