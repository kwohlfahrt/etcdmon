ARG GOVERSION=1.23.4
FROM golang:${GOVERSION}-alpine AS build

WORKDIR /src
COPY *.go go.mod go.sum ./
COPY pkg/ ./pkg
RUN --mount=type=cache,target=/go --mount=type=cache,target=/root/.cache/go-build \
	go build -o /out/etcdmon .

FROM alpine
COPY --from=build /out/etcdmon /
ENTRYPOINT [ "/etcdmon" ]
