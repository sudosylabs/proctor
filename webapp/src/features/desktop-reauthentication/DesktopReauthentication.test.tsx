import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import type { DesktopReauthenticationContext } from "./DesktopReauthenticationApi";
import {
  createDesktopReauthenticationJourney,
  desktopReauthenticationAPI,
  type DesktopReauthenticationSnapshot,
} from "./DesktopReauthenticationJourney";
import { DesktopReauthenticationContent } from "./DesktopReauthenticationPage";

const passwordContext: DesktopReauthenticationContext = { state: "bound", name: "Ada Candidate", expiresAt: "2026-08-01T09:05:00Z" };
const providerContext: DesktopReauthenticationContext = {
  ...passwordContext, provider: { id: "campus", display_name: "Campus sign-in", type: "oidc" },
};
const proof = { state: "state", handle: "handle", browserProof: "proof" };
const redirect = "http://127.0.0.1:49152/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?code=code&state=state";

function setup(context: DesktopReauthenticationContext = passwordContext) {
  const api = {
    approve: vi.fn<typeof desktopReauthenticationAPI.approve>().mockResolvedValue({ kind: "approved", redirectURL: redirect }),
    bind: vi.fn<typeof desktopReauthenticationAPI.bind>().mockResolvedValue({ kind: "bound" }),
    cancel: vi.fn<typeof desktopReauthenticationAPI.cancel>().mockResolvedValue({ kind: "canceled" }),
    context: vi.fn<typeof desktopReauthenticationAPI.context>().mockResolvedValue({ kind: "ready", context }),
    password: vi.fn<typeof desktopReauthenticationAPI.password>().mockResolvedValue({
      kind: "authenticated", context: { ...context, state: "authenticated" },
    }),
    providerURL: vi.fn<typeof desktopReauthenticationAPI.providerURL>().mockReturnValue("/api/v1/auth/desktop/reauthentications/authenticate/providers/campus/login?state=state"),
  };
  const navigate = vi.fn<(url: string, replace: boolean) => void>();
  const journey = createDesktopReauthenticationJourney({ proof, state: "state" }, api, navigate);
  return { api, journey, navigate };
}

function render(snapshot: DesktopReauthenticationSnapshot) {
  const actions = { provePassword: vi.fn(), chooseProvider: vi.fn(), cancel: vi.fn(), retry: vi.fn() };
  return renderToStaticMarkup(<DesktopReauthenticationContent snapshot={snapshot} actions={actions} />);
}

describe("Desktop reauthentication journey", () => {
  it("binds once and offers only the password for a password Session", async () => {
    const { api, journey } = setup();
    const first = journey.start();
    journey.stop();
    expect(journey.start()).toBe(first);
    await first;
    expect(api.bind).toHaveBeenCalledOnce();
    const markup = render(journey.getSnapshot());
    expect(markup).toContain("Confirm it’s you");
    expect(markup).toContain("Enter the password for Ada Candidate to continue in Proctor Desktop.");
    expect(markup).toContain('type="password"');
    expect(markup).not.toContain("Continue with");
  });

  it("offers only the Desktop Session's provider and never a password", async () => {
    const { journey, navigate } = setup(providerContext);
    await journey.start();
    const markup = render(journey.getSnapshot());
    expect(markup).toContain("Continue with Campus sign-in");
    expect(markup).not.toContain('type="password"');
    await journey.provePassword("ignored");
    journey.chooseProvider();
    expect(navigate).toHaveBeenCalledWith("/api/v1/auth/desktop/reauthentications/authenticate/providers/campus/login?state=state", false);
  });

  it("hands a confirmed password back to Desktop without another click", async () => {
    const { api, journey, navigate } = setup();
    await journey.start();
    await journey.provePassword("synthetic-local-password");
    expect(api.password).toHaveBeenCalledWith("synthetic-local-password");
    expect(api.approve).toHaveBeenCalledWith("state");
    expect(navigate).toHaveBeenCalledWith(redirect, true);
    expect(render(journey.getSnapshot())).toContain("You’re confirmed");
  });

  it("keeps the form after a wrong password and explains it", async () => {
    const { api, journey } = setup();
    api.password.mockResolvedValue({ kind: "invalid_password" });
    await journey.start();
    await journey.provePassword("wrong");
    expect(journey.getSnapshot().view.kind).toBe("proof");
    expect(render(journey.getSnapshot())).toContain("That password isn’t right. Try again.");
    expect(api.approve).not.toHaveBeenCalled();
  });

  it("returns from the provider straight to Desktop once proved", async () => {
    const { api, journey, navigate } = setup({ ...providerContext, state: "authenticated" });
    await journey.start();
    expect(api.approve).toHaveBeenCalledOnce();
    expect(navigate).toHaveBeenCalledWith(redirect, true);
  });

  it("shows an unusable request and a canceled one as terminal states", async () => {
    const invalid = setup();
    invalid.api.bind.mockResolvedValue({ kind: "invalid" });
    await invalid.journey.start();
    expect(render(invalid.journey.getSnapshot())).toContain("This link doesn’t work");
    expect(invalid.api.context).not.toHaveBeenCalled();

    const canceled = setup();
    await canceled.journey.start();
    await canceled.journey.cancel();
    expect(render(canceled.journey.getSnapshot())).toContain("Confirmation canceled");
  });

  it("recovers from an unavailable check by retrying", async () => {
    const { api, journey } = setup();
    api.context.mockResolvedValueOnce({ kind: "unavailable" });
    await journey.start();
    expect(render(journey.getSnapshot())).toContain("Try again");
    journey.retry();
    await vi.waitFor(() => expect(journey.getSnapshot().view.kind).toBe("proof"));
    expect(api.bind).toHaveBeenCalledOnce();
  });

  it("tells the person when the provider did not count", async () => {
    const api = setup(providerContext).api;
    const journey = createDesktopReauthenticationJourney({ state: "state", providerFailed: true }, api, vi.fn());
    await journey.start();
    expect(render(journey.getSnapshot())).toContain("Campus sign-in couldn’t confirm it’s you.");
    expect(api.bind).not.toHaveBeenCalled();
  });
});
