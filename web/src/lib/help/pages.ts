/** The help topic of each page (route id), opened by the help button and the "?" key. */
export const pageHelp: Record<string, string> = {
	'/(app)/fleet': 'pages/fleet',
	'/(app)/alerts': 'pages/alerts',
	'/(app)/notifications': 'pages/notifications',
	'/(app)/settings/security': 'pages/security',
	'/(app)/nodes/[id]': 'pages/node',
	'/(app)/nodes/add': 'pages/add_node',
	'/(app)/admin': 'pages/admin',
	'/(app)/organizations': 'pages/organizations'
};
