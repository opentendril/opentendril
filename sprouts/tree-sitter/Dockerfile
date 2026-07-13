# Tree-sitter terrarium image.
#
# Gives Rhizome high-fidelity symbols for non-Go languages (Python,
# JavaScript, TypeScript, TSX). The Conductor runs this container ONCE per
# scan as a repo-level batch pre-pass (conductor/treesitter.go): the workspace
# is mounted read-only at /app with --network none, and parse.js emits NDJSON
# symbol rows that the host wraps in a rhizome.PrecomputedParser. Go files
# keep the native go/ast parser; anything this image cannot cover falls back
# to the regex parser — so a released binary with no Docker still works.
#
# All grammar/runtime versions are pinned exactly for determinism, and every
# dependency is installed at build time: at runtime the container has no
# network. The npm grammar packages ship prebuilt .wasm files, which we copy
# to a stable path so parse.js never depends on npm package layout at runtime.
#
# No ENTRYPOINT / tool protocol here: the Conductor starts this container idle
# (see the CMD below) and execs parse.js into it directly via
# terrarium.Terrarium.Run, exactly like sprouts/go-verifier does.
FROM node:22-alpine

WORKDIR /opt/opentendril

RUN npm install --no-save --no-audit --no-fund \
      web-tree-sitter@0.25.10 \
      tree-sitter-python@0.23.6 \
      tree-sitter-javascript@0.23.1 \
      tree-sitter-typescript@0.23.2 \
 && mkdir -p grammars \
 && cp node_modules/tree-sitter-python/tree-sitter-python.wasm grammars/ \
 && cp node_modules/tree-sitter-javascript/tree-sitter-javascript.wasm grammars/ \
 && cp node_modules/tree-sitter-typescript/tree-sitter-typescript.wasm grammars/ \
 && cp node_modules/tree-sitter-typescript/tree-sitter-tsx.wasm grammars/ \
 && test -f grammars/tree-sitter-python.wasm \
 && test -f grammars/tree-sitter-javascript.wasm \
 && test -f grammars/tree-sitter-typescript.wasm \
 && test -f grammars/tree-sitter-tsx.wasm

COPY parse.js /opt/opentendril/parse.js

WORKDIR /app

CMD ["tail", "-f", "/dev/null"]
