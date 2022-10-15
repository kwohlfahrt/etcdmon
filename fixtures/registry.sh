#!/usr/bin/env bash
set -euo pipefail

# Manually run a custom registry, until this is automated: https://github.com/kubernetes-sigs/kind/issues/1213

CTX=$(dirname -- "${BASH_SOURCE[0]}")/..
if [[ $(docker inspect -f '{{.State.Running}}' registry || true) != true ]]; then
	docker run --rm --detach --publish 5000:5000 --expose 5000 --name registry registry:2
fi
if ! docker network inspect -f '{{.Name }}' kind; then
	docker network create kind
fi
if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' registry)" = 'null' ]; then
	docker network connect "kind" registry
fi

docker build --push -t 127.0.0.1:5000/etcdmon "$CTX"
