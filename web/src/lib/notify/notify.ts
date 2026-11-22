/** Names and messages of the notification settings (M2-18). */
import { ConnectError } from '@connectrpc/connect';
import { Code, codeOf } from '#lib/api/client.js';
import { needsStepUp } from '#lib/stores/stepup.svelte.js';
import { m } from '#lib/paraglide/messages.js';

const typeNames: Record<string, () => string> = {
	email: () => m.channel_type_email(),
	webhook: () => m.channel_type_webhook(),
	ntfy: () => 'ntfy',
	slack: () => 'Slack',
	teams: () => 'Microsoft Teams',
	discord: () => 'Discord',
	mattermost: () => 'Mattermost',
	telegram: () => 'Telegram',
	gotify: () => 'Gotify',
	pushover: () => 'Pushover'
};

/** The display name of a channel type; its key for unknown types. */
export function channelTypeName(type: string): string {
	return typeNames[type]?.() ?? type;
}

const fieldNames: Record<string, () => string> = {
	url: () => m.channel_field_url(),
	to: () => m.channel_field_to(),
	secret: () => m.channel_field_secret(),
	server: () => m.channel_field_server(),
	topic: () => m.channel_field_topic(),
	token: () => m.channel_field_token(),
	chat: () => m.channel_field_chat(),
	thread: () => m.channel_field_thread(),
	user: () => m.channel_field_user(),
	device: () => m.channel_field_device()
};

/** The label of a configuration field ("<type>.<name>" in the API); its name if unknown. */
export function channelFieldName(name: string): string {
	return fieldNames[name]?.() ?? name;
}

/**
 * The message of a failed call; empty for a cancelled step-up. Invalid input names the field in
 * the server's words (never a value, never a secret).
 */
export function notifyError(error: unknown): string {
	if (needsStepUp(error)) return '';
	switch (codeOf(error)) {
		case Code.PermissionDenied:
			return m.admin_error_forbidden();
		case Code.NotFound:
			return m.admin_error_gone();
		case Code.InvalidArgument:
			return m.notify_error_invalid({ detail: ConnectError.from(error).rawMessage });
		case Code.ResourceExhausted:
			// The organization's limit of channels (M2-38), or too many tests in a minute.
			return ConnectError.from(error).rawMessage.includes('channels')
				? m.notify_error_too_many_channels()
				: m.notify_error_too_many_tests();
		default:
			return m.error_unavailable();
	}
}
