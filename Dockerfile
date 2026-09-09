ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.25-alpine
ARG RUNTIME_IMAGE=alpine:3.22
FROM ${NODE_IMAGE} AS frontend-build
WORKDIR /frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM ${GO_IMAGE} AS platform-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=frontend-build /frontend/dist/ ./internal/web/assets/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/resume-platform ./cmd/resume-platform

FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates poppler-utils poppler-data tzdata
WORKDIR /app
COPY --from=platform-build /out/resume-platform /usr/local/bin/resume-platform
RUN mkdir -p /app/media
ENV MEDIA_ROOT=/app/media PLATFORM_ADDRESS=:80
EXPOSE 80
ENTRYPOINT ["/usr/local/bin/resume-platform"]
CMD ["serve"]
