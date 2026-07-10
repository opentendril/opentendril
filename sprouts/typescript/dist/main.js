import { promises as fs } from 'node:fs';
import * as path from 'node:path';
import * as readline from 'node:readline';
import { execaCommand } from 'execa';
import { simpleGit } from 'simple-git';
const skipDirs = new Set(['.git', 'node_modules', 'vendor', '.venv', 'venv', 'dist', 'build', '__pycache__']);
async function main() {
    const workspaceRoot = path.resolve(process.cwd());
    const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
    for await (const line of rl) {
        const trimmed = line.trim();
        if (!trimmed) {
            continue;
        }
        let response;
        try {
            const call = JSON.parse(trimmed);
            response = await executeTool(workspaceRoot, call);
        }
        catch (error) {
            response = {
                status: 'error',
                error: error instanceof Error ? error.message : String(error),
            };
        }
        process.stdout.write(`${JSON.stringify(response)}\n`);
    }
}
async function executeTool(workspaceRoot, call) {
    const toolName = call.tool.trim();
    if (!toolName) {
        return { status: 'error', error: 'tool name is required' };
    }
    switch (toolName) {
        case 'readFile':
            return readFileTool(workspaceRoot, call.arguments ?? {});
        case 'writeFile':
            return writeFileTool(workspaceRoot, call.arguments ?? {});
        case 'listFiles':
            return listFilesTool(workspaceRoot, call.arguments ?? {});
        case 'gitCommit':
            return gitCommitTool(workspaceRoot, call.arguments ?? {});
        case 'gitDiff':
            return gitDiffTool(workspaceRoot, call.arguments ?? {});
        case 'execCommand':
            return execCommandTool(workspaceRoot, call.arguments ?? {});
        case 'listAvailableTools':
            return { status: 'success', output: { tools: availableTools() } };
        default:
            return { status: 'error', error: `unsupported tool "${toolName}"` };
    }
}
function availableTools() {
    return [
        {
            name: 'readFile',
            description: 'Read a text file from the workspace.',
            arguments: [
                {
                    name: 'path',
                    type: 'string',
                    description: 'Path to the file, relative to the workspace root.',
                    required: true,
                },
            ],
        },
        {
            name: 'writeFile',
            description: 'Write text content to a file, creating parent directories when needed.',
            arguments: [
                {
                    name: 'path',
                    type: 'string',
                    description: 'Path to the file, relative to the workspace root.',
                    required: true,
                },
                {
                    name: 'content',
                    type: 'string',
                    description: 'The full file contents to write.',
                    required: true,
                },
                {
                    name: 'append',
                    type: 'boolean',
                    description: 'Append instead of overwriting the file.',
                },
            ],
        },
        {
            name: 'listFiles',
            description: 'List files and directories under a workspace path.',
            arguments: [
                {
                    name: 'path',
                    type: 'string',
                    description: 'Directory to list, relative to the workspace root.',
                },
                {
                    name: 'maxDepth',
                    type: 'number',
                    description: 'Maximum recursion depth to traverse.',
                },
                {
                    name: 'maxEntries',
                    type: 'number',
                    description: 'Maximum number of entries to return.',
                },
            ],
        },
        {
            name: 'gitCommit',
            description: 'Stage files and create a git commit.',
            arguments: [
                {
                    name: 'message',
                    type: 'string',
                    description: 'Commit message.',
                    required: true,
                },
                {
                    name: 'paths',
                    type: 'string[]',
                    description: 'Optional list of paths to stage instead of all changes.',
                },
            ],
        },
        {
            name: 'gitDiff',
            description: 'Show the current git diff.',
            arguments: [
                {
                    name: 'cached',
                    type: 'boolean',
                    description: 'Show the staged diff instead of the working tree diff.',
                },
                {
                    name: 'paths',
                    type: 'string[]',
                    description: 'Optional list of paths to limit the diff.',
                },
            ],
        },
        {
            name: 'execCommand',
            description: 'Run a shell command inside the workspace.',
            arguments: [
                {
                    name: 'command',
                    type: 'string',
                    description: 'Shell command to execute.',
                    required: true,
                },
                {
                    name: 'cwd',
                    type: 'string',
                    description: 'Optional working directory, relative to the workspace root.',
                },
                {
                    name: 'timeoutSeconds',
                    type: 'number',
                    description: 'Optional timeout in seconds.',
                },
            ],
        },
        {
            name: 'listAvailableTools',
            description: 'Return the executor tool catalog.',
        },
    ];
}
async function readFileTool(workspaceRoot, args) {
    const rawPath = stringArg(args, 'path');
    if (!rawPath) {
        return { status: 'error', error: 'readFile requires a path' };
    }
    const { absPath, relPath, error } = resolveWorkspacePath(workspaceRoot, rawPath);
    if (error) {
        return { status: 'error', error };
    }
    try {
        const content = await fs.readFile(absPath, 'utf8');
        return { status: 'success', output: { path: relPath, content } };
    }
    catch (error_) {
        return { status: 'error', error: error_ instanceof Error ? error_.message : String(error_) };
    }
}
async function writeFileTool(workspaceRoot, args) {
    const rawPath = stringArg(args, 'path');
    const content = stringArg(args, 'content');
    if (!rawPath) {
        return { status: 'error', error: 'writeFile requires a path' };
    }
    if (content === undefined) {
        return { status: 'error', error: 'writeFile requires content' };
    }
    const append = booleanArg(args, 'append') ?? false;
    const { absPath, relPath, error } = resolveWorkspacePath(workspaceRoot, rawPath);
    if (error) {
        return { status: 'error', error };
    }
    try {
        await fs.mkdir(path.dirname(absPath), { recursive: true });
        await fs.writeFile(absPath, content, append ? { flag: 'a' } : { flag: 'w' });
        return {
            status: 'success',
            output: {
                path: relPath,
                bytesWritten: Buffer.byteLength(content),
                mode: append ? 'append' : 'overwrite',
            },
        };
    }
    catch (error_) {
        return { status: 'error', error: error_ instanceof Error ? error_.message : String(error_) };
    }
}
async function listFilesTool(workspaceRoot, args) {
    const rawPath = stringArg(args, 'path') ?? '.';
    const { absPath, relPath, error } = resolveWorkspacePath(workspaceRoot, rawPath);
    if (error) {
        return { status: 'error', error };
    }
    const maxDepth = Math.max(0, Math.floor(numberArg(args, 'maxDepth') ?? 3));
    const maxEntries = Math.max(1, Math.floor(numberArg(args, 'maxEntries') ?? 500));
    try {
        const info = await fs.stat(absPath);
        const entries = [];
        let truncated = false;
        if (info.isDirectory()) {
            truncated = await walkDirectory(absPath, relPath, 0, maxDepth, maxEntries, entries);
        }
        else {
            entries.push(entryForPath(relPath, info));
        }
        return {
            status: 'success',
            output: {
                root: relPath,
                entries,
                truncated,
            },
        };
    }
    catch (error_) {
        return { status: 'error', error: error_ instanceof Error ? error_.message : String(error_) };
    }
}
async function gitCommitTool(workspaceRoot, args) {
    const message = stringArg(args, 'message');
    if (!message) {
        return { status: 'error', error: 'gitCommit requires a message' };
    }
    const paths = stringArrayArg(args, 'paths');
    const git = simpleGit(workspaceRoot);
    try {
        if (paths && paths.length > 0) {
            const resolvedPaths = [];
            for (const rawPath of paths) {
                const { relPath, error } = resolveWorkspacePath(workspaceRoot, rawPath);
                if (error) {
                    return { status: 'error', error };
                }
                resolvedPaths.push(relPath);
            }
            await git.add(['--', ...resolvedPaths]);
        }
        else {
            await git.add(['-A']);
        }
        const status = await git.status();
        if (status.staged.length === 0 && status.created.length === 0 && status.deleted.length === 0 && status.modified.length === 0 && status.renamed.length === 0) {
            return {
                status: 'success',
                output: {
                    committed: false,
                    message: 'nothing to commit',
                    paths: paths ?? [],
                },
            };
        }
        await git.raw([
            '-c', 'user.name=OpenTendril',
            '-c', 'user.email=opentendril@localhost',
            'commit',
            '-m',
            message,
        ]);
        const hash = (await git.raw(['rev-parse', 'HEAD'])).trim();
        return {
            status: 'success',
            output: {
                committed: true,
                hash,
                message,
                paths: paths ?? [],
            },
        };
    }
    catch (error_) {
        return { status: 'error', error: error_ instanceof Error ? error_.message : String(error_) };
    }
}
async function gitDiffTool(workspaceRoot, args) {
    const cached = booleanArg(args, 'cached') ?? false;
    const paths = stringArrayArg(args, 'paths') ?? [];
    const git = simpleGit(workspaceRoot);
    try {
        const diffArgs = ['--no-color', '--binary'];
        if (cached) {
            diffArgs.push('--cached');
        }
        if (paths.length > 0) {
            diffArgs.push('--', ...paths);
        }
        const diff = await git.diff(diffArgs);
        return {
            status: 'success',
            output: {
                diff,
                cached,
                paths,
            },
        };
    }
    catch (error_) {
        return { status: 'error', error: error_ instanceof Error ? error_.message : String(error_) };
    }
}
async function execCommandTool(workspaceRoot, args) {
    const command = stringArg(args, 'command');
    if (!command) {
        return { status: 'error', error: 'execCommand requires a command' };
    }
    const cwdRaw = stringArg(args, 'cwd') ?? '.';
    const { absPath: cwdAbs, relPath: cwdRel, error } = resolveWorkspacePath(workspaceRoot, cwdRaw);
    if (error) {
        return { status: 'error', error };
    }
    const timeoutSeconds = numberArg(args, 'timeoutSeconds') ?? 120;
    const timeoutMs = Math.max(1, Math.floor(timeoutSeconds * 1000));
    try {
        const result = await execaCommand(command, {
            cwd: cwdAbs,
            timeout: timeoutMs,
            reject: false,
            all: true,
        });
        const response = {
            command,
            cwd: cwdRel,
            stdout: result.stdout ?? '',
            stderr: result.stderr ?? '',
            exitCode: result.exitCode ?? 0,
        };
        if ((result.exitCode ?? 0) !== 0) {
            return {
                status: 'error',
                output: response,
                error: `command exited with code ${result.exitCode ?? 0}`,
            };
        }
        return { status: 'success', output: response };
    }
    catch (error_) {
        const message = error_ instanceof Error ? error_.message : String(error_);
        return {
            status: 'error',
            error: message,
            output: {
                command,
                cwd: cwdRel,
                stdout: '',
                stderr: message,
                exitCode: -1,
            },
        };
    }
}
async function walkDirectory(rootAbs, rootRel, depth, maxDepth, maxEntries, entries) {
    const childEntries = await fs.readdir(rootAbs, { withFileTypes: true });
    childEntries.sort((a, b) => a.name.localeCompare(b.name));
    for (const entry of childEntries) {
        if (entries.length >= maxEntries) {
            return true;
        }
        if (skipDirs.has(entry.name)) {
            continue;
        }
        const childAbs = path.join(rootAbs, entry.name);
        const childRel = path.posix.join(rootRel.replaceAll(path.sep, '/'), entry.name);
        const stats = await fs.lstat(childAbs);
        entries.push(entryForPath(childRel, stats));
        if (entries.length >= maxEntries) {
            return true;
        }
        if (entry.isDirectory() && depth + 1 < maxDepth) {
            const truncated = await walkDirectory(childAbs, childRel, depth + 1, maxDepth, maxEntries, entries);
            if (truncated) {
                return true;
            }
        }
    }
    return false;
}
function entryForPath(relPath, stats) {
    let type = 'file';
    if (stats.isDirectory()) {
        type = 'dir';
    }
    else if (stats.isSymbolicLink()) {
        type = 'symlink';
    }
    return {
        path: relPath.replaceAll(path.sep, '/'),
        type,
        size: Number(stats.size),
    };
}
function resolveWorkspacePath(workspaceRoot, rawPath) {
    const rootAbs = path.resolve(workspaceRoot);
    const cleaned = rawPath.trim() ? rawPath.trim() : '.';
    const absPath = path.isAbsolute(cleaned) ? path.normalize(cleaned) : path.resolve(rootAbs, cleaned);
    const relative = path.relative(rootAbs, absPath);
    if (relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
        return { absPath, relPath: relative, error: `path "${rawPath}" escapes the workspace root` };
    }
    return {
        absPath,
        relPath: relative === '' ? '.' : relative.replaceAll(path.sep, '/'),
    };
}
function stringArg(args, key) {
    const value = args[key];
    if (typeof value === 'string') {
        return value;
    }
    return undefined;
}
function booleanArg(args, key) {
    const value = args[key];
    if (typeof value === 'boolean') {
        return value;
    }
    if (typeof value === 'string') {
        if (value.toLowerCase() === 'true') {
            return true;
        }
        if (value.toLowerCase() === 'false') {
            return false;
        }
    }
    if (typeof value === 'number') {
        return value !== 0;
    }
    return undefined;
}
function numberArg(args, key) {
    const value = args[key];
    if (typeof value === 'number' && Number.isFinite(value)) {
        return value;
    }
    if (typeof value === 'string') {
        const parsed = Number(value);
        if (!Number.isNaN(parsed)) {
            return parsed;
        }
    }
    return undefined;
}
function stringArrayArg(args, key) {
    const value = args[key];
    if (Array.isArray(value)) {
        return value.map((item) => String(item));
    }
    if (typeof value === 'string' && value.trim() !== '') {
        return [value];
    }
    return undefined;
}
void main();
