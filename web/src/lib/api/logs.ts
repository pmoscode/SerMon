/**
 * The client of the log explorer (KONZEPT §10.4). It lives apart from #lib/api/client.js so that
 * pages without logs don't load its descriptors (JavaScript budget); tests replace it with fakes.
 */
import { client } from './client.js';
import { LogService } from './gen/sermon/web/v1/log_pb.js';

export const logsApi = { logs: client(LogService) };
