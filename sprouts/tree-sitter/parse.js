#!/usr/bin/env node
// OpenTendril tree-sitter terrarium batch parser.
//
// The Conductor mounts a workspace read-only at /app and execs this script
// once per Rhizome scan (a repo-level batch pre-pass, not per-file). It walks
// the workspace, parses every Python/JavaScript/TypeScript/TSX source file
// with pinned web-tree-sitter grammars, and emits NDJSON to stdout — one line
// per successfully parsed file, sorted by path:
//
//   {"path":"src/x.py","symbols":[{"name","type","lineStart","lineEnd","stub"}]}
//
// Contract notes (the Go side, conductor/treesitter.go, depends on these):
//   - `type` is constrained to the Rhizome symbol vocabulary: function,
//     method, class, interface, type, struct, file_context.
//   - Paths are relative to /app, slash-separated.
//   - A file that fails to parse is OMITTED (logged to stderr) so the host
//     falls through to the regex parser for it — one bad file must never
//     sink the batch.
//   - The directory skip list mirrors rhizome/scanner.go shouldSkipPath.
'use strict';

const fs = require('fs');
const path = require('path');
const { Parser, Language } = require('web-tree-sitter');

const GRAMMAR_DIR = process.env.OTTS_GRAMMAR_DIR || '/opt/opentendril/grammars';

// Mirrors rhizome/scanner.go shouldSkipPath (case-insensitive path segments).
const SKIP_SEGMENTS = new Set([
  '.git', 'node_modules', '.tendrilignore', 'venv', '.venv',
  'vendor', 'dist', 'build', '__pycache__',
]);

// Files larger than this are left to the host-side regex parser.
const MAX_FILE_BYTES = 2 * 1024 * 1024;
const MAX_STUB_LENGTH = 300;

const LANGUAGE_BY_EXTENSION = {
  '.py': 'python',
  '.js': 'javascript',
  '.jsx': 'javascript',
  '.mjs': 'javascript',
  '.cjs': 'javascript',
  '.ts': 'typescript',
  '.mts': 'typescript',
  '.cts': 'typescript',
  '.tsx': 'tsx',
};

const WASM_BY_LANGUAGE = {
  python: 'tree-sitter-python.wasm',
  javascript: 'tree-sitter-javascript.wasm',
  typescript: 'tree-sitter-typescript.wasm',
  tsx: 'tree-sitter-tsx.wasm',
};

function firstLine(text) {
  const index = text.indexOf('\n');
  return (index === -1 ? text : text.slice(0, index)).trim();
}

function capStub(text) {
  const trimmed = text.trim();
  if (trimmed.length <= MAX_STUB_LENGTH) {
    return trimmed;
  }
  return trimmed.slice(0, MAX_STUB_LENGTH) + '…';
}

// signatureOf renders the declaration head — everything before the body —
// collapsed onto one line, so multi-line parameter lists stay readable stubs.
function signatureOf(node, source) {
  const body = node.childForFieldName('body');
  const end = body ? body.startIndex : node.endIndex;
  const raw = source.slice(node.startIndex, end);
  return capStub(firstJoined(raw));
}

function firstJoined(raw) {
  return raw
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')
    .join(' ');
}

// makeSymbolRange records a symbol whose line span starts at startNode and
// ends at endNode. They differ when a declaration carries decorators or an
// `export` prefix: the stub and lineStart come from the widened head node so
// `@Injectable() export class Foo` reads as one unit, while lineEnd still
// tracks the declaration body.
function makeSymbolRange(name, type, startNode, endNode, stub) {
  return {
    name,
    type,
    lineStart: startNode.startPosition.row + 1,
    lineEnd: endNode.endPosition.row + 1,
    stub,
  };
}

function fieldText(node, field) {
  const child = node.childForFieldName(field);
  return child ? child.text : '';
}

// --- Python -----------------------------------------------------------------

function pythonDocstring(node) {
  const body = node.childForFieldName('body');
  if (!body || body.namedChildren.length === 0) {
    return '';
  }
  const first = body.namedChildren[0];
  if (first.type !== 'expression_statement' || first.namedChildren.length === 0) {
    return '';
  }
  const candidate = first.namedChildren[0];
  if (candidate.type !== 'string') {
    return '';
  }
  return firstLine(candidate.text);
}

function walkPython(node, insideClass, source, symbols, imports) {
  switch (node.type) {
    case 'import_statement':
    case 'import_from_statement':
      imports.push(firstLine(node.text));
      return;
    case 'decorated_definition': {
      // The decorators are siblings of the wrapped definition; fold their
      // source lines into the stub and let the decorated_definition node own
      // the line span so lineStart points at the first decorator.
      const decorators = node.namedChildren
        .filter((child) => child.type === 'decorator')
        .map((child) => firstLine(child.text));
      const definition = node.namedChildren.find(
        (child) => child.type === 'class_definition' || child.type === 'function_definition',
      );
      if (definition) {
        emitPythonDefinition(definition, node, insideClass, decorators, source, symbols, imports);
      }
      return;
    }
    case 'class_definition':
    case 'function_definition':
      emitPythonDefinition(node, node, insideClass, [], source, symbols, imports);
      return;
    default:
      // Every other container falls through here so nested declarations keep
      // their enclosing-class context.
      for (const child of node.namedChildren) {
        walkPython(child, insideClass, source, symbols, imports);
      }
  }
}

