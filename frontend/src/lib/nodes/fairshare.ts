// Node-level fair-share limits (FR-079e). The panel API speaks bit/s, the same
// unit as every other rate here; the admin UI is Mbps only (FR-079d).
//
// This module is the ONLY place a fair-share field changes unit. It delegates
// to the per-client rate-limit helpers rather than restating their arithmetic,
// so "Mbps" means one thing in the whole panel. The byte/s the xray proto wants
// is produced server-side, in one place, for the same reason.

import { bpsToRate, rateToBps } from '@/lib/clients/rate-limit';

// A blank field is null: it means "not enabled", never "use a default".
export type Blank<T> = T | null;

export interface FairShareClassForm {
  name: string;
  weight: Blank<number>;
  floorMbps: Blank<number>;
  uploadReservedMbps: Blank<number>;
  downloadReservedMbps: Blank<number>;
  heavyWindowSeconds: Blank<number>;
  heavyPercent: Blank<number>;
}

export interface FairShareForm {
  availMbps: Blank<number>;
  congestionEnterPercent: Blank<number>;
  congestionExitPercent: Blank<number>;
  congestionExitTicks: Blank<number>;
  classes: FairShareClassForm[];
}

export interface FairShareClassPayload {
  name: string;
  weight: number;
  floorBitPerSec: number;
  uploadReservedBitPerSec: number;
  downloadReservedBitPerSec: number;
  heavyWindowSeconds: number;
  heavyPercent: number;
}

export interface FairSharePayload {
  availBitPerSec: number;
  congestionEnterPercent: number;
  congestionExitPercent: number;
  congestionExitTicks: number;
  classes: FairShareClassPayload[];
}

export const EMPTY_FAIR_SHARE_FORM: FairShareForm = {
  availMbps: null,
  congestionEnterPercent: null,
  congestionExitPercent: null,
  congestionExitTicks: null,
  classes: [],
};

export const EMPTY_FAIR_SHARE_CLASS: FairShareClassForm = {
  name: '',
  weight: null,
  floorMbps: null,
  uploadReservedMbps: null,
  downloadReservedMbps: null,
  heavyWindowSeconds: null,
  heavyPercent: null,
};

export function mbpsToBitPerSec(mbps: Blank<number>): number {
  return rateToBps(Number(mbps) || 0, 'Mbps');
}

export function bitPerSecToMbps(bitPerSec: number | undefined): Blank<number> {
  const mbps = bpsToRate(Number(bitPerSec) || 0, 'Mbps');
  return mbps > 0 ? mbps : null;
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
    congestionEnterPercent: toCount(form.congestionEnterPercent),
    congestionExitPercent: toCount(form.congestionExitPercent),
    congestionExitTicks: toCount(form.congestionExitTicks),
    classes: form.classes
      .filter((klass) => klass.name.trim() !== '')
      .map((klass) => ({
        name: klass.name.trim(),
        weight: toCount(klass.weight),
        floorBitPerSec: mbpsToBitPerSec(klass.floorMbps),
        uploadReservedBitPerSec: mbpsToBitPerSec(klass.uploadReservedMbps),
        downloadReservedBitPerSec: mbpsToBitPerSec(klass.downloadReservedMbps),
        heavyWindowSeconds: toCount(klass.heavyWindowSeconds),
        heavyPercent: toCount(klass.heavyPercent),
      })),
  };
}

export function payloadToForm(payload: Partial<FairSharePayload> | undefined): FairShareForm {
  if (!payload) return EMPTY_FAIR_SHARE_FORM;
  return {
    availMbps: bitPerSecToMbps(payload.availBitPerSec),
    congestionEnterPercent: fromCount(payload.congestionEnterPercent),
    congestionExitPercent: fromCount(payload.congestionExitPercent),
    congestionExitTicks: fromCount(payload.congestionExitTicks),
    classes: (payload.classes ?? []).map((klass) => ({
      name: klass.name ?? '',
      weight: fromCount(klass.weight),
      floorMbps: bitPerSecToMbps(klass.floorBitPerSec),
      uploadReservedMbps: bitPerSecToMbps(klass.uploadReservedBitPerSec),
      downloadReservedMbps: bitPerSecToMbps(klass.downloadReservedBitPerSec),
      heavyWindowSeconds: fromCount(klass.heavyWindowSeconds),
      heavyPercent: fromCount(klass.heavyPercent),
    })),
  };
}

// Shapes the core would silently ignore. Saying so before submit beats a saved
// setting that quietly does nothing.
export function heavyHalfSet(klass: FairShareClassForm): boolean {
  return toCount(klass.heavyWindowSeconds) > 0 !== toCount(klass.heavyPercent) > 0;
}

export function exitAboveEnter(form: FairShareForm): boolean {
  return toCount(form.congestionExitPercent) > toCount(form.congestionEnterPercent);
}
