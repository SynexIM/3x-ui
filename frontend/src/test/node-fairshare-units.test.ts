import { describe, expect, it } from 'vitest';

import {
  EMPTY_FAIR_SHARE_FORM,
  bitPerSecToMbps,
  exitAboveEnter,
  formToPayload,
  heavyHalfSet,
  mbpsToBitPerSec,
  payloadToForm,
  type FairShareClassForm,
  type FairShareForm,
} from '@/lib/nodes/fairshare';

const CLASS: FairShareClassForm = {
  name: 'c1',
  weight: 3,
  floorMbps: 5,
  uploadReservedMbps: 2,
  downloadReservedMbps: 20,
  heavyWindowSeconds: 900,
  heavyPercent: 80,
};

const FULL: FairShareForm = {
  availMbps: 1000,
  congestionEnterPercent: 85,
  congestionExitPercent: 70,
  congestionExitTicks: 5,
  classes: [CLASS],
};

describe('node fair-share units', () => {
  // The fair-share proto counts bytes/s, the panel API bits/s, and both spell
  // the suffix "bps": get it wrong and every limit is off by exactly 8.
  it('turns Mbps into bit/s, never into byte/s', () => {
    expect(mbpsToBitPerSec(1)).toBe(1_000_000);
    expect(mbpsToBitPerSec(1000)).toBe(1_000_000_000);
  });

  it('sends rates through the conversion and seconds/percents untouched', () => {
    const payload = formToPayload(FULL);
    expect(payload.availBitPerSec).toBe(1_000_000_000);
    expect(payload.classes[0]).toEqual({
      name: 'c1',
      weight: 3,
      floorBitPerSec: 5_000_000,
      uploadReservedBitPerSec: 2_000_000,
      downloadReservedBitPerSec: 20_000_000,
      heavyWindowSeconds: 900,
      heavyPercent: 80,
    });
    expect(payloadToForm(payload)).toEqual(FULL);
  });

  it('treats a blank field as off, and shows it back as blank', () => {
    const payload = formToPayload(EMPTY_FAIR_SHARE_FORM);
    expect(payload).toEqual({
      availBitPerSec: 0,
      congestionEnterPercent: 0,
      congestionExitPercent: 0,
      congestionExitTicks: 0,
      classes: [],
    });
    expect(payloadToForm(payload)).toEqual(EMPTY_FAIR_SHARE_FORM);
    expect(bitPerSecToMbps(0)).toBeNull();
  });

  it('drops class rows that were added but never named', () => {
    expect(
      formToPayload({ ...EMPTY_FAIR_SHARE_FORM, classes: [{ ...CLASS, name: '  ' }] }).classes,
    ).toEqual([]);
  });

  it('flags the shapes the core silently ignores', () => {
    expect(heavyHalfSet({ ...CLASS, heavyPercent: null })).toBe(true);
    expect(heavyHalfSet(CLASS)).toBe(false);
    expect(heavyHalfSet({ ...CLASS, heavyPercent: null, heavyWindowSeconds: null })).toBe(false);
    expect(
      exitAboveEnter({
        ...EMPTY_FAIR_SHARE_FORM,
        congestionEnterPercent: 80,
        congestionExitPercent: 90,
      }),
    ).toBe(true);
    expect(
      exitAboveEnter({
        ...EMPTY_FAIR_SHARE_FORM,
        congestionEnterPercent: 80,
        congestionExitPercent: 70,
      }),
    ).toBe(false);
  });
});
