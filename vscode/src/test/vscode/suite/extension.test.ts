import * as assert from 'assert';
import * as path from 'path';
import * as vscode from 'vscode';
import type { Api } from '../../../extension';

const workspace = process.env.DBGUARD_TEST_WORKSPACE!;
const file = (...p: string[]) => vscode.Uri.file(path.join(workspace, ...p));

async function waitFor<T>(what: string, fn: () => T | undefined | false, ms = 60_000): Promise<T> {
	const end = Date.now() + ms;
	for (;;) {
		const v = fn();
		if (v) {
			return v;
		}
		if (Date.now() > end) {
			throw new Error(`timed out waiting for ${what}`);
		}
		await new Promise((r) => setTimeout(r, 100));
	}
}

const mine = (uri: vscode.Uri) => vscode.languages.getDiagnostics(uri).filter((d) => d.source === 'DB Guard');
const codeOf = (d: vscode.Diagnostic) => (typeof d.code === 'object' ? String(d.code.value) : String(d.code));
const config = () => vscode.workspace.getConfiguration('dbguard');

async function api(): Promise<Api> {
	const ext = vscode.extensions.getExtension('OriginalDaniel02.dbguard');
	assert.ok(ext, 'the extension is installed in the test host');
	return (await ext!.activate()) as Api;
}

async function open(...p: string[]): Promise<vscode.TextDocument> {
	const doc = await vscode.workspace.openTextDocument(file(...p));
	await vscode.window.showTextDocument(doc);
	return doc;
}

async function revert(): Promise<void> {
	await vscode.commands.executeCommand('workbench.action.revertAndCloseActiveEditor');
}

