// Package opentendril carries the build inputs for the Sprout images inside the
// Stem binary itself.
//
// The Stem builds its own Sprout images, so it needs the Dockerfiles and the
// handful of files they copy. It used to find them by asking runtime.Caller
// where this source tree sat on the machine that compiled it — a path that is
// correct only when the binary is run from the tree it was built in, and
// meaningless for an installed one. An installed Stem therefore could not start
// a Sprout at all, and if the build machine's home directory had been readable
// it would instead have built its images out of whatever that tree happened to
// contain at the time.
//
// Embedding makes the answer to "what did this Stem build its Sprouts from?" a
// property of the binary rather than of the filesystem it is running on. It
// also means a build input that stops being embedded fails the build here,
// rather than silently resolving to a path that happens to exist.
//
// This package lives at the module root because that is the only place from
// which go:embed can reach go.mod and go.sum, which the Go Sprout image builds
// against. It deliberately holds nothing else.
package opentendril

import "embed"

// SproutBuildInputs holds exactly the files the Sprout image builds consume,
// laid out at the same paths the Dockerfiles expect. Materialised into a
// temporary directory at build time, it stands in for the repository checkout
// the build context used to be taken from.
//
// The set is explicit rather than a whole-tree embed: the sprouts directory
// also carries virtual environments, installed modules and test caches that run
// to gigabytes and that no image build reads.
//
//go:embed go.mod go.sum
//go:embed sprouts/go/Dockerfile sprouts/go/main.go
//go:embed sprouts/typescript/Dockerfile sprouts/typescript/package.json sprouts/typescript/package-lock.json sprouts/typescript/tsconfig.json sprouts/typescript/src
//go:embed sprouts/node/Dockerfile sprouts/node/package.json sprouts/node/package-lock.json sprouts/node/entrypoint.sh
//go:embed sprouts/python/Dockerfile sprouts/python/pytest-requirements.txt sprouts/python/src/main.py
//go:embed toolchains/go-fuzz/Dockerfile toolchains/go-verifier/Dockerfile
var SproutBuildInputs embed.FS
