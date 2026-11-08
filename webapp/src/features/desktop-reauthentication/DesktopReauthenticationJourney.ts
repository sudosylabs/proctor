import { useEffect, useRef, useSyncExternalStore } from "react";

import {
  approveDesktopReauthentication,
  bindDesktopReauthentication,
  cancelDesktopReauthentication,
  desktopReauthenticationProviderURL,
  proveDesktopReauthenticationPassword,
  requestDesktopReauthenticationContext,
  type DesktopReauthenticationContext,
  type DesktopReauthenticationProof,
} from "./DesktopReauthenticationApi";

export type DesktopReauthenticationView =
  | { kind: "checking" | "returning" | "approved" | "canceled" | "invalid" | "unavailable" }
  | { kind: "proof"; context: DesktopReauthenticationContext };

export type DesktopReauthenticationFeedback = "invalid_password" | "rate_limited" | "unavailable";

export interface DesktopReauthenticationSnapshot {
  view: DesktopReauthenticationView;
  pending?: "password" | "provider" | "cancel";
  feedback?: DesktopReauthenticationFeedback;
  /** The provider returned without counting as proof for this Desktop. */
  providerFailed: boolean;
  focusHeading: boolean;
}

export interface DesktopReauthenticationActions {
  provePassword(password: string): Promise<void>;
  chooseProvider(): void;
  cancel(): Promise<void>;
  retry(): void;
}

interface JourneyController extends DesktopReauthenticationActions {
  getSnapshot(): DesktopReauthenticationSnapshot;
  subscribe(listener: () => void): () => void;
  start(): Promise<void>;
  stop(): void;
}

export interface DesktopReauthenticationInput {
  proof?: DesktopReauthenticationProof;
  state?: string;
  providerFailed?: boolean;
}

export const desktopReauthenticationAPI = {
  approve: approveDesktopReauthentication,
  bind: bindDesktopReauthentication,
  cancel: cancelDesktopReauthentication,
  context: requestDesktopReauthenticationContext,
  password: proveDesktopReauthenticationPassword,
  providerURL: desktopReauthenticationProviderURL,
};

// The journey owns sequencing: binding happens once, a proved request is
// handed back to Desktop without another click, and no two actions overlap.
// Presentation owns only field values and validation.
export function createDesktopReauthenticationJourney(
  input: DesktopReauthenticationInput,
  api = desktopReauthenticationAPI,
  navigate: (url: string, replace: boolean) => void = (url, replace) => {
    if (replace) window.location.replace(url);
    else window.location.assign(url);
  },
): JourneyController {
  let snapshot: DesktopReauthenticationSnapshot = {
    view: { kind: input.state === undefined ? "invalid" : "checking" },
    providerFailed: input.providerFailed === true,
    focusHeading: false,
  };
  let bindingEstablished = input.proof === undefined;
  let active = false;
  let generation = 0;
  let loading: Promise<void> | undefined;
  const listeners = new Set<() => void>();

  function update(next: Partial<DesktopReauthenticationSnapshot>) {
    snapshot = { ...snapshot, ...next };
    for (const listener of listeners) listener();
  }

  function terminal(kind: "invalid" | "unavailable" | "canceled") {
    update({ view: { kind }, pending: undefined, feedback: undefined, focusHeading: true });
  }

  async function handBack(attempt: number) {
    update({ view: { kind: "returning" }, pending: undefined, feedback: undefined, providerFailed: false });
    const approved = await api.approve(input.state!);
    if (!active || attempt !== generation) return;
    if (approved.kind !== "approved") {
      terminal(approved.kind);
      return;
    }
    update({ view: { kind: "approved" }, focusHeading: true });
    navigate(approved.redirectURL, true);
  }

  async function load(attempt: number) {
    try {
      if (!bindingEstablished && input.proof !== undefined) {
        const bound = await api.bind(input.proof);
        if (!active || attempt !== generation) return;
        if (bound.kind !== "bound") {
          terminal(bound.kind);
          return;
        }
        bindingEstablished = true;
      }
      const loaded = await api.context();
      if (!active || attempt !== generation) return;
      if (loaded.kind !== "ready") {
        terminal(loaded.kind);
        return;
      }
      if (loaded.context.state === "authenticated") {
        await handBack(attempt);
        return;
      }
      update({ view: { kind: "proof", context: loaded.context } });
    } catch {
      if (active && attempt === generation) terminal("unavailable");
    }
  }

  function idleProof(): DesktopReauthenticationContext | undefined {
    return active && snapshot.pending === undefined && snapshot.view.kind === "proof" ? snapshot.view.context : undefined;
  }

  return {
    getSnapshot: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
    start() {
      active = true;
      if (input.state === undefined) return Promise.resolve();
      // A Strict Mode restart reuses the first load so binding happens once.
      loading ??= load(generation);
      return loading;
    },
    stop() {
      active = false;
    },
    retry() {
      if (!active || snapshot.view.kind !== "unavailable" || input.state === undefined) return;
      const attempt = ++generation;
      update({ view: { kind: "checking" }, focusHeading: true });
      loading = load(attempt);
    },
    async provePassword(password) {
      const context = idleProof();
      if (context === undefined || context.provider !== undefined) return;
      const attempt = ++generation;
      update({ pending: "password", feedback: undefined, providerFailed: false });
      const result = await api.password(password);
      if (!active || attempt !== generation) return;
      switch (result.kind) {
        case "authenticated":
          await handBack(attempt);
          return;
        case "invalid_password":
        case "rate_limited":
        case "unavailable":
          update({ pending: undefined, feedback: result.kind });
          return;
        case "invalid":
          terminal("invalid");
      }
    },
    chooseProvider() {
      const context = idleProof();
      if (context?.provider === undefined) return;
      update({ pending: "provider", feedback: undefined, providerFailed: false });
      try {
        navigate(api.providerURL(context.provider.id, input.state!), false);
      } catch {
        update({ pending: undefined, feedback: "unavailable" });
      }
    },
    async cancel() {
      if (idleProof() === undefined) return;
      const attempt = ++generation;
      update({ pending: "cancel", feedback: undefined });
      const result = await api.cancel(input.state!);
      if (!active || attempt !== generation) return;
      if (result.kind === "unavailable") update({ pending: undefined, feedback: "unavailable" });
      else terminal(result.kind);
    },
  };
}

export function useDesktopReauthenticationJourney(
  input: DesktopReauthenticationInput,
): DesktopReauthenticationActions & { snapshot: DesktopReauthenticationSnapshot } {
  const ref = useRef<JourneyController>(undefined);
  ref.current ??= createDesktopReauthenticationJourney(input);
  const controller = ref.current;
  const snapshot = useSyncExternalStore(controller.subscribe, controller.getSnapshot);
  useEffect(() => {
    void controller.start();
    return controller.stop;
  }, [controller]);
  return { ...controller, snapshot };
}
