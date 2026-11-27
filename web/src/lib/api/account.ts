/**
 * The client of the own account and of the superadmins' account deletions (V1-17). It lives apart
 * from #lib/api/client.js so that other pages don't load its descriptors (JavaScript budget); tests
 * replace it with fakes.
 */
import { client } from './client.js';
import { AccountAdminService, AccountService } from './gen/sermon/web/v1/account_pb.js';

export const accountApi = { account: client(AccountService), admin: client(AccountAdminService) };
