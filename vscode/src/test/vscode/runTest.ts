// Launches a real VS Code with the extension loaded and runs the suite inside it.
// Uses an isolated user-data and extensions directory so nothing touches your profile.

import { execFileSync } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { downloadAndUnzipVSCode, runTests } from '@vscode/test-electron';

async function main(): Promise<void> {
	const extensionDevelopmentPath = path.resolve(__dirname, '..', '..', '..');
	const extensionTestsPath = path.resolve(__dirname, 'suite', 'index');
	const repoRoot = path.resolve(extensionDevelopmentPath, '..');

	// The real dbguard binary, built from this repository (or supplied).
	const work = fs.mkdtempSync(path.join(os.tmpdir(), 'dbguard-vscode-test-'));
	let bin = process.env.DBGUARD_BIN ?? '';
	if (!bin) {
		bin = path.join(work, process.platform === 'win32' ? 'dbguard.exe' : 'dbguard');
		execFileSync('go', ['build', '-o', bin, './cmd/dbguard'], { cwd: repoRoot, stdio: 'inherit', timeout: 900_000 });
	}

	// A workspace with a Flyway migration directory.
	const workspace = path.join(work, 'workspace');
	fs.cpSync(path.resolve(extensionDevelopmentPath, 'src', 'test', 'vscode', 'fixtures'), workspace, { recursive: true });
	fs.mkdirSync(path.join(workspace, '.vscode'), { recursive: true });
	fs.writeFileSync(
		path.join(workspace, '.vscode', 'settings.json'),
		JSON.stringify({ 'dbguard.path': bin, 'dbguard.tableRows': { transactions: 14000000, orders: 14000000 } }, null, 2),
	);

	// Use the VS Code that is installed (no download) unless told otherwise.
	const installed = [
		process.env.VSCODE_EXECUTABLE_PATH,
		process.env.LOCALAPPDATA && path.join(process.env.LOCALAPPDATA, 'Programs', 'Microsoft VS Code', 'Code.exe'),
		'/usr/share/code/code',
		'/Applications/Visual Studio Code.app/Contents/MacOS/Electron',
	].find((p) => p && fs.existsSync(p));
	const vscodeExecutablePath = installed ?? (await downloadAndUnzipVSCode());

	// When started from a terminal inside VS Code (or another Electron app) the environment carries
	// ELECTRON_RUN_AS_NODE, which makes Code.exe behave as plain Node, plus VSCODE_* variables that point
	// a new window at the parent instance. The test window must be a clean, independent VS Code.
	for (const key of Object.keys(process.env)) {
		if (key === 'ELECTRON_RUN_AS_NODE' || key.startsWith('VSCODE_')) {
			delete process.env[key];
		}
	}

	const resultsFile = path.join(work, 'results.txt');
	try {
	await runTests({
		vscodeExecutablePath,
		extensionDevelopmentPath,
		extensionTestsPath,
		launchArgs: [
			workspace,
			'--user-data-dir', path.join(work, 'user-data'),
			'--extensions-dir', path.join(work, 'extensions'),
			'--disable-extensions',
			'--disable-workspace-trust',
			'--skip-welcome',
			'--skip-release-notes',
		],
		extensionTestsEnv: { DBGUARD_TEST_WORKSPACE: workspace, DBGUARD_TEST_RESULTS: resultsFile },
	});
	} finally {
		// Print what the tests inside VS Code reported, pass or fail.
		if (fs.existsSync(resultsFile)) {
			console.log('==== results from inside VS Code ====');
			console.log(fs.readFileSync(resultsFile, 'utf8'));
		} else {
			console.log('==== the test suite inside VS Code reported nothing ====');
		}
	}
}

main().catch((err) => {
	console.error('VS Code integration test failed:', err);
	process.exit(1);
});
