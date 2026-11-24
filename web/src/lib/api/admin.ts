/**
 * Clients of the administration area (members, groups, enrollment tokens, log settings,
 * two-factor settings, security policy, organizations of the instance). They live apart from #lib/api/client.js so that pages without administration
 * don't load their descriptors (JavaScript budget, M1-29); tests replace single services with fakes.
 */
import { client } from './client.js';
import { AccessService } from './gen/sermon/web/v1/access_pb.js';
import { EnrollmentTokenService } from './gen/sermon/web/v1/enrollment_token_pb.js';
import { LogSettingsService } from './gen/sermon/web/v1/log_settings_pb.js';
import { OidcMappingService } from './gen/sermon/web/v1/oidc_mapping_pb.js';
import { OrganizationService } from './gen/sermon/web/v1/organization_pb.js';
import { SecurityPolicyService, TwoFactorService } from './gen/sermon/web/v1/two_factor_pb.js';

export const adminApi = {
	access: client(AccessService),
	enrollment: client(EnrollmentTokenService),
	logSettings: client(LogSettingsService),
	oidcMappings: client(OidcMappingService),
	organization: client(OrganizationService),
	twoFactor: client(TwoFactorService),
	securityPolicy: client(SecurityPolicyService)
};
