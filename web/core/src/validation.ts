/**
 * Syntactic validation for Halp identifiers.
 *
 * INVARIANT: These functions are strictly local syntactic checks.
 * Under NO circumstances should this module query the server to check
 * if a handle or alias exists, as that would serve as an enumeration oracle
 * (docs/DISCOVERY.md, AGENTS.md invariant 7).
 */

// Crockford base32 alphabet (lowercase), excluding i, l, o, u
const CROCKFORD_BASE32_REGEX = /^e[0-9abcdefghjkmnpqrstvwxyz]{12}$/i;
const ALIAS_REGEX = /^@[a-z0-9_-]{2,32}$/i;

/**
 * Checks whether a string is syntactically shaped like a handle:
 * 13 characters, starts with 'e', followed by 12 Crockford base32 characters.
 */
export function isHandleShaped(handle: string): boolean {
  if (!handle || typeof handle !== 'string') return false;
  return CROCKFORD_BASE32_REGEX.test(handle.trim());
}

/**
 * Checks whether a string is syntactically shaped like an alias:
 * Starts with '@' followed by 2 to 32 alphanumeric, underscore, or hyphen characters.
 */
export function isAliasShaped(alias: string): boolean {
  if (!alias || typeof alias !== 'string') return false;
  return ALIAS_REGEX.test(alias.trim());
}

/**
 * Checks whether an input string is shaped like a 16-digit account key (whitespace ignored).
 */
export function isAccountKeyShaped(key: string): boolean {
  if (!key || typeof key !== 'string') return false;
  const digits = key.replace(/\s+/g, '');
  return /^\d{16}$/.test(digits);
}

/**
 * Removes all whitespace and validates that the result is 16 decimal digits.
 * Throws an error if invalid.
 */
export function normalizeAccountKey(key: string): string {
  const digits = key.replace(/\s+/g, '');
  if (!/^\d{16}$/.test(digits)) {
    throw new Error('Account key must consist of exactly 16 digits');
  }
  return digits;
}

/**
 * Formats a 16-digit account key into four groups of four digits separated by spaces.
 * e.g. "6421 8830 5197 4462"
 */
export function formatAccountKey(key: string): string {
  const digits = key.replace(/\s+/g, '');
  if (digits.length !== 16) {
    return key;
  }
  return `${digits.slice(0, 4)} ${digits.slice(4, 8)} ${digits.slice(8, 12)} ${digits.slice(12, 16)}`;
}
