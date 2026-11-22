import { useEffect, useRef, useSyncExternalStore } from "react";

import { createAsyncResourceController } from "../../app/AsyncResource";
import {
  approveDesktopAuthorization,
  authenticateDesktopAuthorizationLocally,
  authenticateDesktopAuthorizationSession,
  bindDesktopAuthorization,
  cancelDesktopAuthorization,
  desktopAuthorizationProviderURL,
  requestDesktopAuthorizationContext,
  resetDesktopAuthorizationAccount,
  type DesktopAuthorizationContext,
  type DesktopAuthorizationProof,
  type DesktopContextResult,
  type DesktopLocalAuthenticationSubmission,
} from "./DesktopAuthorizationApi";
import {
  completeExternalSecondFactor,
  requestExternalSecondFactorContext,
  type ExternalSecondFactorContext,
} from "../external-second-factor/ExternalSecondFactorApi";

/** `second_factor_expired`: the paused provider sign-in ran out before its code. */
export type DesktopTerminalState = "approved" | "canceled" | "invalid" | "locked" | "second_factor_expired";
type PendingAction = "approve" | "cancel" | "reset" | "authenticate" | "provider" | "verify";
export type DesktopSecondFactorFeedback = "code_invalid" | "rate_limited";
export type DesktopLocalFeedback =
  | "authenticated"
  | "mfa_required"
  | "mfa_invalid"
  | "invalid_credentials"
  | "rate_limited"
  | "ignored";

export interface DesktopJourneySnapshot {
  view:
    | { kind: "checking" | "unavailable" | DesktopTerminalState }
    | { kind: "authentication" | "confirmation"; context: DesktopAuthorizationContext }
    | { kind: "second_factor"; context: DesktopAuthorizationContext; secondFactor: ExternalSecondFactorContext };
  /** The account this request authenticated, while confirming or after approval. */
  account?: NonNullable<DesktopAuthorizationContext["account"]>;
  pending?: PendingAction;
  feedback?: "unavailable";
  /** The server's verdict on the last Proctor code. */
  codeFeedback?: DesktopSecondFactorFeedback;
  focusHeading: boolean;
}

interface DesktopJourneyActions {
  verifyCode(code: string): Promise<void>;
  approve(): Promise<void>;
  cancel(): Promise<void>;
  useAnotherAccount(): Promise<void>;
  authenticate(
    submission: DesktopLocalAuthenticationSubmission,
  ): Promise<DesktopLocalFeedback>;
  chooseProvider(providerID: string): void;
  retry(): void;
}

export interface DesktopAuthorizationJourney extends DesktopJourneyActions {
  snapshot: DesktopJourneySnapshot;
}

interface JourneyController extends DesktopJourneyActions {
  getSnapshot(): DesktopJourneySnapshot;
  subscribe(listener: () => void): () => void;
  start(): Promise<DesktopContextResult>;
  stop(): void;
}

interface JourneyInput {
  proof?: DesktopAuthorizationProof;
  state?: string;
  servingOrigin: string;
}

export const desktopJourneyAPI = {
  approve: approveDesktopAuthorization,
  authenticate: authenticateDesktopAuthorizationLocally,
  authenticateSession: authenticateDesktopAuthorizationSession,
  bind: bindDesktopAuthorization,
  cancel: cancelDesktopAuthorization,
  context: requestDesktopAuthorizationContext,
  providerURL: desktopAuthorizationProviderURL,
  reset: resetDesktopAuthorizationAccount,
  secondFactor: requestExternalSecondFactorContext,
  completeSecondFactor: completeExternalSecondFactor,
};