// emitPythonDefinition records one class/function symbol and then walks its
// body for nested declarations. rangeNode is the decorated_definition when
// decorators are present (so the line span covers the decorator lines),
// otherwise the definition itself. A class body yields methods; a function
// body yields functions.
function emitPythonDefinition(definition, rangeNode, insideClass, decorators, source, symbols, imports) {
  const isClass = definition.type === 'class_definition';
  const name = fieldText(definition, 'name');
  if (name) {
    let stub = signatureOf(definition, source);
    if (decorators.length > 0) {
      stub = decorators.join('\n') + '\n' + stub;
    }
    const doc = pythonDocstring(definition);
    if (doc) {
      stub = doc + '\n' + stub;
    }
    const type = isClass ? 'class' : insideClass ? 'method' : 'function';
    symbols.push(makeSymbolRange(name, type, rangeNode, rangeNode, stub));
  }
  const body = definition.childForFieldName('body');
  if (body) {
    for (const child of body.namedChildren) {
      walkPython(child, isClass, source, symbols, imports);
    }
  }
}

// --- JavaScript / TypeScript / TSX -------------------------------------------

function scriptDocComment(node) {
  let anchor = node;
  if (anchor.parent && anchor.parent.type === 'export_statement') {
    anchor = anchor.parent;
  }
  // A JSDoc block sits above any method decorators, so step past them.
  let sibling = anchor.previousNamedSibling;
  while (sibling && sibling.type === 'decorator') {
    sibling = sibling.previousNamedSibling;
  }
  if (sibling && sibling.type === 'comment' && sibling.text.startsWith('/**')) {
    return capStub(sibling.text);
  }
  return '';
}

// precedingDecorators returns the contiguous decorator nodes immediately
// before node. TypeScript/JavaScript attach method decorators as previous
// siblings; class decorators instead live inside the export_statement and are
// captured by widening to that parent (see scriptHeadNode).
function precedingDecorators(node) {
  const decorators = [];
  let sibling = node.previousNamedSibling;
  while (sibling && sibling.type === 'decorator') {
    decorators.unshift(sibling);
    sibling = sibling.previousNamedSibling;
  }
  return decorators;
}

// scriptHeadNode widens node to the syntax that should open its stub: an
// enclosing `export`/`export default` statement (which also encloses class
// decorators), or the first of any preceding decorator siblings. This is what
// makes stubs read `export class Foo`, `@Injectable() export class Foo`, or
// `@log method(...)` instead of dropping the modifier.
function scriptHeadNode(node) {
  if (node.parent && node.parent.type === 'export_statement') {
    return node.parent;
  }
  const decorators = precedingDecorators(node);
  if (decorators.length > 0) {
    return decorators[0];
  }
  return node;
}

// arrowHeadNode widens an arrow/function-expression variable_declarator to the
// declaration keyword and any `export` prefix, so its stub carries the same
// modifiers a function_declaration would. It refuses to widen a multi-binding
// declaration (`const a = ..., b = ...`), where a shared head would wrongly
// fold every binding into each symbol's stub.
function arrowHeadNode(declarator) {
  const declaration = declarator.parent;
  if (
    !declaration ||
    (declaration.type !== 'lexical_declaration' && declaration.type !== 'variable_declaration')
  ) {
    return declarator;
  }
  const bindings = declaration.namedChildren.filter((child) => child.type === 'variable_declarator');
  if (bindings.length !== 1) {
    return declarator;
  }
  if (declaration.parent && declaration.parent.type === 'export_statement') {
    return declaration.parent;
  }
  return declaration;
}

function pushScriptSymbol(symbols, name, type, node, source) {
  const head = scriptHeadNode(node);
  const body = node.childForFieldName('body');
  const end = body ? body.startIndex : node.endIndex;
  let stub = capStub(firstJoined(source.slice(head.startIndex, end)));
  const doc = scriptDocComment(node);
  if (doc) {
    stub = doc + '\n' + stub;
  }
  symbols.push(makeSymbolRange(name, type, head, node, stub));
}

