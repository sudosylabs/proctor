const dateTimeOptions: Intl.DateTimeFormatOptions = {
  weekday: "long",
  day: "numeric",
  month: "long",
  year: "numeric",
  hour: "numeric",
  minute: "2-digit",
  timeZoneName: "short",
};

/**
 * Formats a validated wire timestamp as a full date and time in the viewer's
 * locale and time zone, naming the zone so the moment is unambiguous.
 * `locale` and `timeZone` exist for deterministic tests; pages omit them.
 */
export function formatDateTime(
  timestamp: string,
  locale?: string,
  timeZone?: string,
): string {
  return new Intl.DateTimeFormat(locale, {
    ...dateTimeOptions,
    ...(timeZone === undefined ? {} : { timeZone }),
  }).format(new Date(timestamp));
}

const dateOptions: Intl.DateTimeFormatOptions = {
  day: "numeric",
  month: "long",
  year: "numeric",
};

/**
 * Formats a validated wire timestamp as a calendar date in the viewer's locale
 * and time zone, for facts where the time of day adds nothing (such as when a
 * password last changed). `locale` and `timeZone` exist for deterministic tests.
 */
export function formatDate(
  timestamp: string,
  locale?: string,
  timeZone?: string,
): string {
  return new Intl.DateTimeFormat(locale, {
    ...dateOptions,
    ...(timeZone === undefined ? {} : { timeZone }),
  }).format(new Date(timestamp));
}
