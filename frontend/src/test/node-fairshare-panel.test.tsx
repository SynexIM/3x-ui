import { describe, expect, it, vi, beforeEach } from 'vitest';
import { waitFor } from '@testing-library/react';
import type { QueryClient } from '@tanstack/react-query';

import NodeFairSharePanel from '@/pages/nodes/NodeFairSharePanel';
import { renderWithProviders, makeTestQueryClient } from './test-utils';
import { keys } from '@/api/queryKeys';
import { HttpUtil, Msg } from '@/utils';
import type { FairSharePolicy, FairSharePolicyView, FairShareStatusView } from '@/generated/types';

const OFF: FairSharePolicy = {
  availBitPerSec: 0,
  softFloorBitPerSec: 0,
  hardFloorBitPerSec: 0,
  congestionEnterPercent: 0,
  congestionExitPercent: 0,
  congestionExitTicks: 0,
  classes: [],
};

const STATUS: FairShareStatusView = {
  running: true,
  rootCapBitPerSec: 0,
  congested: false,
  activeMembers: 0,
  fillTruncated: false,
  fillUnresolvedMembers: 0,
  fillTruncatedTicks: 0,
  fillTruncatedTotalTicks: 0,
  fillRounds: 0,
};

// Renders the panel and waits until both queries are really in the cache;
// asserting earlier only inspects the component's own empty state.
async function renderLoaded(view: FairSharePolicyView, status: FairShareStatusView = STATUS) {
  vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) =>
    (url.endsWith('/status') ? new Msg(true, '', status) : new Msg(true, '', view)) as never,
  );
  const queryClient: QueryClient = makeTestQueryClient();
  renderWithProviders(<NodeFairSharePanel />, { queryClient });
  await waitFor(() => {
    expect(queryClient.getQueryData(keys.nodes.fairShare())).toBeTruthy();
    expect(queryClient.getQueryData(keys.nodes.fairShareStatus())).toBeTruthy();
  });
}

function numberInputs(): HTMLInputElement[] {
  return Array.from(document.querySelectorAll('.ant-input-number-input')) as HTMLInputElement[];
}

function saveButton(): HTMLButtonElement {
  return document.querySelector('.ant-card-extra .ant-btn-primary') as HTMLButtonElement;
}

describe('NodeFairSharePanel', () => {
  beforeEach(() => vi.restoreAllMocks());

  // A blank field must arrive blank. Prefilling anything here would turn
  // "nothing configured" into a bandwidth limit nobody asked for.
  it('shows every unset field as empty, not as zero', async () => {
    await renderLoaded({ declarativelyManaged: false, policy: OFF });
    expect(numberInputs().length).toBeGreaterThan(0);
    for (const input of numberInputs()) {
      expect(input.value).toBe('');
    }
  });

  // "Looks clickable, tells you no only after you click" is exactly the
  // dishonesty this panel exists to avoid: the server refuses the write.
  it('disables every control while the control plane owns the node', async () => {
    await renderLoaded({ declarativelyManaged: true, policy: { ...OFF, availBitPerSec: 1_000_000_000 } });
    await waitFor(() => expect(document.body.textContent).toContain('Managed by the control plane'));
    for (const input of numberInputs()) {
      expect(input.disabled).toBe(true);
    }
    expect(saveButton().disabled).toBe(true);
    // The greyed-out field still has to show the value that is really in force.
    expect(numberInputs().some((input) => input.value === '1000')).toBe(true);
  });

  it('leaves the controls editable on a standalone install', async () => {
    await renderLoaded({ declarativelyManaged: false, policy: { ...OFF, availBitPerSec: 1_000_000_000 } });
    await waitFor(() => expect(numberInputs().some((input) => input.value === '1000')).toBe(true));
    for (const input of numberInputs()) {
      expect(input.disabled).toBe(false);
    }
    expect(saveButton().disabled).toBe(false);
    expect(document.body.textContent).not.toContain('Managed by the control plane');
  });

  // congested is the first thing to check when tuning appears to do nothing.
  it('says out loud that nothing is being shaped when the node is not congested', async () => {
    await renderLoaded({ declarativelyManaged: false, policy: OFF });
    expect(document.body.textContent).toContain('nothing is being shaped');
    expect(document.body.textContent).toContain('Outside fair mode the core slows nobody down');
  });
});
