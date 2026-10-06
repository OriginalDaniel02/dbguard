// Integration test: the extension's runner and diagnostics code against the REAL dbguard
// binary (built from this repository). No VS Code needed.

import { before, test } from 'node:test';
import * as assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { Settings, buildArgs, commentStyleFor, ignoreComment, toDiagnostics } from '../core';
import { BinaryNotFound, checkVersion, runChangelog, runCheck } from '../runner';

const repoRoot = path.resolve(__dirname, '..', '..', '..');
let bin = '';
let tmp = '';

const settings = (over: Partial<Settings> = {}): Settings => ({
	engine: '',
	dsnEnv: 'DBGUARD_DSN',
	tableRows: { transactions: 14_000_000, orders: 14_000_000 },
	largeRows: 100000,
	failOn: 'medium-high',
	mysqlVersion: '',
	placeholders: {},
	...over,
});

before(() => {
	tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'dbguard-ext-'));
	if (process.env.DBGUARD_BIN) {
		bin = process.env.DBGUARD_BIN;
		return;
	}
	bin = path.join(tmp, process.platform === 'win32' ? 'dbguard.exe' : 'dbguard');
	execFileSync('go', ['build', '-o', bin, './cmd/dbguard'], { cwd: repoRoot, stdio: 'inherit', timeout: 900_000 });
});

function write(name: string, content: string): string {
	const p = path.join(tmp, name);
	fs.mkdirSync(path.dirname(p), { recursive: true });
	fs.writeFileSync(p, content);
	return p;
}

async function check(file: string, s = settings()) {
	const res = await runCheck(bin, buildArgs(s, file), tmp).result;
	return { res, diags: res.reports[0] ? toDiagnostics(res.reports[0]) : [] };
}

test('the binary is new enough and reports its version', async () => {
	const v = await checkVersion(bin);
	assert.ok(v.ok, v.output);
	assert.match(v.output, /^dbguard /);
});

test('a risky Flyway migration produces a blocking error on the right line', async () => {
	const f = write('V3__idx.sql', '-- add an index\n\nCREATE INDEX idx_amount ON transactions (amount);\n');
	const { res, diags } = await check(f);
	assert.equal(res.code, 1);
	assert.equal(diags.length, 1);
	const d = diags[0];
	assert.equal(d.severity, 'error');
	assert.equal(d.rule, 'create-index');
	assert.equal(d.startLine, 2, 'the CREATE INDEX is on line 3 (0-based 2), not on the comment above it');
	assert.match(d.message, /14\.0M rows/);
	assert.match(d.message, /Safer: CREATE INDEX CONCURRENTLY/);
});

test('a safe migration has no diagnostics and exits 0', async () => {
	const f = write('V4__safe.sql', 'ALTER TABLE transactions ADD COLUMN note text;\n');
	const { res, diags } = await check(f);
	assert.equal(res.code, 0);
	assert.deepEqual(diags, []);
});

test('multi-line statements span all their lines', async () => {
	const f = write('V5__multi.sql', 'CREATE INDEX idx_amount\n  ON transactions\n  (amount);\n');
	const { diags } = await check(f);
	assert.equal(diags[0].startLine, 0);
	assert.equal(diags[0].endLine, 2);
});

