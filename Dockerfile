FROM ubuntu:22.04

LABEL maintainer="modelz-support@tensorchord.ai"

COPY controller /usr/bin/controller
ENTRYPOINT ["/usr/bin/controller"]
