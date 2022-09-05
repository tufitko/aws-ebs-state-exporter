FROM ubuntu

ARG TARGETARCH

RUN apt-get update && apt-get install ca-certificates -y && update-ca-certificates

COPY exporter /exporter

EXPOSE 8080

ENTRYPOINT [ "/exporter" ]
