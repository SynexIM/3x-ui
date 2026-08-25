import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Tag, Tooltip } from 'antd';
import { RobotOutlined } from '@ant-design/icons';

import { keys } from '@/api/queryKeys';
import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { DefaultsPayloadSchema } from '@/schemas/defaults';

/*
 * Managed namespaces: how a person and an automation share one panel.
 *
 * An API token declares the tag/email prefixes it owns. Objects carrying one of
 * those prefixes are marked here so an operator can see, before editing, that a
 * later reconciliation may put the automated value back. Nothing is disabled —
 * a panel that locks itself the moment automation touches it is no use in the
 * incident where you actually need it.
 */

export function isManagedName(name: string | undefined, namespaces: readonly string[]): boolean {
  if (!name) return false;
  return namespaces.some((prefix) => prefix && name.startsWith(prefix));
}

/** The prefixes some automation owns on this panel, from the settings payload. */
export function useManagedNamespaces(): string[] {
  const query = useQuery({
    queryKey: keys.settings.defaults(),
    queryFn: async () => {
      const msg = await HttpUtil.post('/panel/api/setting/defaultSettings', undefined, { silent: true });
      if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch settings');
      return parseMsg(msg, DefaultsPayloadSchema, 'setting/defaultSettings').obj ?? {};
    },
    staleTime: Infinity,
  });
  return query.data?.managedNamespaces ?? [];
}

/** Marks one object as owned by an automation. Renders nothing otherwise. */
export function ManagedTag({ name, namespaces }: { name: string | undefined; namespaces: readonly string[] }) {
  const { t } = useTranslation();
  if (!isManagedName(name, namespaces)) return null;
  return (
    <Tooltip title={t('managedByAutomationDesc')}>
      <Tag color="warning" icon={<RobotOutlined />} style={{ marginInlineStart: 6, marginInlineEnd: 0 }}>
        {t('managedByAutomation')}
      </Tag>
    </Tooltip>
  );
}
