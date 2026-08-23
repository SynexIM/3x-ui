import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Card, Col, Descriptions, Form, Input, InputNumber, Row, Space, Table, Tag, Tooltip, message } from 'antd';
import { DeleteOutlined, PlusOutlined, SaveOutlined } from '@ant-design/icons';

import { useFairShareMutation, useFairShareQuery } from '@/api/queries/useFairShareQuery';
import {
  EMPTY_FAIR_SHARE_CLASS,
  EMPTY_FAIR_SHARE_FORM,
  burstCapNotAboveNormal,
  bitPerSecToMbps,
  exitAboveEnter,
  formToPayload,
  payloadToForm,
  type Blank,
  type FairShareClassForm,
  type FairShareForm,
} from '@/lib/nodes/fairshare';

// A disabled antd control swallows mouse events, so the Tooltip explaining why
// it is disabled never fires without a wrapper that still receives them.
function WhyDisabled({ reason, children }: { reason?: string; children: React.ReactNode }) {
  if (!reason) return <>{children}</>;
  return (
    <Tooltip title={reason}>
      <span style={{ display: 'block', pointerEvents: 'auto' }}>
        <div style={{ pointerEvents: 'none' }}>{children}</div>
      </span>
    </Tooltip>
  );
}

export default function NodeFairSharePanel() {
  const { t } = useTranslation();
  const { policy, status } = useFairShareQuery();
  const save = useFairShareMutation();
  const [form, setForm] = useState<FairShareForm>(EMPTY_FAIR_SHARE_FORM);

  const managed = policy.data?.declarativelyManaged === true;
  const readOnlyReason = managed ? t('pages.nodes.fairShare.managedTooltip') : undefined;

  useEffect(() => {
    if (!policy.data) return;
    setForm(payloadToForm(policy.data.policy));
  }, [policy.data]);

  const setField = <K extends keyof FairShareForm>(key: K, value: FairShareForm[K]) =>
    setForm((previous) => ({ ...previous, [key]: value }));

  const setClass = (index: number, patch: Partial<FairShareClassForm>) =>
    setForm((previous) => ({
      ...previous,
      classes: previous.classes.map((klass, i) => (i === index ? { ...klass, ...patch } : klass)),
    }));

  const exitTooHigh = exitAboveEnter(form);
  const badBurstClasses = useMemo(
    () => form.classes.filter(burstCapNotAboveNormal).map((klass) => klass.name || '-'),
    [form.classes],
  );
  const blocked = exitTooHigh || badBurstClasses.length > 0;

  async function onSave() {
    const msg = await save.mutateAsync(formToPayload(form));
    if (msg?.success) message.success(t('pages.nodes.fairShare.saved'));
  }

  const rateField = (
    label: string,
    tip: string,
    key: 'availMbps' | 'softFloorMbps' | 'hardFloorMbps',
  ) => (
    <Form.Item label={label} tooltip={tip} extra={tip}>
      <WhyDisabled reason={readOnlyReason}>
        <InputNumber
          value={form[key]}
          min={0}
          step={1}
          disabled={managed}
          addonAfter="Mbps"
          placeholder={t('pages.nodes.fairShare.blank')}
          style={{ width: '100%' }}
          onChange={(v) => setField(key, (v as Blank<number>) ?? null)}
        />
      </WhyDisabled>
    </Form.Item>
  );

  const countField = (
    label: string,
    tip: string,
    key: 'congestionEnterPercent' | 'congestionExitPercent' | 'congestionExitTicks',
    addon: string,
    max?: number,
    invalid?: string,
  ) => (
    <Form.Item
      label={label}
      tooltip={tip}
      extra={tip}
      validateStatus={invalid ? 'error' : undefined}
      help={invalid}
    >
      <WhyDisabled reason={readOnlyReason}>
        <InputNumber
          value={form[key]}
          min={0}
          max={max}
          step={1}
          disabled={managed}
          addonAfter={addon}
          placeholder={t('pages.nodes.fairShare.blank')}
          style={{ width: '100%' }}
          onChange={(v) => setField(key, (v as Blank<number>) ?? null)}
        />
      </WhyDisabled>
    </Form.Item>
  );

  const classColumn = (
    title: string,
    tip: string,
    key: 'weight' | 'normalCapMbps' | 'burstCapMbps' | 'burstCreditGB' | 'floorRatioPercent',
    addon: string,
    max?: number,
  ) => ({
    title: <Tooltip title={tip}><span>{title}</span></Tooltip>,
    dataIndex: key,
    width: 170,
    render: (_: unknown, klass: FairShareClassForm, index: number) => (
      <WhyDisabled reason={readOnlyReason}>
        <InputNumber
          value={klass[key]}
          min={0}
          max={max}
          step={1}
          disabled={managed}
          addonAfter={addon}
          placeholder={t('pages.nodes.fairShare.blank')}
          style={{ width: '100%' }}
          onChange={(v) => setClass(index, { [key]: (v as Blank<number>) ?? null })}
        />
      </WhyDisabled>
    ),
  });

  const live = status.data;
  const statusItems = [
    {
      key: 'congested',
      label: t('pages.nodes.fairShare.statusCongested'),
      children: (
        <Space direction="vertical" size={0}>
          <Tag color={live?.congested ? 'orange' : 'green'}>
            {live?.congested ? t('pages.nodes.fairShare.congestedYes') : t('pages.nodes.fairShare.congestedNo')}
          </Tag>
          <span className="hint">{t('pages.nodes.fairShare.statusCongestedHint')}</span>
        </Space>
      ),
      span: 2,
    },
    {
      key: 'rootCap',
      label: t('pages.nodes.fairShare.statusRootCap'),
      children: live?.rootCapBitPerSec
        ? `${bitPerSecToMbps(live.rootCapBitPerSec)} Mbps`
        : t('pages.nodes.fairShare.statusRootCapOff'),
    },
    {
      key: 'activeMembers',
      label: t('pages.nodes.fairShare.statusActiveMembers'),
      children: live?.activeMembers ?? 0,
    },
    {
      key: 'fillRounds',
      label: t('pages.nodes.fairShare.statusFillRounds'),
      children: live?.fillRounds ?? 0,
    },
    {
      key: 'fillTruncated',
      label: t('pages.nodes.fairShare.statusFillTruncated'),
      children: (
        <Tag color={live?.fillTruncated ? 'orange' : 'green'}>
          {live?.fillTruncated ? t('pages.nodes.fairShare.truncatedYes') : t('pages.nodes.fairShare.truncatedNo')}
        </Tag>
      ),
    },
    {
      key: 'fillUnresolvedMembers',
      label: t('pages.nodes.fairShare.statusFillUnresolved'),
      children: `${live?.fillUnresolvedMembers ?? 0} / ${live?.activeMembers ?? 0}`,
    },
    {
      key: 'fillTruncatedTicks',
      label: t('pages.nodes.fairShare.statusFillTruncatedTicks'),
      children: `${live?.fillTruncatedTicks ?? 0} s`,
    },
    {
      key: 'fillTruncatedTotalTicks',
      label: t('pages.nodes.fairShare.statusFillTruncatedTotal'),
      children: `${live?.fillTruncatedTotalTicks ?? 0} s`,
    },
  ];

  return (
    <Card
      style={{ marginBottom: 16 }}
      title={t('pages.nodes.fairShare.title')}
      extra={(
        <WhyDisabled reason={readOnlyReason}>
          <Button
            type="primary"
            icon={<SaveOutlined />}
            disabled={managed || blocked}
            loading={save.isPending}
            onClick={onSave}
          >
            {t('save')}
          </Button>
        </WhyDisabled>
      )}
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message={t('pages.nodes.fairShare.scope')}
        description={t('pages.nodes.fairShare.blankMeaning')}
      />
      {managed && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t('pages.nodes.fairShare.managedTitle')}
          description={t('pages.nodes.fairShare.managedDesc')}
        />
      )}

      <Card type="inner" size="small" title={t('pages.nodes.fairShare.statusSection')} style={{ marginBottom: 16 }}>
        {!live?.running && (
          <Alert type="warning" showIcon style={{ marginBottom: 12 }} message={t('pages.nodes.fairShare.statusStopped')} />
        )}
        <Descriptions bordered size="small" column={{ xs: 1, sm: 2, md: 2 }} items={statusItems} />
        <p className="hint" style={{ marginTop: 8 }}>{t('pages.nodes.fairShare.statusFillHint')}</p>
      </Card>

      <Form layout="vertical">
        <Card type="inner" size="small" title={t('pages.nodes.fairShare.nodeSection')} style={{ marginBottom: 16 }}>
          <Row gutter={16}>
            <Col xs={24} md={8}>
              {rateField(t('pages.nodes.fairShare.avail'), t('pages.nodes.fairShare.availBlank'), 'availMbps')}
            </Col>
            <Col xs={24} md={8}>
              {rateField(t('pages.nodes.fairShare.softFloor'), t('pages.nodes.fairShare.softFloorBlank'), 'softFloorMbps')}
            </Col>
            <Col xs={24} md={8}>
              {rateField(t('pages.nodes.fairShare.hardFloor'), t('pages.nodes.fairShare.hardFloorBlank'), 'hardFloorMbps')}
            </Col>
          </Row>
          <Row gutter={16}>
            <Col xs={24} md={8}>
              {countField(t('pages.nodes.fairShare.congestionEnter'), t('pages.nodes.fairShare.congestionEnterBlank'), 'congestionEnterPercent', '%', 100)}
            </Col>
            <Col xs={24} md={8}>
              {countField(
                t('pages.nodes.fairShare.congestionExit'),
                t('pages.nodes.fairShare.congestionExitBlank'),
                'congestionExitPercent',
                '%',
                100,
                exitTooHigh ? t('pages.nodes.fairShare.exitAboveEnter') : undefined,
              )}
            </Col>
            <Col xs={24} md={8}>
              {countField(t('pages.nodes.fairShare.congestionTicks'), t('pages.nodes.fairShare.congestionTicksBlank'), 'congestionExitTicks', 's')}
            </Col>
          </Row>
        </Card>

        <Card
          type="inner"
          size="small"
          title={t('pages.nodes.fairShare.classSection')}
          extra={(
            <WhyDisabled reason={readOnlyReason}>
              <Button
                size="small"
                icon={<PlusOutlined />}
                disabled={managed}
                onClick={() => setForm((p) => ({ ...p, classes: [...p.classes, { ...EMPTY_FAIR_SHARE_CLASS }] }))}
              >
                {t('pages.nodes.fairShare.addClass')}
              </Button>
            </WhyDisabled>
          )}
        >
          <Alert type="info" showIcon style={{ marginBottom: 12 }} message={t('pages.nodes.fairShare.classReplaceWhole')} />
          {badBurstClasses.length > 0 && (
            <Alert
              type="error"
              showIcon
              style={{ marginBottom: 12 }}
              message={t('pages.nodes.fairShare.burstNotAboveNormal', { classes: badBurstClasses.join(', ') })}
            />
          )}
          <Table<FairShareClassForm>
            size="small"
            rowKey={(_, index) => String(index)}
            dataSource={form.classes}
            pagination={false}
            scroll={{ x: 1100 }}
            locale={{ emptyText: t('pages.nodes.fairShare.noClasses') }}
            columns={[
              {
                title: <Tooltip title={t('pages.nodes.fairShare.classNameDesc')}><span>{t('pages.nodes.fairShare.className')}</span></Tooltip>,
                dataIndex: 'name',
                width: 180,
                render: (_: unknown, klass: FairShareClassForm, index: number) => (
                  <WhyDisabled reason={readOnlyReason}>
                    <Input
                      value={klass.name}
                      disabled={managed}
                      placeholder={t('pages.nodes.fairShare.classNameFallback')}
                      onChange={(e) => setClass(index, { name: e.target.value })}
                    />
                  </WhyDisabled>
                ),
              },
              classColumn(t('pages.nodes.fairShare.weight'), t('pages.nodes.fairShare.weightBlank'), 'weight', 'x'),
              classColumn(t('pages.nodes.fairShare.normalCap'), t('pages.nodes.fairShare.normalCapBlank'), 'normalCapMbps', 'Mbps'),
              classColumn(t('pages.nodes.fairShare.burstCap'), t('pages.nodes.fairShare.burstCapBlank'), 'burstCapMbps', 'Mbps'),
              classColumn(t('pages.nodes.fairShare.burstCredit'), t('pages.nodes.fairShare.burstCreditBlank'), 'burstCreditGB', 'GB'),
              classColumn(t('pages.nodes.fairShare.floorRatio'), t('pages.nodes.fairShare.floorRatioBlank'), 'floorRatioPercent', '%', 100),
              {
                title: '',
                dataIndex: 'remove',
                width: 56,
                fixed: 'right' as const,
                render: (_: unknown, __: FairShareClassForm, index: number) => (
                  <WhyDisabled reason={readOnlyReason}>
                    <Button
                      type="text"
                      danger
                      disabled={managed}
                      icon={<DeleteOutlined />}
                      aria-label={t('delete')}
                      onClick={() => setForm((p) => ({ ...p, classes: p.classes.filter((_, i) => i !== index) }))}
                    />
                  </WhyDisabled>
                ),
              },
            ]}
          />
        </Card>
      </Form>
    </Card>
  );
}
