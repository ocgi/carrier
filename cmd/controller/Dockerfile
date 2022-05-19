FROM --platform=linux/amd64 centos:centos7
LABEL description="tenc controller"

COPY ./bin/controller controller
ENTRYPOINT ["/controller"]