function walkScript(node, source, symbols, imports) {
  switch (node.type) {
    case 'import_statement':
      imports.push(firstLine(node.text));
      return;
    case 'class_declaration':
    case 'abstract_class_declaration': {
      const name = fieldText(node, 'name');
      if (name) {
        pushScriptSymbol(symbols, name, 'class', node, source);
      }
      break;
    }
    case 'method_definition': {
      const name = fieldText(node, 'name');
      if (name) {
        pushScriptSymbol(symbols, name, 'method', node, source);
      }
      break;
    }
    case 'function_declaration':
    case 'generator_function_declaration':
    case 'function_signature': {
      const name = fieldText(node, 'name');
      if (name) {
        pushScriptSymbol(symbols, name, 'function', node, source);
      }
      break;
    }
    case 'interface_declaration': {
      const name = fieldText(node, 'name');
      if (name) {
        pushScriptSymbol(symbols, name, 'interface', node, source);
      }
      break;
    }
    case 'type_alias_declaration':
    case 'enum_declaration': {
      const name = fieldText(node, 'name');
      if (name) {
        pushScriptSymbol(symbols, name, 'type', node, source);
      }
      break;
    }
    case 'variable_declarator': {
      const value = node.childForFieldName('value');
      const name = fieldText(node, 'name');
      if (name && value && (value.type === 'arrow_function' || value.type === 'function_expression' || value.type === 'function')) {
        // Widen to the `const`/`let` (and any `export`) so the stub reads
        // `export const identity = (value) =>`, matching function-declaration
        // fidelity. Only widen for a single-declarator statement — a shared
        // `const a = ..., b = ...` must not fold both arrows into one stub.
        const head = arrowHeadNode(node);
        const bodyStart = value.childForFieldName('body')
          ? value.childForFieldName('body').startIndex
          : value.endIndex;
        let stub = capStub(firstJoined(source.slice(head.startIndex, bodyStart)));
        const doc = scriptDocComment(node.parent || node);
        if (doc) {
          stub = doc + '\n' + stub;
        }
        symbols.push(makeSymbolRange(name, 'function', head, node, stub));
      }
      break;
    }
    default:
      break;
  }
  for (const child of node.namedChildren) {
    walkScript(child, source, symbols, imports);
  }
}

// --- Extraction --------------------------------------------------------------

function extractSymbols(tree, source, languageName) {
  const symbols = [];
  const imports = [];
  if (languageName === 'python') {
    walkPython(tree.rootNode, false, source, symbols, imports);
  } else {
    walkScript(tree.rootNode, source, symbols, imports);
  }
  if (imports.length > 0) {
    symbols.unshift({
      name: 'file_context',
      type: 'file_context',
      lineStart: 1,
      lineEnd: 1,
      stub: capStub('Imports: ' + imports.join(', ')),
    });
  }
  return symbols;
}

// --- Workspace walk ----------------------------------------------------------

function collectFiles(rootDir, relativeDir, out) {
  let entries;
  try {
    entries = fs.readdirSync(path.join(rootDir, relativeDir), { withFileTypes: true });
  } catch (err) {
    process.stderr.write(`tree-sitter: skipping directory ${relativeDir || '.'}: ${err.message}\n`);
    return;
  }
  for (const entry of entries) {
    if (SKIP_SEGMENTS.has(entry.name.toLowerCase())) {
      continue;
    }
    const relativePath = relativeDir ? relativeDir + '/' + entry.name : entry.name;
    if (entry.isSymbolicLink()) {
      continue;
    }
    if (entry.isDirectory()) {
      collectFiles(rootDir, relativePath, out);
      continue;
    }
    if (!entry.isFile()) {
      continue;
    }
    const extension = path.extname(entry.name).toLowerCase();
    if (!(extension in LANGUAGE_BY_EXTENSION)) {
      continue;
    }
    out.push(relativePath);
  }
}

async function main() {
  const root = process.argv[2] || '/app';

  await Parser.init();
  const parser = new Parser();
  const loadedLanguages = {};

  const files = [];
  collectFiles(root, '', files);
  files.sort();

  for (const relativePath of files) {
    try {
      const absolutePath = path.join(root, relativePath);
      if (fs.statSync(absolutePath).size > MAX_FILE_BYTES) {
        process.stderr.write(`tree-sitter: skipping ${relativePath}: exceeds size cap\n`);
        continue;
      }
      const languageName = LANGUAGE_BY_EXTENSION[path.extname(relativePath).toLowerCase()];
      if (!loadedLanguages[languageName]) {
        loadedLanguages[languageName] = await Language.load(path.join(GRAMMAR_DIR, WASM_BY_LANGUAGE[languageName]));
      }
      parser.setLanguage(loadedLanguages[languageName]);

      const source = fs.readFileSync(absolutePath, 'utf8');
      const tree = parser.parse(source);
      if (!tree) {
        process.stderr.write(`tree-sitter: skipping ${relativePath}: parser returned no tree\n`);
        continue;
      }
      const symbols = extractSymbols(tree, source, languageName);
      tree.delete();

      process.stdout.write(JSON.stringify({ path: relativePath, symbols }) + '\n');
    } catch (err) {
      // Omit the file: the host-side regex parser still covers it.
      process.stderr.write(`tree-sitter: skipping ${relativePath}: ${err.message}\n`);
    }
  }
}

main().catch((err) => {
  process.stderr.write(`tree-sitter: fatal: ${err && err.stack ? err.stack : err}\n`);
  process.exit(1);
});
