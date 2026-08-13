// Per-client speed limits. The panel stores bit/s and bytes (what xray reads);
// operators think in "100 兆", so the unit they picked is stored alongside.

export const RATE_UNITS = ['Mbps', 'Kbps', 'MB/s', 'KB/s'] as const;
export const BURST_UNITS = ['MB', 'GB'] as const;

export type RateUnit = (typeof RATE_UNITS)[number];
export type BurstUnit = (typeof BURST_UNITS)[number];

export const DEFAULT_RATE_UNIT: RateUnit = 'Mbps';
export const DEFAULT_BURST_UNIT: BurstUnit = 'GB';

// Decimal, not binary: a "100 Mbps" line is 100_000_000 bit/s everywhere in
// telecom billing, and xray's limiter takes the same number.
const BITS_PER_UNIT: Record<RateUnit, number> = {
  Mbps: 1_000_000,
  Kbps: 1_000,
  'MB/s': 8_000_000,
  'KB/s': 8_000,
};

const BYTES_PER_UNIT: Record<BurstUnit, number> = {
  MB: 1_000_000,
  GB: 1_000_000_000,
};

export function isRateUnit(value: unknown): value is RateUnit {
  return typeof value === 'string' && (RATE_UNITS as readonly string[]).includes(value);
}

export function isBurstUnit(value: unknown): value is BurstUnit {
  return typeof value === 'string' && (BURST_UNITS as readonly string[]).includes(value);
}

export function normalizeRateUnit(value: unknown): RateUnit {
  return isRateUnit(value) ? value : DEFAULT_RATE_UNIT;
}

export function normalizeBurstUnit(value: unknown): BurstUnit {
  return isBurstUnit(value) ? value : DEFAULT_BURST_UNIT;
}

export function rateToBps(value: number, unit: RateUnit): number {
  if (!Number.isFinite(value) || value <= 0) return 0;
  return Math.round(value * BITS_PER_UNIT[unit]);
}

export function bpsToRate(bps: number, unit: RateUnit): number {
  if (!Number.isFinite(bps) || bps <= 0) return 0;
  return roundForDisplay(bps / BITS_PER_UNIT[unit]);
}

export function burstToBytes(value: number, unit: BurstUnit): number {
  if (!Number.isFinite(value) || value <= 0) return 0;
  return Math.round(value * BYTES_PER_UNIT[unit]);
}

export function bytesToBurst(bytes: number, unit: BurstUnit): number {
  if (!Number.isFinite(bytes) || bytes <= 0) return 0;
  return roundForDisplay(bytes / BYTES_PER_UNIT[unit]);
}

// Six decimals keeps 1 Kbps exact in MB/s (0.000125) without ever showing the
// float noise that a plain division leaves behind.
function roundForDisplay(value: number): number {
  return Math.round(value * 1e6) / 1e6;
}

// A committed rate above the peak is ignored by xray-core, so the form must
// refuse it rather than save a setting that quietly does nothing.
export function committedExceedsPeak(peakBps: number, committedBps: number): boolean {
  return peakBps > 0 && committedBps > 0 && committedBps > peakBps;
}
