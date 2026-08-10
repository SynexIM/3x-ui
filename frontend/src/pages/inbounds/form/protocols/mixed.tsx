import { useTranslation } from 'react-i18next';
import { Input, Select, Switch } from 'antd';

import { FormField } from '@/components/form/rhf';

export default function MixedFields({ mixedUdpOn }: { mixedUdpOn: boolean }) {
  const { t } = useTranslation();
  return (
    <>
      <FormField name={['settings', 'auth']} label={t('pages.inbounds.info.auth')}>
        <Select
          options={[
            { value: 'noauth', label: 'noauth' },
            { value: 'password', label: 'password' },
          ]}
        />
      </FormField>
      <FormField
        name={['settings', 'udp']}
        label="UDP"
        valueProp="checked"
      >
        <Switch />
      </FormField>
      {mixedUdpOn && (
        <FormField name={['settings', 'ip']} label="UDP IP">
          <Input />
        </FormField>
      )}
    </>
  );
}