// The quick fix inserts an ignore comment above the flagged line; prove the CLI accepts it
// for every file type the extension can write one for.
for (const [name, content, line] of [
	['V6__ack.sql', 'SELECT 1;\nCREATE INDEX idx_amount ON transactions (amount);\n', 1],
	[
		'ack-changelog.xml',
		'<databaseChangeLog>\n  <changeSet id="1" author="a">\n    <createIndex tableName="transactions" indexName="i"><column name="amount"/></createIndex>\n  </changeSet>\n</databaseChangeLog>\n',
		1,
	],
	[
		'ack-changelog.yaml',
		'databaseChangeLog:\n  - changeSet:\n      id: "1"\n      author: a\n      changes:\n        - createIndex:\n            tableName: transactions\n            indexName: i\n            columns:\n              - column:\n                  name: amount\n',
		1,
	],
] as Array<[string, string, number]>) {
	test(`acknowledging a risk in ${name} turns the error into a hint the CLI accepts`, async () => {
		const f = write(name, content);
		const before = await check(f);
		assert.equal(before.res.code, 1);
		const flagged = before.diags.find((d) => d.rule === 'create-index')!;
		assert.equal(flagged.startLine, line);

		// What the extension's "acknowledge" quick fix does.
		const style = commentStyleFor(f)!;
		const lines = content.split('\n');
		const target = lines[flagged.startLine];
		const indent = target.slice(0, target.length - target.trimStart().length);
		lines.splice(flagged.startLine, 0, indent + ignoreComment(style, 'create-index', 'write-quiet during the maintenance window'));
		fs.writeFileSync(f, lines.join('\n'));

		const after = await check(f);
		assert.equal(after.res.code, 0, 'an acknowledged finding does not block');
		const ack = after.diags.find((d) => d.rule === 'create-index')!;
		assert.equal(ack.severity, 'hint');
		assert.ok(ack.acknowledged);
		assert.match(ack.message, /Acknowledged: write-quiet during the maintenance window/);
		assert.ok(!after.diags.some((d) => d.kind === 'problem' && /ignore/.test(d.message)), 'the ignore must not be reported as unused');
	});
}

test('MySQL: the engine setting selects the MySQL rules', async () => {
	const f = write('V7__check.sql', 'ALTER TABLE orders ADD CONSTRAINT chk CHECK (total >= 0);\nALTER TABLE orders ADD COLUMN note VARCHAR(10);\n');
	const { res, diags } = await check(f, settings({ engine: 'mysql' }));
	assert.equal(res.code, 1);
	assert.deepEqual(diags.map((d) => d.rule), ['add-check-constraint'], 'ADD COLUMN is INSTANT on MySQL and must not be flagged');
	assert.match(diags[0].message, /block writes to orders/);
});

test('statements the parser cannot read become informational "not analyzed" diagnostics', async () => {
	const f = write('V8__odd.sql', 'THIS IS NOT MYSQL;\nCREATE FULLTEXT INDEX ft ON orders (body);\n');
	const { res, diags } = await check(f, settings({ engine: 'mysql' }));
	assert.equal(res.code, 1);
	const problem = diags.find((d) => d.kind === 'problem')!;
	assert.equal(problem.startLine, 0);
	assert.equal(problem.severity, 'info');
	assert.match(problem.message, /NOT analyzed/);
});

test('a file dbguard cannot parse is exit code 2 with the reason, not a crash', async () => {
	const f = write('V9__broken.sql', 'ALTER TABL oops;\n');
	const res = await runCheck(bin, buildArgs(settings(), f), tmp).result;
	assert.equal(res.code, 2);
	assert.match(res.stderr, /syntax error/i);
	assert.deepEqual(res.reports, []);
});

test('a missing binary is reported as such', async () => {
	await assert.rejects(runCheck(path.join(tmp, 'no-such-dbguard'), ['check'], tmp).result, BinaryNotFound);
	await assert.rejects(checkVersion(path.join(tmp, 'no-such-dbguard')), BinaryNotFound);
});

test('a newer check can cancel an older one', async () => {
	const f = write('V10__idx.sql', 'CREATE INDEX i ON transactions (a);\n');
	const first = runCheck(bin, buildArgs(settings(), f), tmp);
	first.child.kill();
	await assert.rejects(first.result, /cancelled|timed out|JSON|dbguard/i);
	const second = await runCheck(bin, buildArgs(settings(), f), tmp).result;
	assert.equal(second.code, 1, 'the next check still works');
});

test('the schema changelog command works through the runner', async () => {
	const snap = (cols: string[]) =>
		JSON.stringify({
			format_version: 1,
			engine: 'postgres',
			tables: [{ schema: 'public', name: 'orders', columns: cols.map((c) => ({ name: c, type: 'text' })) }],
		});
	write('snapshots/production/20261001T060000Z.json', snap(['id']));
	write('snapshots/production/20261002T060000Z.json', snap(['id', 'discount']));
	const text = await runChangelog(bin, ['--dir', path.join(tmp, 'snapshots'), '--format', 'markdown', '--table', 'orders'], tmp);
	assert.match(text, /# Schema changelog/);
	assert.match(text, /column discount added \(text\)/);
	await assert.rejects(runChangelog(bin, ['--dir', path.join(tmp, 'nope')], tmp), /no such file|cannot find|does not exist/i);
});
