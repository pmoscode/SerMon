/**
 * Clients of the pages before a session: first setup and accepting an invitation. Apart from
 * #lib/api/client.js for the JavaScript budget (M1-29); tests replace single services with fakes.
 */
import { client } from './client.js';
import { InvitationService } from './gen/sermon/web/v1/invitation_pb.js';
import { SetupService } from './gen/sermon/web/v1/setup_pb.js';

export const onboardingApi = {
	setup: client(SetupService),
	invitation: client(InvitationService)
};
