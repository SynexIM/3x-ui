import { z } from 'zod';

export const ClientRateLimitShape = {
  bandwidth_bps: z.number().int().min(0).default(0),
  committed_bps: z.number().int().min(0).default(0),
  committed_burst_bytes: z.number().int().min(0).default(0),
  rateUnit: z.enum(['Mbps', 'Kbps', 'MB/s', 'KB/s']).default('Mbps'),
  burstUnit: z.enum(['MB', 'GB']).default('MB'),
};
