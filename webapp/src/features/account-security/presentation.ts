import { useSyncExternalStore } from "react";

import { sharedPublicAccessDiscovery } from "../../auth/PublicAccessDiscovery";

/** A localized whole-minute duration such as “10 minutes” or “1 minute”. */
export function formatMinutes(minutes: number): string {
  return new Intl.NumberFormat(documentLocale(), {
    style: "unit", unit: "minute", unitDisplay: "long", maximumFractionDigits: 0,
  }).format(minutes);
}

/** Whole minutes left until `deadline`, rounded up; zero once it has passed. */
export function minutesUntil(deadline: string, now = Date.now()): number {
  return Math.max(0, Math.ceil((Date.parse(deadline) - now) / 60_000));
}

/** Groups a base32 setup key in fours so it can be read and typed. */
export function groupSetupKey(secret: string): string {
  return secret.match(/.{1,4}/g)?.join(" ") ?? secret;
}

/**
 * Display name of a public external provider from the shared discovery
 * document the hosted shell already loads. Undefined until discovery resolves
 * or when the ID is not published.
 */
export function useProviderName(providerID: string | undefined): string | undefined {
  const result = useSyncExternalStore(
    sharedPublicAccessDiscovery.subscribe,
    sharedPublicAccessDiscovery.current,
    sharedPublicAccessDiscovery.current,
  );
  if (providerID === undefined || result?.kind !== "ready") return undefined;
  const name = result.discovery.providers.find((provider) => provider.id === providerID)?.display_name.trim();
  return name === "" ? undefined : name;
}

function documentLocale(): string | undefined {
  return typeof document === "undefined" || document.documentElement.lang === "" ? undefined : document.documentElement.lang;
}
