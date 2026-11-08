import type { components } from "../../api/generated/schema";
import { apiClient } from "../../api/client";
import { readProblemValue } from "../../api/problem";
import { isWireTimestamp } from "../../api/timestamp";
import { validDesktopApprovalRedirect } from "../desktop-authorization/DesktopAuthorizationApi";

type ServerContext = components["schemas"]["DesktopReauthenticationContextResponse"];
export type DesktopReauthenticationProvider = components["schemas"]["ExternalAuthenticationProviderResponse"];

/** The safe projection of one bound Desktop reauthentication. */
export interface DesktopReauthenticationContext {
  state: "bound" | "authenticated";
  /** The Desktop Session account's display name. */
  name: string;
  /** Present only when the Desktop Session signed in through a provider. */
  provider?: DesktopReauthenticationProvider;
  expiresAt: string;
}

export interface DesktopReauthenticationProof {
  browserProof: string;
  handle: string;
  state: string;
}

export type DesktopReauthenticationFailure = { kind: "invalid" } | { kind: "unavailable" };

export type DesktopReauthenticationContextResult =
  | { kind: "ready"; context: DesktopReauthenticationContext }
  | DesktopReauthenticationFailure;

export type DesktopReauthenticationPasswordResult =
  | { kind: "authenticated"; context: DesktopReauthenticationContext }
  | { kind: "invalid_password" }
  | { kind: "rate_limited" }
  | DesktopReauthenticationFailure;

export type DesktopReauthenticationApprovalResult =
  | { kind: "approved"; redirectURL: string }
  | DesktopReauthenticationFailure;

const base = "/api/v1/auth/desktop/reauthentications";

export async function bindDesktopReauthentication(
  proof: DesktopReauthenticationProof,
): Promise<{ kind: "bound" } | DesktopReauthenticationFailure> {
  try {
    const { error, response } = await apiClient.POST("/api/v1/auth/desktop/reauthentications/bind", {
      body: { handle: proof.handle, browser_proof: proof.browserProof, state: proof.state },
    });
    return response.status === 204 ? { kind: "bound" } : failure(error);
  } catch {
    return { kind: "unavailable" };
  }
}

export async function requestDesktopReauthenticationContext(): Promise<DesktopReauthenticationContextResult> {
  try {
    const { data, error, response } = await apiClient.GET("/api/v1/auth/desktop/reauthentications/context");
    if (response.status !== 200) return failure(error);
    return validServerContext(data) ? { kind: "ready", context: projectContext(data) } : { kind: "unavailable" };
  } catch {
    return { kind: "unavailable" };
  }
}

export async function proveDesktopReauthenticationPassword(password: string): Promise<DesktopReauthenticationPasswordResult> {
  try {
    const { data, error, response } = await apiClient.POST("/api/v1/auth/desktop/reauthentications/authenticate/password", {
      body: { password },
    });
    if (response.status === 200 && validServerContext(data) && data.state === "authenticated") {
      return { kind: "authenticated", context: projectContext(data) };
    }
    const code = readProblemValue(error)?.code;
    if (code === "authentication.credentials_invalid") return { kind: "invalid_password" };
    if (code === "authentication.rate_limited") return { kind: "rate_limited" };
    return failureFromCode(code);
  } catch {
    return { kind: "unavailable" };
  }
}

/** The same-tab provider entry; the provider returns to this page. */
export function desktopReauthenticationProviderURL(providerID: string, state: string): string {
  const parameters = new URLSearchParams({ state });
  return `${base}/authenticate/providers/${encodeURIComponent(providerID)}/login?${parameters.toString()}`;
}

export async function approveDesktopReauthentication(state: string): Promise<DesktopReauthenticationApprovalResult> {
  try {
    const { data, error, response } = await apiClient.POST("/api/v1/auth/desktop/reauthentications/approve", {
      body: { state },
    });
    if (response.status === 200 && isWireTimestamp(data?.expires_at) && validDesktopApprovalRedirect(data?.redirect_url, state)) {
      return { kind: "approved", redirectURL: data.redirect_url };
    }
    return failure(error);
  } catch {
    return { kind: "unavailable" };
  }
}

export async function cancelDesktopReauthentication(state: string): Promise<{ kind: "canceled" } | DesktopReauthenticationFailure> {
  try {
    const { error, response } = await apiClient.POST("/api/v1/auth/desktop/reauthentications/cancel", { body: { state } });
    return response.status === 204 ? { kind: "canceled" } : failure(error);
  } catch {
    return { kind: "unavailable" };
  }
}

function projectContext(value: ServerContext): DesktopReauthenticationContext {
  return {
    state: value.state,
    name: value.display_name,
    expiresAt: value.expires_at,
    ...(value.provider === undefined ? {} : { provider: value.provider }),
  };
}

function validServerContext(value: unknown): value is ServerContext {
  if (!isRecord(value) || (value.state !== "bound" && value.state !== "authenticated") ||
    !boundedString(value.display_name, 256) || !isWireTimestamp(value.expires_at)) {
    return false;
  }
  if (value.authentication_method === "password") return value.provider === undefined;
  return value.authentication_method === "external" && isRecord(value.provider) &&
    typeof value.provider.id === "string" && /^[a-z0-9][a-z0-9._-]{0,63}$/.test(value.provider.id) &&
    boundedString(value.provider.display_name, 128, true) && boundedString(value.provider.type, 64, true);
}

function boundedString(value: unknown, maximum: number, required = false): value is string {
  return typeof value === "string" && (!required || value.trim() !== "") && [...value].length <= maximum;
}

function failure(error: unknown): DesktopReauthenticationFailure {
  return failureFromCode(readProblemValue(error)?.code);
}

function failureFromCode(code: string | undefined): DesktopReauthenticationFailure {
  return code === "authentication.desktop_reauthentication.invalid" ||
    code === "authentication.desktop_reauthentication.denied" ||
    code === "authentication.reauthentication_method_required" ||
    code === "request.invalid"
    ? { kind: "invalid" }
    : { kind: "unavailable" };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
