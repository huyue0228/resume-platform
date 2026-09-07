ARG POSTGRES_IMAGE=postgres:16
FROM ${POSTGRES_IMAGE}

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates coreutils findutils restic \
    && rm -rf /var/lib/apt/lists/*

COPY ops/backup/container/ /opt/srf-backup/
RUN chmod 0755 /opt/srf-backup/*.sh

ENTRYPOINT ["/opt/srf-backup/entrypoint.sh"]
CMD ["backup"]
