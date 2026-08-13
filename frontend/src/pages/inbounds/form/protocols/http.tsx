import { useTranslation } from 'react-i18next';
import { Switch } from 'antd';

import { FormField } from '@/components/form/rhf';

export default function HttpFields() {
  const { t } = useTranslation();
  return (
    <>
      <FormField
        name={['settings', 'allowTransparent']}
        label={t('pages.inbounds.form.allowTransparent')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
    </>
  );
}
