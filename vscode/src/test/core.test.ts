import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import {
	FileReport,
	Finding,
	Settings,
	buildArgs,
	commentStyleFor,
	ignoreComment,
	isFlywayName,
	messageFor,
	parseReport,
	severityFor,
	summarize,
	toDiagnostics,
	versionOk,
} from '../core';

const finding = (over: Partial<Finding> = {}): Finding => ({
	rule: 'create-index',
	risk: 'high',
	table: 'public.transactions',
	rows: 14_000_000,
	line: 4,
	statement: 'CREATE INDEX idx ON transactions (amount);',
	lock: 'SHARE lock: blocks writes to the table for the whole index build',
	message: 'this will lock public.transactions (14.0M rows) for approximately 1-6 min',
	alternative: 'CREATE INDEX CONCURRENTLY',
	blocking: true,
	...over,
});

const settings = (over: Partial<Settings> = {}): Settings => ({
	engine: '',
	dsnEnv: 'DBGUARD_DSN',
	tableRows: {},
	largeRows: 100000,
	failOn: 'medium-high',
	mysqlVersion: '',
	placeholders: {},
	...over,
});

test('parseReport reads the documented JSON and defaults missing lists', () => {
	const r = parseReport(JSON.stringify([{ path: 'a.sql', findings: [finding()], problems: ['line 2: x'] }, { path: 'b.sql' }]));
	assert.equal(r.length, 2);
	assert.equal(r[0].findings[0].rule, 'create-index');
	assert.deepEqual(r[1].findings, []);
	assert.deepEqual(r[1].problems, []);
});

test('parseReport rejects output that is not the JSON contract, with a helpful message', () => {
	assert.throws(() => parseReport('usage: dbguard <command>'), /version 0\.4\.0/);
	assert.throws(() => parseReport('{"a":1}'), /JSON array/);
});

test('severity: acknowledged is a hint, blocking an error, other risks warnings or info', () => {
	assert.equal(severityFor(finding({ override: 'quiet window' })), 'hint');
	assert.equal(severityFor(finding({ blocking: true })), 'error');
	assert.equal(severityFor(finding({ blocking: false, risk: 'medium' })), 'warning');
	assert.equal(severityFor(finding({ blocking: false, risk: 'high' })), 'warning');
	assert.equal(severityFor(finding({ blocking: false, risk: 'low' })), 'info');
	// An acknowledged finding never shows as an error even if the risk is high.
	assert.equal(severityFor(finding({ blocking: false, override: 'x', risk: 'high' })), 'hint');
});

test('message includes the impact, the lock and the safer alternative', () => {
	const m = messageFor(finding());
	assert.match(m, /14\.0M rows/);
	assert.match(m, /SHARE lock/);
	assert.match(m, /Safer: CREATE INDEX CONCURRENTLY/);
	assert.match(messageFor(finding({ override: 'quiet window' })), /Acknowledged: quiet window/);
	assert.doesNotMatch(messageFor(finding({ override: 'quiet window' })), /Safer:/);
});

test('diagnostics: 1-based lines become 0-based ranges, multi-line statements span their lines', () => {
	const report: FileReport = {
		path: 'a.sql',
		findings: [finding({ line: 4, statement: 'CREATE INDEX i\n  ON t\n  (a);' }), finding({ line: 1, rule: 'drop-column', risk: 'low', blocking: false })],
		problems: ['line 9: could not parse this statement, so it was NOT analyzed (x)', 'something without a line'],
	};
	const d = toDiagnostics(report);
	assert.equal(d.length, 4);
	assert.deepEqual(d.map((x) => x.startLine), [0, 0, 3, 8], 'sorted by line');
	const multi = d.find((x) => x.rule === 'create-index')!;
	assert.equal(multi.startLine, 3);
	assert.equal(multi.endLine, 5);
	const problem = d.find((x) => x.startLine === 8)!;
	assert.equal(problem.kind, 'problem');
	assert.equal(problem.code, 'not-analyzed');
	assert.match(problem.message, /NOT analyzed/);
	assert.doesNotMatch(problem.message, /^line 9/);
	assert.equal(d.find((x) => x.message === 'something without a line')!.startLine, 0);
});

