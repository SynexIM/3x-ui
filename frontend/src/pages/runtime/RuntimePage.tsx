import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Alert, Card, Col, Descriptions, Row, Spin, Table, Tag } from 'antd';

import { HttpUtil, SizeFormatter } from '@/utils';
import type { XrayRuntimeView, RuntimeInbound, RuntimeRule } from '@/generated/types';

const sizeFormat = (value: number | null | undefined) => SizeFormatter.sizeFormat(value);

/*
 * What the node is actually running, read straight from the core.
 *
 * Read-only on purpose. Everywhere else in the panel shows what was saved;
 * this page shows what took effect, and the gap between the two is the thing
 * you come here to find. Editing belongs on the pages that own each object.
 */
export default function RuntimePage() {
  const { t } = useTranslation();
  const query = useQuery({
    queryKey: ['runtime', 'snapshot'],
    queryFn: async (): Promise<XrayRuntimeView> => {
      const msg = await HttpUtil.get('/panel/api/runtime');
      if (!msg?.success) throw new Error(msg?.msg || 'failed');
      return msg.obj as XrayRuntimeView;
    },
    refetchInterval: 5000,
  });

  const view = query.data;

  const inboundColumns = [
    { title: t('pages.xray.outbound.tag'), dataIndex: 'tag', key: 'tag' },
    { title: t('protocol'), dataIndex: 'protocol', key: 'protocol', render: (v: string) => <Tag color="purple">{v}</Tag> },
    { title: t('pages.inbounds.port'), dataIndex: 'port', key: 'port' },
    {
      title: t('pages.runtime.loaded'),
      key: 'loaded',
      render: (_: unknown, row: RuntimeInbound) => (
        row.loaded
          ? <Tag color="green">{t('pages.runtime.loadedYes')}</Tag>
          : <Tag color={row.enabled ? 'red' : 'default'}>{row.enabled ? t('pages.runtime.loadedNo') : t('disabled')}</Tag>
      ),
    },
    { title: t('menu.clients'), dataIndex: 'clients', key: 'clients' },
    {
      title: t('pages.inbounds.traffic'),
      key: 'traffic',
      render: (_: unknown, row: RuntimeInbound) => `${sizeFormat(row.up)} / ${sizeFormat(row.down)}`,
    },
  ];

  return (
    <Spin spinning={query.isLoading}>
      <Alert type="info" showIcon style={{ marginBottom: 16 }} message={t('pages.runtime.readOnly')} />

      {view?.runtimeError && (
        <Alert type="warning" showIcon style={{ marginBottom: 16 }} message={view.runtimeError} />
      )}

      <Card size="small" style={{ marginBottom: 16 }} title={t('pages.runtime.core')}>
        <Descriptions size="small" column={{ xs: 1, sm: 2, lg: 4 }}>
          <Descriptions.Item label={t('status')}>
            {view?.running
              ? <Tag color="green">{t('pages.runtime.running')}</Tag>
              : <Tag color="red">{t('pages.runtime.stopped')}</Tag>}
          </Descriptions.Item>
          <Descriptions.Item label="PID">{view?.pid || '—'}</Descriptions.Item>
          <Descriptions.Item label={t('pages.runtime.version')}>{view?.version || '—'}</Descriptions.Item>
          <Descriptions.Item label={t('pages.runtime.uptime')}>
            {view ? `${Math.floor((view.uptimeSeconds ?? 0) / 60)} min` : '—'}
          </Descriptions.Item>
          <Descriptions.Item label={t('pages.runtime.online')}>{view?.onlineClients ?? 0}</Descriptions.Item>
          <Descriptions.Item label={t('pages.inbounds.traffic')}>
            {view ? `${sizeFormat(view.totalUp)} / ${sizeFormat(view.totalDown)}` : '—'}
          </Descriptions.Item>
        </Descriptions>
      </Card>

      <Card size="small" style={{ marginBottom: 16 }} title={t('pages.xray.Inbounds')}>
        <Table
          size="small"
          rowKey="tag"
          pagination={false}
          scroll={{ x: true }}
          dataSource={view?.inbounds ?? []}
          columns={inboundColumns}
        />
      </Card>

      <Row gutter={16}>
        <Col xs={24} lg={10}>
          <Card size="small" title={t('pages.xray.Outbounds')}>
            <Table
              size="small"
              rowKey="tag"
              pagination={false}
              dataSource={view?.outbounds ?? []}
              columns={[{ title: t('pages.xray.outbound.tag'), dataIndex: 'tag', key: 'tag' }] as never}
              locale={{ emptyText: t('pages.runtime.noneLoaded') }}
            />
          </Card>
        </Col>
        <Col xs={24} lg={14}>
          <Card size="small" title={t('pages.xray.Routings')}>
            <Table
              size="small"
              rowKey={(row: RuntimeRule) => `${row.ruleTag}|${row.outboundTag}`}
              pagination={{ pageSize: 20, size: 'small' }}
              dataSource={view?.rules ?? []}
              columns={[
                { title: t('pages.xray.ruleForm.ruleTag'), dataIndex: 'ruleTag', key: 'ruleTag', render: (v: string) => v || <span style={{ opacity: 0.5 }}>—</span> },
                { title: t('pages.xray.ruleForm.outboundTag'), dataIndex: 'outboundTag', key: 'outboundTag' },
              ] as never}
              locale={{ emptyText: t('pages.runtime.noneLoaded') }}
            />
          </Card>
        </Col>
      </Row>
    </Spin>
  );
}
