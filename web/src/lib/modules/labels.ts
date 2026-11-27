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
	mysql: m.module_mysql,
	postgres: m.module_postgres,
	systemd_units: m.module_systemd_units,
	tls: m.module_tls,
	nginx: m.module_nginx,
	apache: m.module_apache,
	redis: m.module_redis,
	prometheus: m.module_prometheus,
	docker_socket: m.module_docker_socket
};

const fields: Record<string, Record<string, [label: Text, help?: Text]>> = {
	simulate: {
		// The test module: its schema's descriptions suffice.
		behavior: [m.module_simulate_behavior],
		value: [m.module_simulate_value],
		token: [m.module_simulate_token]
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
	mysql: {
		address: [m.module_mysql_address, m.module_mysql_address_help],
		socket: [m.module_mysql_socket, m.module_mysql_socket_help],
		user: [m.module_mysql_user, m.module_mysql_user_help],
		secret: [m.module_mysql_secret, m.module_mysql_secret_help],
		tls: [m.module_mysql_tls, m.module_mysql_tls_help],
		ca_file: [m.module_mysql_ca_file, m.module_mysql_ca_file_help]
	},
	postgres: {
		address: [m.module_postgres_address, m.module_postgres_address_help],
		socket: [m.module_postgres_socket, m.module_postgres_socket_help],
		role: [m.module_postgres_role, m.module_postgres_role_help],
		// The password: a field named so would make its i18n key look like a leaked one.
		secret: [m.module_postgres_secret, m.module_postgres_secret_help],
		database: [m.module_postgres_database, m.module_postgres_database_help],
		tls: [m.module_postgres_tls, m.module_postgres_tls_help],
		ca_file: [m.module_postgres_ca_file, m.module_postgres_ca_file_help],
		auth: [m.module_postgres_auth, m.module_postgres_auth_help]
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
	},
	apache: {
		url: [m.module_apache_url, m.module_apache_url_help],
		timeout: [m.module_apache_timeout, m.module_apache_timeout_help],
		tls_verify: [m.module_apache_tls_verify, m.module_apache_tls_verify_help],
		ca_file: [m.module_apache_ca_file, m.module_apache_ca_file_help]
	},
	redis: {
		address: [m.module_redis_address, m.module_redis_address_help],
		socket: [m.module_redis_socket, m.module_redis_socket_help],
		username: [m.module_redis_username, m.module_redis_username_help],
		auth: [m.module_redis_auth, m.module_redis_auth_help],
		tls: [m.module_redis_tls, m.module_redis_tls_help],
		ca_file: [m.module_redis_ca_file, m.module_redis_ca_file_help],
		timeout: [m.module_redis_timeout, m.module_redis_timeout_help]
	},
	prometheus: {
		urls: [m.module_prometheus_urls, m.module_prometheus_urls_help],
		metrics: [m.module_prometheus_metrics, m.module_prometheus_metrics_help],
		max_series: [m.module_prometheus_max_series, m.module_prometheus_max_series_help],
		timeout: [m.module_prometheus_timeout, m.module_prometheus_timeout_help],
		tls_verify: [m.module_prometheus_tls_verify, m.module_prometheus_tls_verify_help],
		ca_file: [m.module_prometheus_ca_file, m.module_prometheus_ca_file_help],
		bearer: [m.module_prometheus_bearer, m.module_prometheus_bearer_help]
	},
	docker_socket: {
		socket: [m.module_docker_socket_socket, m.module_docker_socket_socket_help],
		max_containers: [
			m.module_docker_socket_max_containers,
			m.module_docker_socket_max_containers_help
		],
		timeout: [m.module_docker_socket_timeout, m.module_docker_socket_timeout_help],
		logs: [m.module_docker_socket_logs, m.module_docker_socket_logs_help]
	}
};

/** Modules whose access weighs more than their form shows (O4: root-equivalent sockets). */
const warnings: Record<string, Text> = {
	docker_socket: m.module_docker_socket_warning
};

/** The warning of a module, shown above its form. */
export function moduleWarning(id: string): string | undefined {
	return warnings[id]?.();
}

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
