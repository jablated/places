# places — single Go binary (API + embedded SvelteKit frontend), built static
# and shipped on distroless. Pure-Go SQLite (modernc.org/sqlite) means we can
# build with CGO_ENABLED=0 and run on distroless/static.
FROM golang:1.23-alpine AS build

# Build the SvelteKit frontend first so we can copy it into the Go source tree
# for embedding.
RUN apk add --no-cache nodejs npm

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

# Stage the built assets where go:embed can find them.
WORKDIR /src
COPY api/go.mod api/go.sum ./api/
RUN cd api && go mod download

COPY api/ ./api/
RUN rm -rf api/webdist && mkdir -p api/webdist && cp -r web/build/. api/webdist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /places ./api/

# :nonroot runs as UID/GID 65532 — paired with the pod securityContext in
# deployment.yaml.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /places /places
EXPOSE 8080
ENTRYPOINT ["/places"]
