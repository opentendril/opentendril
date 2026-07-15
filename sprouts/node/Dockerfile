FROM node:22-alpine AS build

WORKDIR /src/sprouts/node

COPY sprouts/node/package.json sprouts/node/package-lock.json sprouts/node/tsconfig.json ./

# npm ci installs the exact, integrity-checked tree from package-lock.json and
# fails the build on any hash mismatch — a tampered or re-published registry
# tarball cannot slip in under a caret range.
RUN npm ci

COPY sprouts/node/src ./src

RUN npm run build && npm prune --omit=dev

FROM debian:bookworm-slim

ENV NODE_ENV=production
ENV NVM_DIR=/usr/local/nvm
ENV NODE_VERSION=22.15.0

RUN apt-get update && apt-get install -y curl bash ca-certificates git && \
    mkdir -p $NVM_DIR && \
    curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.7/install.sh | bash && \
    . $NVM_DIR/nvm.sh && \
    nvm install $NODE_VERSION && \
    nvm alias default $NODE_VERSION && \
    nvm use default && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

ENV PATH=$NVM_DIR/v$NODE_VERSION/bin:$PATH

WORKDIR /app

COPY --from=build /src/sprouts/node/dist /opt/opentendril-node/dist
COPY --from=build /src/sprouts/node/node_modules /opt/opentendril-node/node_modules
COPY --from=build /src/sprouts/node/package.json /opt/opentendril-node/package.json
COPY sprouts/node/entrypoint.sh /opt/opentendril-node/entrypoint.sh

RUN chmod +x /opt/opentendril-node/entrypoint.sh

ENTRYPOINT ["/opt/opentendril-node/entrypoint.sh"]
