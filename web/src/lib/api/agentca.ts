/**
 * Client of the agent CA (V1-19, superadmins): apart from #lib/api/client.js so that no other page
 * loads its descriptors (JavaScript budget); tests replace it with fakes.
 */
import { client } from './client.js';
import { AgentCAService } from './gen/sermon/web/v1/agent_ca_pb.js';

export const agentCAApi = client(AgentCAService);
