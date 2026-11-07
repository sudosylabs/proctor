import {
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useSyncExternalStore,
} from "react";

import {
  sharedPublicAccessDiscovery,
  type PublicAccessDiscoveryResult,
  type SharedPublicAccessDiscovery,
} from "./PublicAccessDiscovery";

/**
 * Safe public Institution presentation for hosted-page chrome. Every field is
 * optional: it stays empty while discovery loads and when discovery fails or
 * omits the value.
 */
export interface HostedInstitution {
  displayName?: string;
  supportAddress?: string;
}

const HostedInstitutionContext = createContext<HostedInstitution>({});

export interface HostedInstitutionProviderProps {
  children: ReactNode;
  discovery?: SharedPublicAccessDiscovery;
}

export function HostedInstitutionProvider({
  children,
  discovery = sharedPublicAccessDiscovery,
}: HostedInstitutionProviderProps) {
  const result = useSyncExternalStore(
    discovery.subscribe,
    discovery.current,
    discovery.current,
  );

  useEffect(() => {
    void discovery.load(window.location.origin);
  }, [discovery]);

  const institution = useMemo(() => hostedInstitutionFrom(result), [result]);

  return (
    <HostedInstitutionContext.Provider value={institution}>
      {children}
    </HostedInstitutionContext.Provider>
  );
}

export function useHostedInstitution(): HostedInstitution {
  return useContext(HostedInstitutionContext);
}

export function hostedInstitutionFrom(
  result: PublicAccessDiscoveryResult | undefined,
): HostedInstitution {
  if (result?.kind !== "ready") {
    return {};
  }
  const displayName = result.discovery.institution?.display_name.trim();
  const supportAddress = result.discovery.support_address?.trim();
  return {
    ...(displayName === undefined || displayName === "" ? {} : { displayName }),
    ...(supportAddress !== undefined && isMailtoSafeAddress(supportAddress)
      ? { supportAddress }
      : {}),
  };
}

/**
 * A bounded plain mailbox using only characters that need no escaping in a
 * `mailto:` link. Any other published value is dropped, so the page offers
 * no support link rather than an unsafe or surprising one.
 */
function isMailtoSafeAddress(value: string): boolean {
  return (
    value.length <= 254 &&
    /^[A-Za-z0-9._+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$/.test(value)
  );
}
