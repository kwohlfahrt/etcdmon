ARG GOVERSION=1.18.4
FROM golang:${GOVERSION}-alpine AS build

WORKDIR /src
COPY *.go go.mod go.sum
RUN --mount=type=cache,target=/go --mount=type=cache,target=/root/.cache/go-build \
	go build -o /out/etcdmon .

FROM alpine
COPY --from=build /out/etcdmon /
ENTRYPOINT [ "/etcdmon" ]
