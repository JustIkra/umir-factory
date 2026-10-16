# syntax=docker/dockerfile:1
FROM docker.io/library/alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

RUN apk add --no-cache bash curl openssl python3 postgresql17-client redis

COPY secrets.sh pg-roles.sh redis-init.sh /usr/local/bin/
COPY rabbitmq.conf pg_hba.conf /usr/local/share/durable/
RUN chmod 0755 /usr/local/bin/secrets.sh /usr/local/bin/pg-roles.sh /usr/local/bin/redis-init.sh

ENTRYPOINT ["/bin/bash"]
