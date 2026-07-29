import { promises as fs } from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { execaCommand } from 'execa';
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import {
  readFileTool,
  writeFileTool,
  listFilesTool,
  gitCommitTool,
  gitDiffTool,
  execCommandTool,
  executeTool,
  availableTools,
} from '../src/main.js';

describe('Sprout Executor Tools', () => {
  let workspaceRoot: string;

  beforeEach(async () => {
    workspaceRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'ot-sprout-test-'));
  });

  afterEach(async () => {
    await fs.rm(workspaceRoot, { recursive: true, force: true });
  });

  describe('readFileTool', () => {
    it('reads a file successfully', async () => {
      const filePath = path.join(workspaceRoot, 'test.txt');
      await fs.writeFile(filePath, 'hello world');
      const response = await readFileTool(workspaceRoot, { path: 'test.txt' });
      expect(response.status).toBe('success');
      expect((response.output as any).content).toBe('hello world');
    });

    it('returns error when path is missing', async () => {
      const response = await readFileTool(workspaceRoot, {});
      expect(response.status).toBe('error');
      expect(response.error).toContain('readFile requires a path');
    });

    it('returns error when file does not exist', async () => {
      const response = await readFileTool(workspaceRoot, { path: 'nonexistent.txt' });
      expect(response.status).toBe('error');
    });

    it('prevents directory traversal escaping workspace', async () => {
      const response = await readFileTool(workspaceRoot, { path: '../../etc/passwd' });
      expect(response.status).toBe('error');
      expect(response.error).toContain('escapes the workspace root');
    });
  });

  describe('writeFileTool', () => {
    it('overwrites an existing file by default', async () => {
      await writeFileTool(workspaceRoot, { path: 'test.txt', content: 'first' });
      const response = await writeFileTool(workspaceRoot, { path: 'test.txt', content: 'second' });
      expect(response.status).toBe('success');
      expect((response.output as any).mode).toBe('overwrite');
      
      const content = await fs.readFile(path.join(workspaceRoot, 'test.txt'), 'utf8');
      expect(content).toBe('second');
    });

    it('appends to file when append is true', async () => {
      await writeFileTool(workspaceRoot, { path: 'test.txt', content: 'first' });
      const response = await writeFileTool(workspaceRoot, { path: 'test.txt', content: 'second', append: true });
      expect(response.status).toBe('success');
      expect((response.output as any).mode).toBe('append');
      
      const content = await fs.readFile(path.join(workspaceRoot, 'test.txt'), 'utf8');
      expect(content).toBe('firstsecond');
    });

    it('returns error when content is missing', async () => {
      const response = await writeFileTool(workspaceRoot, { path: 'test.txt' });
      expect(response.status).toBe('error');
      expect(response.error).toContain('writeFile requires content');
    });

    it('succeeds with empty string content', async () => {
      const response = await writeFileTool(workspaceRoot, { path: 'empty.txt', content: '' });
      expect(response.status).toBe('success');
    });

    it('returns error when path is missing', async () => {
      const response = await writeFileTool(workspaceRoot, { content: 'hello' });
      expect(response.status).toBe('error');
      expect(response.error).toContain('writeFile requires a path');
    });

    it('creates parent directories', async () => {
      const response = await writeFileTool(workspaceRoot, { path: 'a/b/c/new.txt', content: 'nested' });
      expect(response.status).toBe('success');
      const content = await fs.readFile(path.join(workspaceRoot, 'a/b/c/new.txt'), 'utf8');
      expect(content).toBe('nested');
    });
  });

  describe('listFilesTool', () => {
    it('lists files and directories successfully', async () => {
      await fs.writeFile(path.join(workspaceRoot, 'a.txt'), 'a');
      await fs.mkdir(path.join(workspaceRoot, 'subdir'));
      await fs.writeFile(path.join(workspaceRoot, 'subdir', 'b.txt'), 'b');

      const response = await listFilesTool(workspaceRoot, {});
      expect(response.status).toBe('success');
      const entries = (response.output as any).entries;
      
      expect(entries).toEqual([
        { path: 'a.txt', type: 'file', size: 1 },
        { path: 'subdir', type: 'dir', size: expect.any(Number) },
        { path: 'subdir/b.txt', type: 'file', size: 1 }
      ]);
    });

    it('skips excluded directories', async () => {
      await fs.mkdir(path.join(workspaceRoot, '.git'));
      await fs.writeFile(path.join(workspaceRoot, '.git', 'config'), 'git config');
      await fs.writeFile(path.join(workspaceRoot, 'tracked.txt'), 'tracked');

      const response = await listFilesTool(workspaceRoot, {});
      const entries = (response.output as any).entries;
      expect(entries).toEqual([
        { path: 'tracked.txt', type: 'file', size: expect.any(Number) }
      ]);
    });

    it('truncates output based on maxEntries', async () => {
      await fs.writeFile(path.join(workspaceRoot, '1.txt'), '1');
      await fs.writeFile(path.join(workspaceRoot, '2.txt'), '2');

      const response = await listFilesTool(workspaceRoot, { maxEntries: 1 });
      expect((response.output as any).truncated).toBe(true);
      expect((response.output as any).entries).toHaveLength(1);
    });

    it('respects maxDepth', async () => {
      await fs.mkdir(path.join(workspaceRoot, 'd1'));
      await fs.mkdir(path.join(workspaceRoot, 'd1', 'd2'));
      await fs.writeFile(path.join(workspaceRoot, 'd1', 'd2', 'file.txt'), 'content');

      const response = await listFilesTool(workspaceRoot, { maxDepth: 1 });
      const entries = (response.output as any).entries;
      expect(entries.find((e: any) => e.path.includes('file.txt'))).toBeUndefined();
    });
  });

  describe('git tools', () => {
    beforeEach(async () => {
      await execaCommand('git init', { cwd: workspaceRoot });
      await execaCommand('git config user.name "Test User"', { cwd: workspaceRoot });
      await execaCommand('git config user.email "test@example.com"', { cwd: workspaceRoot });
    });

    it('commits changes successfully', async () => {
      await fs.writeFile(path.join(workspaceRoot, 'file.txt'), 'content');
      const response = await gitCommitTool(workspaceRoot, { message: 'initial commit' });
      expect(response.status).toBe('success');
      expect((response.output as any).committed).toBe(true);
      expect((response.output as any).hash).toBeTruthy();

      const log = await execaCommand('git log -1 --format=%s', { cwd: workspaceRoot });
      expect(log.stdout).toBe('initial commit');
    });

    it('returns nothing to commit when tree is clean', async () => {
      await fs.writeFile(path.join(workspaceRoot, 'file.txt'), 'content');
      await gitCommitTool(workspaceRoot, { message: 'initial commit' });

      const response2 = await gitCommitTool(workspaceRoot, { message: 'second commit' });
      expect(response2.status).toBe('success');
      expect((response2.output as any).committed).toBe(false);
      expect((response2.output as any).message).toBe('nothing to commit');
    });

    it('commits specific paths', async () => {
      await fs.writeFile(path.join(workspaceRoot, 'file1.txt'), 'content1');
      await fs.writeFile(path.join(workspaceRoot, 'file2.txt'), 'content2');

      const response = await gitCommitTool(workspaceRoot, { message: 'partial commit', paths: ['file1.txt'] });
      expect(response.status).toBe('success');
      
      const status = await execaCommand('git status --porcelain', { cwd: workspaceRoot });
      expect(status.stdout).toContain('?? file2.txt');
    });

    it('returns error when message is missing', async () => {
      const response = await gitCommitTool(workspaceRoot, {});
      expect(response.status).toBe('error');
      expect(response.error).toContain('gitCommit requires a message');
    });

    it('shows unstaged diff', async () => {
      await fs.writeFile(path.join(workspaceRoot, 'file.txt'), 'initial');
      await gitCommitTool(workspaceRoot, { message: 'init' });

      await fs.writeFile(path.join(workspaceRoot, 'file.txt'), 'modified');
      const response = await gitDiffTool(workspaceRoot, {});
      expect(response.status).toBe('success');
      expect((response.output as any).diff).toContain('-initial');
      expect((response.output as any).diff).toContain('+modified');
    });

    it('shows staged diff', async () => {
      await fs.writeFile(path.join(workspaceRoot, 'file.txt'), 'initial');
      await gitCommitTool(workspaceRoot, { message: 'init' });

      await fs.writeFile(path.join(workspaceRoot, 'file.txt'), 'modified');
      await execaCommand('git add file.txt', { cwd: workspaceRoot });

      const response = await gitDiffTool(workspaceRoot, { cached: true });
      expect(response.status).toBe('success');
      expect((response.output as any).diff).toContain('+modified');

      const unstaged = await gitDiffTool(workspaceRoot, {});
      expect((unstaged.output as any).diff).toBe('');
    });
  });

  describe('execCommandTool', () => {
    it('executes a command successfully', async () => {
      const response = await execCommandTool(workspaceRoot, { command: 'echo hello' });
      expect(response.status).toBe('success');
      expect((response.output as any).exitCode).toBe(0);
      expect((response.output as any).stdout.trim()).toBe('hello');
    });

    it('returns error on non-zero exit but preserves output', async () => {
      const response = await execCommandTool(workspaceRoot, { command: 'echo out; echo err >&2; exit 3' });
      expect(response.status).toBe('error');
      expect((response.output as any).exitCode).toBe(3);
      expect((response.output as any).stdout).toContain('out');
      expect((response.output as any).stderr).toContain('err');
    });

    it('handles timeout and preserves partial output', async () => {
      const start = Date.now();
      const response = await execCommandTool(workspaceRoot, { 
        command: 'echo partial; sleep 5', 
        timeoutSeconds: 1 
      });
      const elapsed = Date.now() - start;
      
      expect(response.status).toBe('error');
      expect(response.error).toContain('timed out');
      expect((response.output as any).exitCode).toBe(-1);
      expect((response.output as any).stdout).toContain('partial');
      expect(elapsed).toBeLessThan(3000); 
    });

    it('kills process group on timeout', async () => {
      const markerPath = path.join(workspaceRoot, 'pid.txt');
      const response = await execCommandTool(workspaceRoot, {
        command: `sleep 5 & echo $! > ${markerPath}; sleep 5`,
        timeoutSeconds: 1
      });
      
      expect(response.status).toBe('error');
      
      const pidStr = await fs.readFile(markerPath, 'utf8');
      const pid = parseInt(pidStr.trim(), 10);
      
      expect(() => process.kill(pid, 0)).toThrow(/ESRCH/);
    });

    it('respects cwd argument', async () => {
      await fs.mkdir(path.join(workspaceRoot, 'subdir'));
      const response = await execCommandTool(workspaceRoot, { command: 'pwd', cwd: 'subdir' });
      expect(response.status).toBe('success');
      expect((response.output as any).stdout.trim()).toMatch(/subdir$/);
    });
  });

  describe('executeTool dispatch & catalog', () => {
    it('errors on empty tool name', async () => {
      const response = await executeTool(workspaceRoot, { tool: '  ' });
      expect(response.status).toBe('error');
      expect(response.error).toContain('tool name is required');
    });

    it('errors on unknown tool', async () => {
      const response = await executeTool(workspaceRoot, { tool: 'doesNotExist' });
      expect(response.status).toBe('error');
      expect(response.error).toContain('unsupported tool "doesNotExist"');
    });

    it('returns available tools', async () => {
      const response = await executeTool(workspaceRoot, { tool: 'listAvailableTools' });
      expect(response.status).toBe('success');
      expect((response.output as any).tools.length).toBeGreaterThan(0);
    });

    it('has catalog consistency', async () => {
      const tools = availableTools();
      for (const t of tools) {
        let args: any = {};
        if (t.name === 'execCommand') {
          args = { command: 'echo test' };
        }
        const response = await executeTool(workspaceRoot, { tool: t.name, arguments: args });
        expect(response.error ?? '').not.toMatch(/^unsupported tool/);
      }
    });
  });
});
