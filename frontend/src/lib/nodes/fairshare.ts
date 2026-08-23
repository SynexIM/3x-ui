// Node-level fair-share limits (FR-079e). The panel API speaks bit/s, the same
// unit as every other rate here; the admin UI is Mbps only (FR-079d).
//
// This module is the ONLY place a fair-share field changes unit. It delegates
// to the per-client rate-limit helpers rather than restating their arithmetic,
// so "Mbps" means one thing in the whole panel. The byte/s the xray proto wants
// is produced server-side, in one place, for the same reason.

import { bpsToRate, burstToBytes, bytesToBurst, rateToBps } from '@/lib/clients/rate-limit';

// A blank field is null: it means "not enabled", never "use a default".
export type Blank<T> = T | null;

export interface FairShareClassForm {
  name: string;
  weight: Blank<number>;
  normalCapMbps: Blank<number>;
  burstCapMbps: Blank<number>;
  burstCreditGB: Blank<number>;
  floorRatioPercent: Blank<number>;
}

export interface FairShareForm {
  availMbps: Blank<number>;
  softFloorMbps: Blank<number>;
  hardFloorMbps: Blank<number>;
  congestionEnterPercent: Blank<number>;
  congestionExitPercent: Blank<number>;
  congestionExitTicks: Blank<number>;
  classes: FairShareClassForm[];
}

export interface FairShareClassPayload {
  name: string;
  weight: number;
  normalCapBitPerSec: number;
  burstCapBitPerSec: number;
  burstCreditBytes: number;
  floorRatioPercent: number;
}

export interface FairSharePayload {
  availBitPerSec: number;
  softFloorBitPerSec: number;
  hardFloorBitPerSec: number;
  congestionEnterPercent: number;
  congestionExitPercent: number;
  congestionExitTicks: number;
  classes: FairShareClassPayload[];
}

export const EMPTY_FAIR_SHARE_FORM: FairShareForm = {
  availMbps: null,
  softFloorMbps: null,
  hardFloorMbps: null,
  congestionEnterPercent: null,
  congestionExitPercent: null,
  congestionExitTicks: null,
  classes: [],
};

export const EMPTY_FAIR_SHARE_CLASS: FairShareClassForm = {
  name: '',
  weight: null,
  normalCapMbps: null,
  burstCapMbps: null,
  burstCreditGB: null,
  floorRatioPercent: null,
};

export function mbpsToBitPerSec(mbps: Blank<number>): number {
  return rateToBps(Number(mbps) || 0, 'Mbps');
}

export function bitPerSecToMbps(bitPerSec: number | undefined): Blank<number> {
  const mbps = bpsToRate(Number(bitPerSec) || 0, 'Mbps');
  return mbps > 0 ? mbps : null;
}

export function gigabytesToBytes(gb: Blank<number>): number {
  return burstToBytes(Number(gb) || 0, 'GB');
}

export function bytesToGigabytes(bytes: number | undefined): Blank<number> {
  const gb = bytesToBurst(Number(bytes) || 0, 'GB');
  return gb > 0 ? gb : null;
}

function toCount(value: Blank<number>): number {
  const count = Number(value);
  return Number.isFinite(count) && count > 0 ? Math.round(count) : 0;
}

function fromCount(value: number | undefined): Blank<number> {
  const count = Number(value) || 0;
  return count > 0 ? count : null;
}

export function formToPayload(form: FairShareForm): FairSharePayload {
  return {
    availBitPerSec: mbpsToBitPerSec(form.availMbps),
    softFloorBitPerSec: mbpsToBitPerSec(form.softFloorMbps),
    hardFloorBitPerSec: mbpsToBitPerSec(form.hardFloorMbps),
    congestionEnterPercent: toCount(form.congestionEnterPercent),
    congestionExitPercent: toCount(form.congestionExitPercent),
    congestionExitTicks: toCount(form.congestionExitTicks),
    classes: form.classes
      .filter((klass) => klass.name.trim() !== '')
      .map((klass) => ({
        name: klass.name.trim(),
        weight: toCount(klass.weight),
        normalCapBitPerSec: mbpsToBitPerSec(klass.normalCapMbps),
        burstCapBitPerSec: mbpsToBitPerSec(klass.burstCapMbps),
        burstCreditBytes: gigabytesToBytes(klass.burstCreditGB),
        floorRatioPercent: toCount(klass.floorRatioPercent),
      })),
  };
}

export function payloadToForm(payload: Partial<FairSharePayload> | undefined): FairShareForm {
  if (!payload) return EMPTY_FAIR_SHARE_FORM;
  return {
    availMbps: bitPerSecToMbps(payload.availBitPerSec),
    softFloorMbps: bitPerSecToMbps(payload.softFloorBitPerSec),
    hardFloorMbps: bitPerSecToMbps(payload.hardFloorBitPerSec),
    congestionEnterPercent: fromCount(payload.congestionEnterPercent),
    congestionExitPercent: fromCount(payload.congestionExitPercent),
    congestionExitTicks: fromCount(payload.congestionExitTicks),
    classes: (payload.classes ?? []).map((klass) => ({
      name: klass.name ?? '',
      weight: fromCount(klass.weight),
      normalCapMbps: bitPerSecToMbps(klass.normalCapBitPerSec),
      burstCapMbps: bitPerSecToMbps(klass.burstCapBitPerSec),
      burstCreditGB: bytesToGigabytes(klass.burstCreditBytes),
      floorRatioPercent: fromCount(klass.floorRatioPercent),
    })),
  };
}

// The two shapes the core silently ignores. Saying so before submit beats a
// saved setting that quietly does nothing.
export function burstCapNotAboveNormal(klass: FairShareClassForm): boolean {
  const burst = mbpsToBitPerSec(klass.burstCapMbps);
  return burst > 0 && burst <= mbpsToBitPerSec(klass.normalCapMbps);
}

export function exitAboveEnter(form: FairShareForm): boolean {
  return toCount(form.congestionExitPercent) > toCount(form.congestionEnterPercent);
}