suite('DB Guard extension in a real VS Code', () => {
	teardown(async () => {
		await config().update('checkOnType', undefined, vscode.ConfigurationTarget.Workspace);
		await config().update('tableRows', { transactions: 14000000, orders: 14000000 }, vscode.ConfigurationTarget.Workspace);
		await vscode.commands.executeCommand('workbench.action.closeAllEditors');
	});

	test('activates and contributes its commands', async () => {
		await api();
		const cmds = await vscode.commands.getCommands(true);
		for (const c of ['dbguard.checkFile', 'dbguard.acknowledge', 'dbguard.showChangelog', 'dbguard.showOutput']) {
			assert.ok(cmds.includes(c), `command ${c}`);
		}
	});

	test('a risky migration is flagged automatically when opened', async () => {
		await api();
		const doc = await open('db', 'migration', 'V3__add_index.sql'); // no explicit check call: the open event does it
		const diags = await waitFor('diagnostics after open', () => {
			const d = mine(doc.uri);
			return d.length ? d : undefined;
		});
		assert.strictEqual(diags.length, 1);
		const d = diags[0];
		assert.strictEqual(d.severity, vscode.DiagnosticSeverity.Error);
		assert.strictEqual(codeOf(d), 'create-index');
		assert.strictEqual(d.range.start.line, 2, 'the statement, not the comment above it');
		assert.strictEqual(d.range.start.character, 0);
		assert.strictEqual(d.range.end.line, 2);
		assert.match(d.message, /14\.0M rows/);
		assert.match(d.message, /Safer: CREATE INDEX CONCURRENTLY/);
	});

	test('a safe migration has no diagnostics', async () => {
		const a = await api();
		const doc = await open('db', 'migration', 'V1__create_accounts.sql');
		await a.check(doc);
		assert.deepStrictEqual(mine(doc.uri), []);
	});

	test('a file outside the migration folders is not checked automatically, but can be checked on demand', async () => {
		const a = await api();
		const doc = await open('notes', 'scratch.sql');
		await new Promise((r) => setTimeout(r, 3000)); // give the open event time to (wrongly) fire
		assert.deepStrictEqual(mine(doc.uri), [], 'not a migration: no automatic check');
		await vscode.commands.executeCommand('dbguard.checkFile');
		await a.check(doc, true);
		assert.strictEqual(mine(doc.uri).length, 1, 'the explicit command checks any file');
	});

	test('the quick fix acknowledges a risk with a dbguard:ignore comment, and the diagnostic becomes a hint', async () => {
		const a = await api();
		const doc = await open('db', 'migration', 'V3__add_index.sql');
		await a.check(doc);
		const d = mine(doc.uri)[0];

		const actions = (await vscode.commands.executeCommand<vscode.CodeAction[]>('vscode.executeCodeActionProvider', doc.uri, d.range)) ?? [];
		const fix = actions.find((x) => /acknowledge "create-index"/.test(x.title));
		assert.ok(fix, `a quick fix is offered: ${actions.map((x) => x.title).join(' | ')}`);

		// The command the quick fix runs; the reason is passed so no input box is needed.
		const ok = await vscode.commands.executeCommand<boolean>('dbguard.acknowledge', doc.uri, d.range.start.line, 'create-index', 'write-quiet during the maintenance window');
		assert.strictEqual(ok, true);
		assert.strictEqual(doc.lineAt(2).text, '-- dbguard:ignore create-index reason: write-quiet during the maintenance window');
		assert.strictEqual(doc.lineAt(3).text, 'CREATE INDEX idx_transactions_amount ON transactions (amount);');

		const after = await waitFor('acknowledged diagnostic', () => {
			const x = mine(doc.uri);
			return x.length === 1 && x[0].severity === vscode.DiagnosticSeverity.Hint ? x : undefined;
		});
		assert.match(after[0].message, /Acknowledged: write-quiet during the maintenance window/);
		assert.strictEqual(after[0].range.start.line, 3, 'the finding moved down with the inserted line');
		await revert();
	});

	test('saving a fixed migration clears the warning', async () => {
		const a = await api();
		const doc = await open('db', 'migration', 'V3__add_index.sql');
		await a.check(doc);
		assert.strictEqual(mine(doc.uri).length, 1);

		const edit = new vscode.WorkspaceEdit();
		edit.replace(doc.uri, doc.lineAt(2).range, 'CREATE INDEX CONCURRENTLY idx_transactions_amount ON transactions (amount);');
		await vscode.workspace.applyEdit(edit);
		await doc.save(); // the save event triggers the check
		await waitFor('diagnostics to clear after the fix is saved', () => mine(doc.uri).length === 0);

		// Put the file back for the other tests.
		const undo = new vscode.WorkspaceEdit();
		undo.replace(doc.uri, doc.lineAt(2).range, 'CREATE INDEX idx_transactions_amount ON transactions (amount);');
		await vscode.workspace.applyEdit(undo);
		await doc.save();
	});

	test('check on type uses the unsaved text', async () => {
		const a = await api();
		await config().update('checkOnType', true, vscode.ConfigurationTarget.Workspace);
		const doc = await open('db', 'migration', 'V1__create_accounts.sql');
		await a.check(doc);
		assert.deepStrictEqual(mine(doc.uri), []);

		const edit = new vscode.WorkspaceEdit();
		edit.insert(doc.uri, new vscode.Position(doc.lineCount - 1, 0), '\nCREATE INDEX idx_typed ON transactions (email);\n');
		await vscode.workspace.applyEdit(edit);
		assert.ok(doc.isDirty, 'the text is not saved');
		const diags = await waitFor('a diagnostic for text that was typed but not saved', () => {
			const d = mine(doc.uri);
			return d.length ? d : undefined;
		});
		assert.strictEqual(codeOf(diags[0]), 'create-index');
		await revert();
	});

	test('changing a setting re-checks open migrations', async () => {
		const a = await api();
		const doc = await open('db', 'migration', 'V3__add_index.sql');
		await a.check(doc);
		assert.strictEqual(mine(doc.uri)[0].severity, vscode.DiagnosticSeverity.Error);

		// A small table is low risk: the finding is downgraded to information.
		await config().update('tableRows', { transactions: 100 }, vscode.ConfigurationTarget.Workspace);
		await waitFor('the diagnostic to be downgraded', () => {
			const d = mine(doc.uri);
			return d.length === 1 && d[0].severity === vscode.DiagnosticSeverity.Information ? d : undefined;
		});
	});

	test('Liquibase XML: flagged on the changeSet line, acknowledged with an XML comment', async () => {
		const a = await api();
		const doc = await open('db', 'changelog', 'changelog.xml');
		await a.check(doc);
		const d = mine(doc.uri).find((x) => codeOf(x) === 'create-index')!;
		assert.ok(d, 'create-index found');
		assert.strictEqual(d.range.start.line, 2, 'the <changeSet> line');

		await vscode.commands.executeCommand('dbguard.acknowledge', doc.uri, d.range.start.line, 'create-index', 'maintenance window');
		assert.strictEqual(doc.lineAt(2).text.trim(), '<!-- dbguard:ignore create-index reason: maintenance window -->');
		assert.strictEqual(doc.lineAt(2).text.indexOf('<!--'), 2, 'indented like the changeSet below it');
		await waitFor('acknowledged', () => mine(doc.uri).some((x) => x.severity === vscode.DiagnosticSeverity.Hint));
		await revert();
	});

	test('Liquibase JSON: flagged, but no comment quick fix because JSON has no comments', async () => {
		const a = await api();
		const doc = await open('db', 'changelog', 'changelog.json');
		await a.check(doc);
		const d = mine(doc.uri)[0];
		assert.ok(d, 'flagged');
		const actions = (await vscode.commands.executeCommand<vscode.CodeAction[]>('vscode.executeCodeActionProvider', doc.uri, d.range)) ?? [];
		assert.ok(!actions.some((x) => /acknowledge/.test(x.title)), 'no acknowledge action for JSON');
	});

	test('the schema changelog command opens a searchable history', async () => {
		await api();
		await open('db', 'migration', 'V1__create_accounts.sql');
		await vscode.commands.executeCommand('dbguard.showChangelog', 'orders', path.join(workspace, 'snapshots'));
		const doc = await waitFor('the changelog document', () => {
			const d = vscode.window.activeTextEditor?.document;
			return d && d.languageId === 'markdown' && d.getText().includes('Schema changelog') ? d : undefined;
		});
		assert.match(doc.getText(), /column discount added \(numeric\)/);
	});

	test('a missing binary does not break the editor', async () => {
		const a = await api();
		const doc = await open('db', 'migration', 'V3__add_index.sql');
		await config().update('path', path.join(workspace, 'no-such-dbguard'), vscode.ConfigurationTarget.Workspace);
		try {
			await a.check(doc, true); // must resolve, not throw
		} finally {
			await config().update('path', undefined, vscode.ConfigurationTarget.Workspace);
		}
	});
});
