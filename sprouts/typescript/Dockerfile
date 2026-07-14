FROM node:22-alpine AS build

WORKDIR /src/sprouts/typescript

COPY sprouts/typescript/package.json sprouts/typescript/package-lock.json sprouts/typescript/tsconfig.json ./

# npm ci installs the exact, integrity-checked tree from package-lock.json and
# fails the build on any hash mismatch — a tampered or re-published registry
# tarball cannot slip in under a caret range (#257).
RUN npm ci

COPY sprouts/typescript/src ./src

RUN npm run build && npm prune --omit=dev

FROM node:22-alpine

ENV NODE_ENV=production

WORKDIR /app

COPY --from=build /src/sprouts/typescript/dist /opt/opentendril-typescript/dist
COPY --from=build /src/sprouts/typescript/node_modules /opt/opentendril-typescript/node_modules
COPY --from=build /src/sprouts/typescript/package.json /opt/opentendril-typescript/package.json

ENTRYPOINT ["node", "/opt/opentendril-typescript/dist/main.js"]
