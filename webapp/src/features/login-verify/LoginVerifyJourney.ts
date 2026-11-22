import { useEffect, useRef, useSyncExternalStore } from "react";

import {
  completeExternalSecondFactor,
  requestExternalSecondFactorContext,
  type ExternalSecondFactorContext,
} from "../external-second-factor/ExternalSecondFactorApi";

export type LoginVerifyView =
  | { kind: "checking" | "signing_in" | "expired" | "mfa_unavailable" | "unavailable" }
  | { kind: "code"; context: ExternalSecondFactorContext };

export type LoginVerifyFeedback = "code_invalid" | "rate_limited" | "unavailable";

export interface LoginVerifySnapshot {
  view: LoginVerifyView;
  pending: boolean;
  feedback?: LoginVerifyFeedback;
  focusHeading: boolean;
}

export interface LoginVerifyActions {
  verify(code: string): Promise<void>;
  retry(): void;
}

interface JourneyController extends LoginVerifyActions {
  getSnapshot(): LoginVerifySnapshot;
  subscribe(listener: () => void): () => void;
  start(): Promise<void>;
  stop(): void;
}

export const loginVerifyAPI = {
  context: requestExternalSecondFactorContext,
  complete: completeExternalSecondFactor,
};

// The journey owns sequencing: one code in flight at a time, and a finished
// sign-in leaves this page by replacing it so Back never returns to the code.
export function createLoginVerifyJourney(
  api = loginVerifyAPI,
  navigate: (url: string) => void = (url) => window.location.replace(url),
): JourneyController {
  let snapshot: LoginVerifySnapshot = { view: { kind: "checking" }, pending: false, focusHeading: false };
  let active = false;
  let generation = 0;
  let loading: Promise<void> | undefined;
  const listeners = new Set<() => void>();

  function update(next: Partial<LoginVerifySnapshot>) {
    snapshot = { ...snapshot, ...next };
    for (const listener of listeners) listener();
  }

  function settle(kind: "expired" | "mfa_unavailable" | "unavailable") {
    update({ view: { kind }, pending: false, feedback: undefined, focusHeading: true });
  }

  async function load(attempt: number) {
    const loaded = await api.context();
    if (!active || attempt !== generation) return;
    if (loaded.kind !== "ready") {
      settle(loaded.kind);
      return;
    }
    // A Desktop pause belongs to the Desktop authorization page.
    if (loaded.context.purpose !== "login") {
      settle("expired");
      return;
    }
    update({ view: { kind: "code", context: loaded.context } });
  }

  return {
    getSnapshot: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
    start() {
      active = true;
      loading ??= load(generation);
      return loading;
    },
    stop() {
      active = false;
    },
    retry() {
      if (!active || snapshot.view.kind !== "unavailable") return;
      const attempt = ++generation;
      update({ view: { kind: "checking" }, focusHeading: true });
      loading = load(attempt);
    },
    async verify(code) {
      if (!active || snapshot.pending || snapshot.view.kind !== "code") return;
      const attempt = ++generation;
      update({ pending: true, feedback: undefined });
      const result = await api.complete("login", code);
      if (!active || attempt !== generation) return;
      switch (result.kind) {
        case "signed_in":
          update({ view: { kind: "signing_in" }, pending: false, focusHeading: true });
          navigate(result.returnTo);
          return;
        case "code_invalid":
        case "rate_limited":
          update({ pending: false, feedback: result.kind });
          return;
        case "unavailable":
          update({ pending: false, feedback: "unavailable" });
          return;
        case "desktop_authenticated":
          settle("unavailable");
          return;
        default:
          settle(result.kind);
      }
    },
  };
}

export function useLoginVerifyJourney(): LoginVerifyActions & { snapshot: LoginVerifySnapshot } {
  const ref = useRef<JourneyController>(undefined);
  ref.current ??= createLoginVerifyJourney();
  const controller = ref.current;
  const snapshot = useSyncExternalStore(controller.subscribe, controller.getSnapshot);
  useEffect(() => {
    void controller.start();
    return controller.stop;
  }, [controller]);
  return { ...controller, snapshot };
}
