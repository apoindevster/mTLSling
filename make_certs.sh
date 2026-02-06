#!/bin/bash

mkdir -p certs
openssl req -x509 -newkey rsa:2048 -keyout certs/ca.key -out certs/ca.crt -days 365 -nodes -subj "/CN=ca"

cat > certs/openssl.cnf <<'CONF'
[ req ]
default_bits       = 2048
distinguished_name = req_distinguished_name
prompt             = no
req_extensions     = req_ext

[ req_distinguished_name ]
CN = transfer.local

[ req_ext ]
subjectAltName = @alt_names

[ alt_names ]
DNS.1 = transfer.local
DNS.2 = localhost
IP.1  = 127.0.0.1
CONF

openssl req -newkey rsa:2048 -nodes -keyout certs/server.key -out certs/server.csr -config certs/openssl.cnf
openssl x509 -req -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key -CAcreateserial \
  -out certs/server.crt -days 365 -extfile certs/openssl.cnf -extensions req_ext

openssl req -newkey rsa:2048 -nodes -keyout certs/client.key -out certs/client.csr -subj "/CN=client"
openssl x509 -req -in certs/client.csr -CA certs/ca.crt -CAkey certs/ca.key -CAcreateserial \
  -out certs/client.crt -days 365

