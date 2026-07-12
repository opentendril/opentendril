# Go verifier/CI image.
#
# Unlike sprouts/go/Dockerfile — which builds the tendril-go tool harness once
# and then discards the Go toolchain to hit a minimal runtime budget
# (docs/DESIGN-STATELESS-SPROUT.md) — a deterministic verifier step needs the
# full Go toolchain available at container *runtime* to run `go build` /
# `go test`, so this image keeps it. It is only ever used for the Conductor's
# deterministic command steps (SequenceStep.Command); every LLM-driven Go
# sprout (Worker, Verifier-as-agent, Debugger) still runs the minimal
# opentendril-go:latest image unchanged.
#
# No ENTRYPOINT / tool protocol here: the Conductor starts this container idle
# (see the CMD below) and execs the verifier command into it directly via
# terrarium.Terrarium.Run, exactly like sprouts/go-fuzz does for Macrophage.
FROM golang:1.24-alpine

RUN apk add --no-cache git ca-certificates

WORKDIR /app

CMD ["tail", "-f", "/dev/null"]
