# Go verifier/CI image.
#
# Unlike sprouts/go/Dockerfile — which builds the sprout-go tool harness once
# and then discards the Go toolchain to hit a minimal runtime budget
# (docs/DESIGN-STATELESS-SPROUT.md) — a deterministic verifier step needs the
# full Go toolchain available at container *runtime* to run `go build` /
# `go test`, so this image keeps it. It is only ever used for the Conductor's
# deterministic command steps (SequenceStep.Command); every LLM-driven Go
# sprout (Worker, Verifier, Debugger) still runs the minimal
# opentendril-go:latest image unchanged.
#
# No ENTRYPOINT / tool protocol here: the Conductor starts this container idle
# (see the CMD below) and execs the verifier command into it directly via
# terrarium.Terrarium.Run, exactly like sprouts/go-fuzz does for Macrophage.
FROM golang:1.26-alpine

# openssh-keygen: the commit-signing tests generate an ephemeral SSH key and
# verify git signs with it. Without ssh-keygen present they skip, and a skipped
# test reports as blocked (never green) under the skip-aware verdict — so the
# signing path would go unverified in every sealed run.
RUN apk add --no-cache git ca-certificates openssh-keygen

WORKDIR /app

CMD ["tail", "-f", "/dev/null"]
