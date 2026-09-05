# Two builders, one binary. The Vite build writes into backend/web, which is
# what the Go embed directive picks up — same seam as the local build.
FROM node:22-alpine AS frontend
WORKDIR /src
COPY package.json package-lock.json ./
COPY frontend/package.json frontend/package-lock.json ./frontend/
RUN npm --prefix frontend ci
COPY frontend ./frontend
RUN npm --prefix frontend run build

FROM golang:1.23-alpine AS backend
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend ./
COPY --from=frontend /src/backend/web ./web
RUN CGO_ENABLED=0 go build -o /out/hackertracker .

FROM gcr.io/distroless/static-debian12
COPY --from=backend /out/hackertracker /hackertracker
EXPOSE 8080
ENTRYPOINT ["/hackertracker"]
