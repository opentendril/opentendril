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
# Grammar/runtime versions are integrity-pinned in package-lock.json and
# installed with `npm ci`, which fails the build if any resolved artifact's
# hash does not match the lockfile — a re-published or tampered registry
# tarball can no longer slip in under the same version string (#257). Every
# dependency is installed at build time: at runtime the container has no
# network. The npm grammar packages ship prebuilt .wasm files, which we copy
# to a stable path so parse.js never depends on npm package layout at runtime.
#
# No ENTRYPOINT / tool protocol here: the Conductor starts this container idle
# (see the CMD below) and execs parse.js into it directly via
# terrarium.Terrarium.Run, exactly like sprouts/go-verifier does.
FROM node:22-alpine

WORKDIR /opt/opentendril

COPY package.json package-lock.json ./

RUN npm ci --no-audit --no-fund \
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
