export interface RuleRow {
  key: number;
  enabled?: boolean;
  ruleTag?: string;
  domain?: string;
  ip?: string;
  port?: string;
  sourcePort?: string;
  vlessRoute?: string;
  network?: string;
  sourceIP?: string;
  user?: string;
  inboundTag?: string;
  protocol?: string;
  attrs?: string;
  outboundTag?: string;
  balancerTag?: string;
}