test('buildArgs: defaults are minimal, every setting maps to its flag', () => {
	assert.deepEqual(buildArgs(settings(), 'V1__x.sql'), ['check', '--format', 'json', '--fail-on', 'medium-high', '--large-rows', '100000', 'V1__x.sql']);
	const a = buildArgs(
		settings({
			engine: 'mysql',
			dsnEnv: 'MY_DSN',
			mysqlVersion: '8.0.28',
			failOn: 'high',
			largeRows: 5,
			tableRows: { orders: 14000000, 'shop.users': 10 },
			placeholders: { schema: 'app' },
		}),
		'f.xml',
	);
	assert.deepEqual(a, [
		'check', '--format', 'json', '--fail-on', 'high', '--large-rows', '5',
		'--engine', 'mysql', '--dsn-env', 'MY_DSN', '--mysql-version', '8.0.28',
		'--rows', 'orders=14000000', '--rows', 'shop.users=10',
		'--placeholder', 'schema=app', 'f.xml',
	]);
});

test('Flyway file names', () => {
	for (const n of ['V1__init.sql', 'V2.1__add_col.sql', 'V10_3__x.sql', 'R__views.sql', 'V2__AddIndex.java', 'R__Refresh.java']) {
		assert.ok(isFlywayName(n), n);
	}
	for (const n of ['init.sql', 'V1_init.sql', 'V__x.sql', 'V1__x.txt', 'v1__x.sql', 'Helper.java', 'V1_Add.java']) {
		assert.ok(!isFlywayName(n), n);
	}
});

test('ignore comments use the right syntax per file type and carry the reason', () => {
	assert.equal(commentStyleFor('V1__x.sql'), 'sql');
	assert.equal(commentStyleFor('c.XML'), 'xml');
	assert.equal(commentStyleFor('c.yml'), 'yaml');
	assert.equal(commentStyleFor('c.yaml'), 'yaml');
	assert.equal(commentStyleFor('V2__Add.java'), 'java');
	assert.equal(commentStyleFor('c.json'), undefined, 'JSON has no comments');
	assert.equal(ignoreComment('sql', 'create-index', 'quiet window'), '-- dbguard:ignore create-index reason: quiet window');
	assert.equal(ignoreComment('xml', 'create-index', 'quiet window'), '<!-- dbguard:ignore create-index reason: quiet window -->');
	assert.equal(ignoreComment('yaml', 'create-index', 'quiet window'), '# dbguard:ignore create-index reason: quiet window');
	assert.equal(ignoreComment('java', 'create-index', 'quiet window'), '// dbguard:ignore create-index reason: quiet window');
});

test('ignore comments: a reason is mandatory, whitespace is collapsed, and XML stays well-formed', () => {
	assert.throws(() => ignoreComment('sql', 'create-index', '   '), /reason is required/);
	assert.equal(ignoreComment('sql', 'r', 'a\n  b\tc'), '-- dbguard:ignore r reason: a b c', 'a multi-line reason must not break out of the comment');
	// "--" is illegal inside an XML comment.
	assert.ok(!ignoreComment('xml', 'r', 'see -- ticket --- 42').slice(4, -3).includes('--'));
});

test('versionOk: 0.4.0 and newer pass, older and unknown outputs fail, dev builds pass', () => {
	assert.ok(versionOk('dbguard v0.4.0'));
	assert.ok(versionOk('dbguard v0.4.1'));
	assert.ok(versionOk('dbguard v1.0.0'));
	assert.ok(versionOk('dbguard dev'));
	assert.ok(!versionOk('dbguard v0.3.0'), '0.3.x cannot read Java migrations');
	assert.ok(!versionOk('dbguard v0.3.9'));
	assert.ok(!versionOk('dbguard v0.2.0'));
	assert.ok(!versionOk('usage: dbguard <command>'));
});

test('summarize counts live findings only', () => {
	const d = toDiagnostics({
		path: 'a',
		findings: [
			finding(),
			finding({ blocking: false, risk: 'medium' }),
			finding({ override: 'x', blocking: false }),
		],
		problems: ['line 1: not analyzed'],
	});
	const s = summarize(d);
	assert.equal(s.errors, 1);
	assert.equal(s.warnings, 1);
	assert.equal(s.text, '1 blocking, 1 to review');
	assert.equal(summarize([]).text, 'no risks');
});
