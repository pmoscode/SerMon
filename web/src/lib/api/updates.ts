/**
 * Clients of the updates (M4-09 to M4-15): reports, policies, updates and reboots by hand. Apart
 * from #lib/api/client.js, so that pages without updates don't load their descriptors.
 */
import { client } from './client.js';
import { UpdateService } from './gen/sermon/web/v1/update_pb.js';
import { UpdatePolicyService } from './gen/sermon/web/v1/update_policy_pb.js';

export const updatesApi = {
	updates: client(UpdateService),
	policies: client(UpdatePolicyService)
};
