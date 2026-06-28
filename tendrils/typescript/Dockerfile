FROM node:22-alpine AS build

WORKDIR /src/tendrils/typescript

COPY tendrils/typescript/package.json tendrils/typescript/tsconfig.json ./

RUN npm install

COPY tendrils/typescript/src ./src

RUN npm run build && npm prune --omit=dev

FROM node:22-alpine

ENV NODE_ENV=production

WORKDIR /app

COPY --from=build /src/tendrils/typescript/dist /opt/opentendril-typescript/dist
COPY --from=build /src/tendrils/typescript/node_modules /opt/opentendril-typescript/node_modules
COPY --from=build /src/tendrils/typescript/package.json /opt/opentendril-typescript/package.json

ENTRYPOINT ["node", "/opt/opentendril-typescript/dist/main.js"]
