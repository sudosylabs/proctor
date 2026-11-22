import { describe, expect, it, vi } from "vitest";

import type { DesktopAuthorizationContext } from "./DesktopAuthorizationApi";
import { createDesktopAuthorizationJourney, desktopJourneyAPI } from "./DesktopAuthorizationJourney";

const bound: DesktopAuthorizationContext = {
  state: "bound", installation: "Example University", deviceName: "Exam laptop", expiresAt: "1970-01-01T00:00:01Z", requestedAt: "1970-01-01T00:00:00Z",
  localLoginEnabled: true, externalProviders: [{ id: "campus", display_name: "Campus", type: "oidc" }],
};
const authenticated: DesktopAuthorizationContext = {
  ...bound, state: "authenticated", account: { id: "user-1", username: "student", display_name: "Student", email: "student@example.edu" },
};
const input = { servingOrigin: "https://proctor.example", state: "state", proof: { state: "state", handle: "handle", browserProof: "proof" } };

function setup(currentSession = true) {
  const api = {
    approve: vi.fn<typeof desktopJourneyAPI.approve>().mockResolvedValue({ kind: "approved", redirectURL: "http://127.0.0.1:55000/callback" }),
    authenticate: vi.fn<typeof desktopJourneyAPI.authenticate>().mockResolvedValue({ kind: "authenticated", context: authenticated }),
    authenticateSession: vi.fn<typeof desktopJourneyAPI.authenticateSession>().mockResolvedValue(currentSession
      ? { kind: "authenticated", context: authenticated } : { kind: "no_session" }),
    bind: vi.fn<typeof desktopJourneyAPI.bind>().mockResolvedValue({ kind: "bound" }),
    cancel: vi.fn<typeof desktopJourneyAPI.cancel>().mockResolvedValue({ kind: "canceled" }),
    context: vi.fn<typeof desktopJourneyAPI.context>().mockResolvedValue({ kind: "ready", context: bound }),
    providerURL: vi.fn<typeof desktopJourneyAPI.providerURL>().mockReturnValue("/api/v1/auth/desktop/authorizations/authenticate/providers/campus/login?state=state"),
    reset: vi.fn<typeof desktopJourneyAPI.reset>().mockResolvedValue({ kind: "reset" }),
    secondFactor: vi.fn<typeof desktopJourneyAPI.secondFactor>().mockResolvedValue({
      kind: "ready", context: { purpose: "desktop_authorization", name: "Student", provider: "Campus", expiresAt: "1970-01-01T00:10:00Z" },
    }),
    completeSecondFactor: vi.fn<typeof desktopJourneyAPI.completeSecondFactor>().mockResolvedValue({ kind: "desktop_authenticated" }),
  };
  const navigate = vi.fn();
  const journey = createDesktopAuthorizationJourney(input, api, navigate);
  return { journey, api, navigate };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("Desktop Authorization journey", () => {
  it("keeps a missing request terminal without loading or making requests", async () => {
    const { api } = setup();
    const journey = createDesktopAuthorizationJourney({ servingOrigin: input.servingOrigin }, api);
    expect(journey.getSnapshot().view.kind).toBe("invalid");
    await journey.start();
    expect(journey.getSnapshot().view.kind).toBe("invalid");
    expect(api.bind).not.toHaveBeenCalled();
    expect(api.context).not.toHaveBeenCalled();
  });
  it("reuses initial binding across a Strict Mode effect restart", async () => {
    const { journey, api } = setup();
    const binding = deferred<{ kind: "bound" }>();
    api.bind.mockReturnValue(binding.promise);
    const first = journey.start();
    journey.stop();
    const restarted = journey.start();
    expect(restarted).toBe(first);
    binding.resolve({ kind: "bound" });
    await restarted;
    expect(api.bind).toHaveBeenCalledTimes(1);
    expect(journey.getSnapshot().view.kind).toBe("confirmation");
  });

  it("names the authenticated account while confirming and after approval only", async () => {
    const { journey, navigate } = setup();
    await journey.start();
    expect(journey.getSnapshot().account).toEqual(authenticated.account);
    await journey.approve();
    expect(journey.getSnapshot().view.kind).toBe("approved");
    expect(journey.getSnapshot().account).toEqual(authenticated.account);
    expect(navigate).toHaveBeenCalledOnce();

    const canceled = setup();
    await canceled.journey.start();
    await canceled.journey.cancel();
    expect(canceled.journey.getSnapshot().view.kind).toBe("canceled");
    expect(canceled.journey.getSnapshot().account).toBeUndefined();

    const signedOut = setup(false);
    await signedOut.journey.start();
    expect(signedOut.journey.getSnapshot().account).toBeUndefined();
  });

  it("discards confirmation after reset and retries without reapplying the Web Session", async () => {
    const { journey, api } = setup();
    await journey.start();
    api.context.mockResolvedValueOnce({ kind: "unavailable" });
    await journey.useAnotherAccount();
    expect(journey.getSnapshot().view).toEqual({ kind: "unavailable" });
    expect(JSON.stringify(journey.getSnapshot())).not.toContain("student");
    await journey.approve();
    expect(api.approve).not.toHaveBeenCalled();
    journey.retry();
    await vi.waitFor(() => expect(journey.getSnapshot().view.kind).toBe("authentication"));
    expect(api.authenticateSession).toHaveBeenCalledTimes(1);
    expect(api.reset).toHaveBeenCalledTimes(1);
    expect(api.bind).toHaveBeenCalledTimes(1);
  });

  it("also discards confirmation when the reset result is ambiguous", async () => {
    const { journey, api } = setup();
    await journey.start();
    api.reset.mockRejectedValue(new Error("connection lost"));
    await journey.useAnotherAccount();
    expect(journey.getSnapshot().view.kind).toBe("unavailable");
    expect(journey.getSnapshot().pending).toBeUndefined();
    journey.retry();
    await vi.waitFor(() => expect(journey.getSnapshot().view.kind).toBe("authentication"));
    expect(api.authenticateSession).toHaveBeenCalledTimes(1);
  });

  it("serializes local authentication, cancellation, provider navigation and repeated submission", async () => {
    const { journey, api, navigate } = setup(false);
    await journey.start();
    const authentication = deferred<{ kind: "authenticated"; context: DesktopAuthorizationContext }>();
    api.authenticate.mockReturnValue(authentication.promise);
    const first = journey.authenticate({ loginID: "student", password: "private" });
    await journey.authenticate({ loginID: "student", password: "private" });
    await journey.cancel();
    journey.chooseProvider("campus");
    journey.retry();
    expect(api.authenticate).toHaveBeenCalledTimes(1);
    expect(api.cancel).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
    expect(journey.getSnapshot().pending).toBe("authenticate");
    authentication.resolve({ kind: "authenticated", context: authenticated });
    expect(await first).toBe("authenticated");
    expect(journey.getSnapshot().view.kind).toBe("confirmation");
    expect(JSON.stringify(journey.getSnapshot())).not.toContain("private");
  });

  it("ignores an approval completion after unmount", async () => {
    const { journey, api, navigate } = setup();
    await journey.start();
    const approval = deferred<{ kind: "approved"; redirectURL: string }>();
    api.approve.mockReturnValue(approval.promise);
    const first = journey.approve();
    await journey.approve();
    await journey.useAnotherAccount();
    expect(api.approve).toHaveBeenCalledTimes(1);
    expect(api.reset).not.toHaveBeenCalled();
    journey.stop();
    approval.resolve({ kind: "approved", redirectURL: "http://127.0.0.1:55000/callback" });
    await first;
    expect(navigate).not.toHaveBeenCalled();
    expect(journey.getSnapshot().view.kind).not.toBe("approved");
  });

  it("keeps bounded form outcomes local and recovers from thrown adapters", async () => {
    const { journey, api } = setup(false);
    await journey.start();
    api.authenticate.mockResolvedValueOnce({ kind: "mfa_required" });
    expect(await journey.authenticate({ loginID: "student", password: "private" })).toBe("mfa_required");
    expect(journey.getSnapshot().view.kind).toBe("authentication");
    api.authenticate.mockRejectedValueOnce(new Error("offline"));
    await journey.authenticate({ loginID: "student", password: "private" });
    expect(journey.getSnapshot().feedback).toBe("unavailable");
    expect(journey.getSnapshot().pending).toBeUndefined();
    await journey.cancel();
    expect(journey.getSnapshot().view.kind).toBe("canceled");
    expect(journey.getSnapshot().focusHeading).toBe(true);
  });

  it("only navigates to a provider in the current context", async () => {
    const { journey, api, navigate } = setup(false);
    await journey.start();
    journey.chooseProvider("unlisted");
    expect(navigate).not.toHaveBeenCalled();
    journey.chooseProvider("campus");
    journey.chooseProvider("campus");
    expect(api.providerURL).toHaveBeenCalledExactlyOnceWith("campus", "state");
    expect(navigate).toHaveBeenCalledTimes(1);
    expect(journey.getSnapshot().pending).toBe("provider");
  });
  describe("after a provider that proved one factor", () => {
    const paused: DesktopAuthorizationContext = { ...bound, state: "second_factor_required", localLoginEnabled: false, externalProviders: [] };

    it("asks for the Proctor code before Allow, then offers Allow", async () => {
      const { journey, api } = setup(false);
      api.context.mockResolvedValueOnce({ kind: "ready", context: paused });
      await journey.start();
      const view = journey.getSnapshot().view;
      expect(view.kind).toBe("second_factor");
      if (view.kind === "second_factor") expect(view.secondFactor.provider).toBe("Campus");
      expect(api.authenticateSession).not.toHaveBeenCalled();
      api.context.mockResolvedValueOnce({ kind: "ready", context: authenticated });
      await journey.verifyCode("123456");
      expect(api.completeSecondFactor).toHaveBeenCalledWith("desktop_authorization", "123456");
      expect(journey.getSnapshot().view.kind).toBe("confirmation");
      expect(journey.getSnapshot().focusHeading).toBe(true);
    });

    it("keeps the code step after a wrong code and reports too many attempts", async () => {
      const { journey, api } = setup(false);
      api.context.mockResolvedValue({ kind: "ready", context: paused });
      await journey.start();
      api.completeSecondFactor.mockResolvedValueOnce({ kind: "code_invalid" });
      await journey.verifyCode("000000");
      expect(journey.getSnapshot().view.kind).toBe("second_factor");
      expect(journey.getSnapshot().codeFeedback).toBe("code_invalid");
      api.completeSecondFactor.mockResolvedValueOnce({ kind: "rate_limited" });
      await journey.verifyCode("111111");
      expect(journey.getSnapshot().codeFeedback).toBe("rate_limited");
    });

    it("ends the request when the pause ran out", async () => {
      const { journey, api } = setup(false);
      api.context.mockResolvedValue({ kind: "ready", context: paused });
      await journey.start();
      api.completeSecondFactor.mockResolvedValueOnce({ kind: "expired" });
      await journey.verifyCode("123456");
      expect(journey.getSnapshot().view.kind).toBe("second_factor_expired");
    });

    it("reads an already expired pause as timed out", async () => {
      const { journey, api } = setup(false);
      api.context.mockResolvedValue({ kind: "ready", context: paused });
      api.secondFactor.mockResolvedValueOnce({ kind: "expired" });
      await journey.start();
      expect(journey.getSnapshot().view.kind).toBe("second_factor_expired");
    });

    it("never offers Allow while the pause can't be read", async () => {
      const { journey, api } = setup(false);
      api.context.mockResolvedValue({ kind: "ready", context: paused });
      api.secondFactor.mockResolvedValueOnce({ kind: "unavailable" });
      await journey.start();
      expect(journey.getSnapshot().view.kind).toBe("unavailable");
      await journey.approve();
      expect(api.approve).not.toHaveBeenCalled();
    });
  });
});
