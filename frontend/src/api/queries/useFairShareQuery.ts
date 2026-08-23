import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';
import type { FairSharePayload } from '@/lib/nodes/fairshare';
import type { FairSharePolicyView, FairShareStatusView } from '@/generated/types';

// The scheduler's state is what an operator stares at while tuning, so it has
// to move on its own; the policy only changes when someone saves it.
const STATUS_REFETCH_MS = 5000;

export function useFairShareQuery() {
  const policy = useQuery({
    queryKey: keys.nodes.fairShare(),
    queryFn: async () => {
      const msg = await HttpUtil.get<FairSharePolicyView>('/panel/api/nodes/fairshare', undefined, { silent: true });
      if (!msg?.success) throw new Error(msg?.msg || 'fairshare');
      return msg.obj;
    },
  });

  const status = useQuery({
    queryKey: keys.nodes.fairShareStatus(),
    queryFn: async () => {
      const msg = await HttpUtil.get<FairShareStatusView>('/panel/api/nodes/fairshare/status', undefined, { silent: true });
      if (!msg?.success) throw new Error(msg?.msg || 'fairshare/status');
      return msg.obj;
    },
    refetchInterval: STATUS_REFETCH_MS,
  });

  return { policy, status };
}

export function useFairShareMutation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (payload: FairSharePayload) => HttpUtil.post('/panel/api/nodes/fairshare', payload),
    onSuccess: (msg) => {
      if (!msg?.success) return;
      queryClient.invalidateQueries({ queryKey: keys.nodes.fairShare() });
      queryClient.invalidateQueries({ queryKey: keys.nodes.fairShareStatus() });
    },
  });
}
