FROM node:22-alpine AS build

WORKDIR /src/tendrils/node

COPY tendrils/node/package.json tendrils/node/tsconfig.json ./

RUN npm install

COPY tendrils/node/src ./src

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

COPY --from=build /src/tendrils/node/dist /opt/opentendril-node/dist
COPY --from=build /src/tendrils/node/node_modules /opt/opentendril-node/node_modules
COPY --from=build /src/tendrils/node/package.json /opt/opentendril-node/package.json
COPY tendrils/node/entrypoint.sh /opt/opentendril-node/entrypoint.sh

RUN chmod +x /opt/opentendril-node/entrypoint.sh

ENTRYPOINT ["/opt/opentendril-node/entrypoint.sh"]
