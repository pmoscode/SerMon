/**
 * The names of the modules and the labels of their form fields (ADR 0039): i18n texts
 * module_<id> and module_<id>_<field>, the schema's title as the fallback for a module of a newer
 * agent. A new module adds its entries here (skill sermon-add-module); a test checks every schema
 * of the build. Loaded with the module views only (JavaScript budget).
 */
import { m } from '#lib/paraglide/messages.js';
import type { Field } from './schema.js';

type Text = () => string;

const names: Record<string, Text> = {
	simulate: m.module_simulate,
	http_check: m.module_http_check,
	tcp_check: m.module_tcp_check,
	smart: m.module_smart,
	docker: m.module_docker,
	systemd_units: m.module_systemd_units,
	tls: m.module_tls,
	nginx: m.module_nginx
};

const fields: Record<string, Record<string, [label: Text, help?: Text]>> = {
	simulate: {
		behavior: [m.module_simulate_behavior, m.module_simulate_behavior_help],
		value: [m.module_simulate_value, m.module_simulate_value_help],
		token: [m.module_simulate_token, m.module_simulate_token_help]
	},
	http_check: {
		urls: [m.module_http_check_urls, m.module_http_check_urls_help],
		expect_status: [m.module_http_check_expect_status, m.module_http_check_expect_status_help],
		timeout: [m.module_http_check_timeout, m.module_http_check_timeout_help],
		follow_redirects: [
			m.module_http_check_follow_redirects,
			m.module_http_check_follow_redirects_help
		],
		tls_verify: [m.module_http_check_tls_verify, m.module_http_check_tls_verify_help],
		ca_file: [m.module_http_check_ca_file, m.module_http_check_ca_file_help]
	},
	tcp_check: {
		targets: [m.module_tcp_check_targets, m.module_tcp_check_targets_help],
		timeout: [m.module_tcp_check_timeout, m.module_tcp_check_timeout_help]
	},
	smart: {
		exclude: [m.module_smart_exclude, m.module_smart_exclude_help]
	},
	docker: {
		max_containers: [m.module_docker_max_containers, m.module_docker_max_containers_help]
	},
	systemd_units: {
		units: [m.module_systemd_units_units, m.module_systemd_units_units_help]
	},
	tls: {
		files: [m.module_tls_files, m.module_tls_files_help],
		endpoints: [m.module_tls_endpoints, m.module_tls_endpoints_help],
		server_name: [m.module_tls_server_name, m.module_tls_server_name_help],
		ca_file: [m.module_tls_ca_file, m.module_tls_ca_file_help],
		timeout: [m.module_tls_timeout, m.module_tls_timeout_help]
	},
	nginx: {
		url: [m.module_nginx_url, m.module_nginx_url_help],
		timeout: [m.module_nginx_timeout, m.module_nginx_timeout_help],
		tls_verify: [m.module_nginx_tls_verify, m.module_nginx_tls_verify_help],
		ca_file: [m.module_nginx_ca_file, m.module_nginx_ca_file_help]
	}
};

/** The display name of a module. */
export function moduleLabel(id: string, title?: string): string {
	return names[id]?.() ?? title ?? id;
}

/** The label of a field. */
export function fieldLabel(id: string, field: Field): string {
	return fields[id]?.[field.name]?.[0]() ?? field.title ?? field.name;
}

/** The help line of a field: the i18n text, else the schema's description. */
export function fieldHelp(id: string, field: Field): string | undefined {
	return fields[id]?.[field.name]?.[1]?.() ?? field.description;
}
