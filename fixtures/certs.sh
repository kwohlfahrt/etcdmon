#!/usr/bin/env bash

OUT=$(dirname -- "${BASH_SOURCE[0]}")
openssl req -x509 -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:secp384r1 -days 1 \
	-keyout $OUT/ca.key -out $OUT/ca.crt \
	-subj "/C=GB/ST=London/L=London/O=etcdmon/CN=etcd-ca" -addext "subjectAltName=DNS:etcd-ca" 
for i in $(seq 3); do
	mkdir -p $OUT/$i
	rm $OUT/$i/*
	cp $OUT/ca.{key,crt} $OUT/$i/
done
