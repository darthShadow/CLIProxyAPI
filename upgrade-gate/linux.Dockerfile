# Image for the Linux-x64 mode of upgrade-gate/run.sh (see its header).
FROM node:24-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20
RUN apt-get update && apt-get install -y --no-install-recommends curl lsof perl python3 \
	&& rm -rf /var/lib/apt/lists/*