// The feature owns transaction sequencing. Presentation owns form values and
// field validation; the shared resource owns initial-load/retry generations.
export function createDesktopAuthorizationJourney(
  input: JourneyInput,
  api = desktopJourneyAPI,
  navigate: (url: string, replace: boolean) => void = (url, replace) => {
    if (replace) window.location.replace(url);
    else window.location.assign(url);
  },
): JourneyController {
  let bindingEstablished = input.proof === undefined;
  let useWebSession = true;
  let active = false;
  let generation = 0;
  let pending: PendingAction | undefined;
  let terminal: DesktopTerminalState | undefined;
  let feedback: "unavailable" | undefined;
  let codeFeedback: DesktopSecondFactorFeedback | undefined;
  let secondFactor: ExternalSecondFactorContext | undefined;
  let focusHeading = false;
  const listeners = new Set<() => void>();

  async function load(): Promise<DesktopContextResult> {
    try {
      if (input.state === undefined) return { kind: "invalid" };
      if (!bindingEstablished && input.proof !== undefined) {
        const bound = await api.bind(input.proof);
        if (bound.kind !== "bound") return bound;
        bindingEstablished = true;
      }
      const loaded = await api.context(input.servingOrigin);
      secondFactor = undefined;
      if (loaded.kind === "ready" && loaded.context.state === "second_factor_required") {
        // The pause names the provider and its own deadline; it is read only
        // while the request waits for a code.
        const paused = await api.secondFactor();
        if (paused.kind === "ready" && paused.context.purpose === "desktop_authorization") {
          secondFactor = paused.context;
          return loaded;
        }
        if (paused.kind === "expired") {
          terminal = "second_factor_expired";
          focusHeading = true;
          return loaded;
        }
        return { kind: "unavailable" };
      }
      if (loaded.kind !== "ready" || loaded.context.state !== "bound" || !useWebSession) {
        return loaded;
      }
      const session = await api.authenticateSession(loaded.context.installation);
      if (session.kind === "authenticated") {
        return { kind: "ready", context: session.context };
      }
      if (session.kind === "no_session") return loaded;
      if (session.kind === "invalid" || session.kind === "locked") return session;
      return { kind: "unavailable" };
    } catch {
      return { kind: "unavailable" };
    }
  }

  const resource = createAsyncResourceController<DesktopContextResult>(
    load,
    input.state === undefined ? { kind: "invalid" } : { kind: "unavailable" },
    input.state !== undefined,
  );
  let snapshot: DesktopJourneySnapshot;

  function currentView(): DesktopJourneySnapshot["view"] {
    if (terminal !== undefined) return { kind: terminal };
    const current = resource.getSnapshot();
    if (current.loading) return { kind: "checking" };
    if (current.value.kind !== "ready") return { kind: current.value.kind };
    const context = current.value.context;
    switch (context.state) {
      case "bound":
        return { kind: "authentication", context };
      case "second_factor_required":
        // Never offer Allow before the code: without the pause details the
        // request cannot be shown, so it reads as unavailable.
        return secondFactor === undefined ? { kind: "unavailable" } : { kind: "second_factor", context, secondFactor };
      case "authenticated":
        return { kind: "confirmation", context };
    }
  }

  function publish() {
    const view = currentView();
    const account = view.kind === "confirmation" || view.kind === "approved"
      ? currentContext("authenticated")?.account : undefined;
    snapshot = {
      view, ...(account === undefined ? {} : { account }), pending, feedback,
      ...(codeFeedback === undefined ? {} : { codeFeedback }), focusHeading,
    };
    for (const listener of listeners) listener();
  }
  resource.subscribe(publish);

  function failed(result: { kind: string }) {
    if (result.kind === "invalid" || result.kind === "locked") {
      terminal = result.kind;
      focusHeading = true;
    } else {
      feedback = "unavailable";
    }
  }

  async function perform<T>(
    action: PendingAction,
    work: () => Promise<T>,
    apply: (result: T) => void | Promise<void>,
  ) {
    if (!active || pending !== undefined || terminal !== undefined || resource.getSnapshot().loading) {
      return;
    }
    const attempt = ++generation;
    pending = action;
    feedback = undefined;
    publish();
    try {
      const result = await work();
      if (active && generation === attempt) await apply(result);
    } catch {
      if (active && generation === attempt) failed({ kind: "unavailable" });
    } finally {
      if (active && generation === attempt) {
        pending = undefined;
        publish();
      }
    }
  }

  function currentContext(state: DesktopAuthorizationContext["state"]) {
    const current = resource.getSnapshot();
    return !current.loading && current.value.kind === "ready" && current.value.context.state === state
      ? current.value.context : undefined;
  }

  return {
    getSnapshot: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
    start() {
      active = true;
      publish();
      if (input.state === undefined) return Promise.resolve({ kind: "invalid" });
      return resource.start();
    },
    stop() {
      active = false;
      generation += 1;
      pending = undefined;
    },
    retry() {
      if (!active || pending !== undefined || terminal !== undefined || resource.getSnapshot().loading ||
        resource.getSnapshot().value.kind !== "unavailable") return;
      focusHeading = true;
      feedback = undefined;
      void resource.retry();
    },
    async approve() {
      if (input.state === undefined || currentContext("authenticated") === undefined) return;
      await perform("approve", () => api.approve(input.state!), (result) => {
        if (result.kind === "approved") {
          terminal = "approved";
          focusHeading = true;
          publish();
          navigate(result.redirectURL, true);
        } else failed(result);
      });
    },
    async cancel() {
      if (input.state === undefined || resource.getSnapshot().value.kind !== "ready") return;
      await perform("cancel", () => api.cancel(input.state!), (result) => {
        if (result.kind === "canceled") {
          terminal = "canceled";
          focusHeading = true;
        } else failed(result);
      });
    },
    async useAnotherAccount() {
      if (currentContext("authenticated") === undefined) return;
      await perform("reset", () => {
        // A reset can commit even when its response is lost. Never retain the
        // old account confirmation or automatically reapply the Web Session.
        useWebSession = false;
        focusHeading = true;
        resource.replace({ kind: "unavailable" });
        return api.reset();
      }, async (result) => {
        if (result.kind === "reset") await resource.retry();
        else failed(result);
      });
    },
    async authenticate(submission) {
      const context = currentContext("bound");
      if (context === undefined || !context.localLoginEnabled) return "ignored";
      let local: DesktopLocalFeedback = "ignored";
      await perform("authenticate", () => api.authenticate(context.installation, submission), (result) => {
        switch (result.kind) {
          case "authenticated":
            local = "authenticated";
            focusHeading = true;
            resource.replace({ kind: "ready", context: result.context });
            break;
          case "mfa_required":
          case "mfa_invalid":
          case "invalid_credentials":
          case "rate_limited":
            local = result.kind;
            break;
          default:
            failed(result);
        }
      });
      return local;
    },
    async verifyCode(code) {
      if (currentContext("second_factor_required") === undefined || secondFactor === undefined) return;
      codeFeedback = undefined;
      await perform("verify", () => api.completeSecondFactor("desktop_authorization", code), async (result) => {
        switch (result.kind) {
          case "desktop_authenticated":
            // The request now waits for Allow; read it again rather than trust
            // a second shape of the same context.
            focusHeading = true;
            await resource.retry();
            return;
          case "code_invalid":
          case "rate_limited":
            codeFeedback = result.kind;
            return;
          case "expired":
            terminal = "second_factor_expired";
            focusHeading = true;
            return;
          default:
            failed({ kind: "unavailable" });
        }
      });
    },
    chooseProvider(providerID) {
      const context = currentContext("bound");
      if (!active || pending !== undefined || terminal !== undefined || input.state === undefined ||
        context === undefined || !context.externalProviders.some((provider) => provider.id === providerID)) return;
      pending = "provider";
      feedback = undefined;
      publish();
      try {
        navigate(api.providerURL(providerID, input.state), false);
      } catch {
        pending = undefined;
        feedback = "unavailable";
        publish();
      }
    },
  };
}

export function useDesktopAuthorizationJourney(input: JourneyInput): DesktopAuthorizationJourney {
  const ref = useRef<JourneyController>(undefined);
  ref.current ??= createDesktopAuthorizationJourney(input);
  const controller = ref.current;
  const snapshot = useSyncExternalStore(controller.subscribe, controller.getSnapshot);
  useEffect(() => {
    void controller.start();
    return controller.stop;
  }, [controller]);
  return { ...controller, snapshot };
}
