import { z } from 'zod';
import { ClientRateLimitShape } from './client-limits';

// HTTP proxy inbound — a classic forward proxy with global panel clients.
export const HttpAccountSchema = z.object({
  user: z.string().min(1),
  pass: z.string().min(1),
});
export type HttpAccount = z.infer<typeof HttpAccountSchema>;

export const HttpClientSchema = z.object({
  ...ClientRateLimitShape,
  password: z.string().min(1),
  email: z.string().min(1),
  limitIp: z.number().int().min(0).default(0),
  totalGB: z.number().int().min(0).default(0),
  expiryTime: z.number().int().default(0),
  enable: z.boolean().default(true),
  tgId: z.union([z.number(), z.string()]).transform((v) => Number(v) || 0).default(0),
  subId: z.string().default(''),
  group: z.string().default(''),
  comment: z.string().default(''),
  reset: z.number().int().min(0).default(0),
  created_at: z.number().int().optional(),
  updated_at: z.number().int().optional(),
});
export type HttpClient = z.infer<typeof HttpClientSchema>;

const HttpInboundSettingsWireSchema = z.object({
  accounts: z.array(HttpAccountSchema).optional(),
  clients: z.array(HttpClientSchema).default([]),
  allowTransparent: z.boolean().default(false),
});

export const HttpInboundSettingsSchema = HttpInboundSettingsWireSchema.transform((settings) => ({
  clients: settings.clients.length > 0
    ? settings.clients
    : (settings.accounts ?? []).map((account) => HttpClientSchema.parse({
      email: account.user,
      password: account.pass,
    })),
  allowTransparent: settings.allowTransparent,
}));
export type HttpInboundSettings = z.infer<typeof HttpInboundSettingsSchema>;
