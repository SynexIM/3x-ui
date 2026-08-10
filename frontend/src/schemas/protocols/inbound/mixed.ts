import { z } from 'zod';

export const MixedAuthSchema = z.enum(['password', 'noauth']);
export type MixedAuth = z.infer<typeof MixedAuthSchema>;

export const MixedAccountSchema = z.object({
  user: z.string().min(1),
  pass: z.string().min(1),
});
export type MixedAccount = z.infer<typeof MixedAccountSchema>;

export const MixedClientSchema = z.object({
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
export type MixedClient = z.infer<typeof MixedClientSchema>;

const MixedInboundSettingsWireSchema = z.object({
  auth: MixedAuthSchema.default('password'),
  accounts: z.array(MixedAccountSchema).optional(),
  clients: z.array(MixedClientSchema).default([]),
  udp: z.boolean().default(false),
  ip: z.string().default('127.0.0.1'),
});

export const MixedInboundSettingsSchema = MixedInboundSettingsWireSchema.transform((settings) => {
  const clients = settings.clients.length > 0
    ? settings.clients
    : (settings.accounts ?? []).map((account) => MixedClientSchema.parse({
      email: account.user,
      password: account.pass,
    }));
  return {
    auth: clients.length > 0 ? 'password' as const : settings.auth,
    clients,
    udp: settings.udp,
    ip: settings.ip,
  };
});
export type MixedInboundSettings = z.infer<typeof MixedInboundSettingsSchema>;
