import { describe, expect, it } from 'vitest';

import {
  RATE_UNITS,
  BURST_UNITS,
  bpsToRate,
  burstToBytes,
  bytesToBurst,
  committedExceedsPeak,
  normalizeBurstUnit,
  normalizeRateUnit,
  rateToBps,
  type BurstUnit,
  type RateUnit,
} from '@/lib/clients/rate-limit';
import { ClientFormRefinedSchema, ClientCreateFormSchema } from '@/schemas/client';

describe('rate limit units', () => {
  it('converts what operators actually say', () => {
    expect(rateToBps(100, 'Mbps')).toBe(100_000_000);
    expect(rateToBps(512, 'Kbps')).toBe(512_000);
    expect(rateToBps(12.5, 'MB/s')).toBe(100_000_000);
    expect(rateToBps(64, 'KB/s')).toBe(512_000);
    expect(burstToBytes(50, 'MB')).toBe(50_000_000);
    expect(burstToBytes(2, 'GB')).toBe(2_000_000_000);
  });

  // Reopening the form must show the number that was typed, not a converted one.
  it('round-trips every unit without drift', () => {
    for (const unit of RATE_UNITS satisfies readonly RateUnit[]) {
      for (const value of [1, 5, 100, 1000, 12.5]) {
        expect(bpsToRate(rateToBps(value, unit), unit)).toBe(value);
      }
    }
    for (const unit of BURST_UNITS satisfies readonly BurstUnit[]) {
      for (const value of [1, 50, 2.5]) {
        expect(bytesToBurst(burstToBytes(value, unit), unit)).toBe(value);
      }
    }
  });

  it('treats blank and non-positive input as unlimited', () => {
    expect(rateToBps(0, 'Mbps')).toBe(0);
    expect(rateToBps(Number.NaN, 'Mbps')).toBe(0);
    expect(rateToBps(-5, 'Mbps')).toBe(0);
    expect(bpsToRate(0, 'Mbps')).toBe(0);
    expect(bytesToBurst(0, 'GB')).toBe(0);
  });

  it('falls back to a sane unit for unknown stored values', () => {
    expect(normalizeRateUnit(undefined)).toBe('Mbps');
    expect(normalizeRateUnit('Gbps')).toBe('Mbps');
    expect(normalizeRateUnit('KB/s')).toBe('KB/s');
    expect(normalizeBurstUnit('TB')).toBe('GB');
    expect(normalizeBurstUnit('MB')).toBe('MB');
  });

  it('only flags a committed rate that is really above the peak', () => {
    expect(committedExceedsPeak(100, 200)).toBe(true);
    expect(committedExceedsPeak(100, 100)).toBe(false);
    expect(committedExceedsPeak(0, 200)).toBe(false); // no peak = no ceiling to exceed
    expect(committedExceedsPeak(100, 0)).toBe(false);
  });
});

const baseForm = {
  email: 'line-042',
  subId: 'sub',
  uuid: '',
  password: '',
  auth: '',
  flow: '',
  security: 'auto',
  reverseTag: '',
  totalGB: 0,
  delayedStart: false,
  delayedDays: 0,
  reset: 0,
  limitIp: 0,
  tgId: 0,
  group: '',
  comment: '',
  enable: true,
  inboundIds: [1],
  peakRate: 100,
  committedRate: 20,
  rateUnit: 'Mbps' as const,
  burstSize: 50,
  burstUnit: 'MB' as const,
};

describe('client form rate limit validation', () => {
  it('accepts a committed rate below the peak', () => {
    expect(ClientFormRefinedSchema.safeParse(baseForm).success).toBe(true);
    expect(ClientCreateFormSchema.safeParse(baseForm).success).toBe(true);
  });

  // Xray silently ignores CIR >= PIR, so the panel must stop it here or the
  // operator walks away believing the line is configured.
  it('rejects a committed rate above the peak on both create and edit', () => {
    const bad = { ...baseForm, committedRate: 200 };
    for (const schema of [ClientCreateFormSchema, ClientFormRefinedSchema]) {
      const parsed = schema.safeParse(bad);
      expect(parsed.success).toBe(false);
      if (!parsed.success) {
        expect(parsed.error.issues[0]?.message).toBe('pages.clients.committedAbovePeak');
      }
    }
  });

  it('allows blank limits (unlimited) and a bare committed rate', () => {
    expect(ClientCreateFormSchema.safeParse({ ...baseForm, peakRate: 0, committedRate: 0, burstSize: 0 }).success).toBe(true);
    expect(ClientCreateFormSchema.safeParse({ ...baseForm, peakRate: 0, committedRate: 20 }).success).toBe(true);
  });
});
