import { useSyncExternalStore } from "react";

import {
  sharedPublicAccessDiscovery,
  type PublicAccessDiscoveryResult,
  type SharedPublicAccessDiscovery,
} from "./PublicAccessDiscovery";

/**
 * The installation's minimum new-password length from the shared, validated
 * discovery document the hosted shell loads. Undefined while discovery loads,
 * after failure, or when the server omits it; pages then show their hint
 * without a length rather than guessing one.
 */
export function usePasswordMinimumLength(
  discovery: SharedPublicAccessDiscovery = sharedPublicAccessDiscovery,
): number | undefined {
  const result = useSyncExternalStore(
    discovery.subscribe,
    discovery.current,
    discovery.current,
  );
  return passwordMinimumLengthFrom(result);
}

export function passwordMinimumLengthFrom(
  result: PublicAccessDiscoveryResult | undefined,
): number | undefined {
  return result?.kind === "ready"
    ? result.discovery.password_minimum_length
    : undefined;
}

/** Formats a whole count in the document locale for password hints. */
export function formatPasswordLength(length: number): string {
  const locale =
    typeof document === "undefined" || document.documentElement.lang === ""
      ? undefined
      : document.documentElement.lang;
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(
    length,
  );
}
