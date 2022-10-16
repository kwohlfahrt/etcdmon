#!/usr/bin/env bash
set -euo pipefail

OUT=$(dirname -- "${BASH_SOURCE[0]}")
openssl req -x509 -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:secp384r1 -days 1 \
	-keyout $OUT/ca.key -out $OUT/ca.crt \
	-subj "/C=GB/ST=London/L=London/O=etcdmon/CN=etcd-ca" -addext "subjectAltName=DNS:etcd-ca" 
for i in $(seq 4); do
	rm -rf $OUT/$i/
	mkdir -p $OUT/$i
	cp $OUT/ca.{key,crt} $OUT/$i/
done
