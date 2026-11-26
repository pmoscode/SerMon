/**
 * The client of the modules (ADR 0039, M5-07), apart from #lib/api/client.js: only the module
 * views load its descriptors (JavaScript budget); tests replace it with a fake.
 */
import { client } from './client.js';
import { ModuleService } from './gen/sermon/web/v1/module_pb.js';

export const modulesApi = { modules: client(ModuleService) };
