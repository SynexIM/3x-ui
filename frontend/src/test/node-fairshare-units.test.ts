import { describe, expect, it } from 'vitest';

import {
  EMPTY_FAIR_SHARE_FORM,
  bitPerSecToMbps,
  burstCapNotAboveNormal,
  bytesToGigabytes,
  exitAboveEnter,
  formToPayload,
  gigabytesToBytes,
  mbpsToBitPerSec,
  payloadToForm,
  type FairShareForm,
} from '@/lib/nodes/fairshare';

const FULL: FairShareForm = {
  availMbps: 1000,
  softFloorMbps: 0.5,
  hardFloorMbps: 0.25,
  congestionEnterPercent: 85,
  congestionExitPercent: 70,
  congestionExitTicks: 5,
  classes: [
    {
      name: 'live',
      weight: 3,
      normalCapMbps: 20,
      burstCapMbps: 50,
      burstCreditGB: 1,
      floorRatioPercent: 20,
    },
  ],
};

describe('node fair-share units', () => {
  /*
   * The whole point of this file. The fair-share proto counts bytes/s while the
   * panel API counts bits/s, and both spell the suffix "bps": get it wrong and
   * every limit is off by exactly 8. The UI is Mbps only, and this module is the
   * one place a fair-share field changes unit, so these are the only numbers
   * that can be wrong.
   */
  it('turns Mbps into bit/s, never into byte/s', () => {
    expect(mbpsToBitPerSec(1)).toBe(1_000_000);
    expect(mbpsToBitPerSec(1)).not.toBe(125_000); // the /8 mistake
    expect(mbpsToBitPerSec(1)).not.toBe(8_000_000); // the *8 mistake
    expect(mbpsToBitPerSec(1000)).toBe(1_000_000_000);
  });

  it('sends every rate field through the same conversion', () => {
    const payload = formToPayload(FULL);
    expect(payload.availBitPerSec).toBe(1_000_000_000);
    expect(payload.softFloorBitPerSec).toBe(500_000);
    expect(payload.hardFloorBitPerSec).toBe(250_000);
    expect(payload.classes[0].normalCapBitPerSec).toBe(20_000_000);
    expect(payload.classes[0].burstCapBitPerSec).toBe(50_000_000);
    // Credit is a size, not a rate: GB, and no factor of 8 anywhere near it.
    expect(payload.classes[0].burstCreditBytes).toBe(1_000_000_000);
  });

  it('passes percentages and tick counts through untouched', () => {
    const payload = formToPayload(FULL);
    expect(payload.congestionEnterPercent).toBe(85);
    expect(payload.congestionExitPercent).toBe(70);
    expect(payload.congestionExitTicks).toBe(5);
    expect(payload.classes[0].weight).toBe(3);
    expect(payload.classes[0].floorRatioPercent).toBe(20);
  });

  it('round-trips a saved policy back to the numbers that were typed', () => {
    expect(payloadToForm(formToPayload(FULL))).toEqual(FULL);
  });

  it('treats a blank field as off, and shows it back as blank', () => {
    const payload = formToPayload(EMPTY_FAIR_SHARE_FORM);
    expect(payload).toEqual({
      availBitPerSec: 0,
      softFloorBitPerSec: 0,
      hardFloorBitPerSec: 0,
      congestionEnterPercent: 0,
      congestionExitPercent: 0,
      congestionExitTicks: 0,
      classes: [],
    });
    expect(payloadToForm(payload)).toEqual(EMPTY_FAIR_SHARE_FORM);
    expect(bitPerSecToMbps(0)).toBeNull();
    expect(bytesToGigabytes(0)).toBeNull();
    expect(gigabytesToBytes(null)).toBe(0);
  });

  it('drops class rows that were added but never named', () => {
    const payload = formToPayload({
      ...EMPTY_FAIR_SHARE_FORM,
      classes: [{ name: '  ', weight: 2, normalCapMbps: 10, burstCapMbps: null, burstCreditGB: null, floorRatioPercent: null }],
    });
    expect(payload.classes).toEqual([]);
  });

  it('flags the two shapes the core silently ignores', () => {
    expect(burstCapNotAboveNormal({ name: 'a', weight: null, normalCapMbps: 20, burstCapMbps: 20, burstCreditGB: null, floorRatioPercent: null })).toBe(true);
    expect(burstCapNotAboveNormal({ name: 'a', weight: null, normalCapMbps: 20, burstCapMbps: 21, burstCreditGB: null, floorRatioPercent: null })).toBe(false);
    expect(burstCapNotAboveNormal({ name: 'a', weight: null, normalCapMbps: 20, burstCapMbps: null, burstCreditGB: null, floorRatioPercent: null })).toBe(false);
    expect(exitAboveEnter({ ...EMPTY_FAIR_SHARE_FORM, congestionEnterPercent: 80, congestionExitPercent: 90 })).toBe(true);
    expect(exitAboveEnter({ ...EMPTY_FAIR_SHARE_FORM, congestionEnterPercent: 80, congestionExitPercent: 70 })).toBe(false);
  });
});
