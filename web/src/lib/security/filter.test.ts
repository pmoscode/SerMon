import { describe, expect, it } from 'vitest';
import {
	emptySecurityFilter,
	MAX_RANGE,
	parseSecurityFilter,
	securityFilterParams,
	securityRange,
	validNetwork,
	type SecurityFilter
} from './filter.js';

const node = '0193f5a0-0000-7000-8000-000000000001';
const parse = (query: string) => parseSecurityFilter(new URLSearchParams(query));

describe('security filter in the URL', () => {
	it('round-trips every field', () => {
		const f: SecurityFilter = {
			preset: '24h',
			from: Date.parse('2026-11-20T10:00:00Z'),
			to: Date.parse('2026-11-20T12:00:00Z'),
			nodes: [node],
			kinds: ['ssh.login.failed', 'ssh.login.invalid_user'],
			user: 'root',
			network: '192.0.2.0/24',
			newOnly: true
		};
		const query = securityFilterParams(f).toString();
		expect(query).toBe(
			'from=2026-11-20T10%3A00%3A00.000Z&to=2026-11-20T12%3A00%3A00.000Z&node=' +
				node +
				'&kind=ssh.login.failed&kind=ssh.login.invalid_user&user=root&net=192.0.2.0%2F24&new=1'
		);
		expect(parse(query)).toEqual(f);
	});

	it('leaves the defaults out', () => {
		expect(securityFilterParams(emptySecurityFilter()).toString()).toBe('');
		expect(securityFilterParams({ ...emptySecurityFilter(), preset: '7d' }).toString()).toBe(
			'range=7d'
		);
		expect(parse('')).toEqual(emptySecurityFilter());
	});

	it('keeps the empty user name an attacker tried', () => {
		const f = parse('user=');
		expect(f.user).toBe('');
		expect(securityFilterParams(f).toString()).toBe('user=');
	});

	it('drops invalid values', () => {
		const f = parse(
			'range=2y&from=x&to=2026-11-20T12:00:00Z&node=nope&kind=ssh.root&kind=system.reboot&net=evil.example&new=yes'
		);
		expect(f).toEqual({ ...emptySecurityFilter(), kinds: ['system.reboot'] });
		expect(parse('user=' + 'ü'.repeat(100)).user).toHaveLength(64);
		expect(parse('from=2026-11-20T12:00:00Z&to=2026-11-20T10:00:00Z').from).toBeUndefined();
	});

	it('checks networks roughly', () => {
		expect(validNetwork('192.0.2.7')).toBe(true);
		expect(validNetwork('2001:db8::/32')).toBe(true);
		expect(validNetwork('')).toBe(true);
		expect(validNetwork('example.org')).toBe(false);
		expect(validNetwork('192.0.2.0/24/8')).toBe(false);
		expect(validNetwork('1'.repeat(44))).toBe(false);
	});

	it('bounds the range', () => {
		const now = Date.parse('2026-11-20T12:00:00Z');
		expect(securityRange(emptySecurityFilter(), now)).toEqual({ from: now - 86400_000, to: now });
		const long = securityRange({ ...emptySecurityFilter(), from: 0, to: now }, now);
		expect(long.to - long.from).toBe(MAX_RANGE);
		const tiny = securityRange({ ...emptySecurityFilter(), from: now, to: now }, now);
		expect(tiny.to - tiny.from).toBe(1000);
	});
});
