# A host for the integration tests: sshd, the scp program, and sudo for a login
# user who may run scp as root. A second user, keyless, may log in but holds no
# private key, so it reaches the other host only through a forwarded agent. Each
# container gets its own host keys at start.
FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587

RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server openssh-client sudo procps \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --shell /bin/bash tester \
 && useradd --create-home --shell /bin/bash keyless \
 && printf 'tester ALL=(root) NOPASSWD: /usr/bin/scp\n' > /etc/sudoers.d/tester \
 && chmod 0440 /etc/sudoers.d/tester \
 && mkdir -p /run/sshd /srv/root-only \
 && chmod 0755 /srv/root-only

COPY sshd.conf /etc/ssh/sshd_config.d/test.conf
COPY ssh.conf /etc/ssh/ssh_config.d/test.conf
COPY entrypoint.sh /usr/local/bin/entrypoint.sh

EXPOSE 22
HEALTHCHECK --interval=1s --timeout=2s --retries=30 CMD pgrep -x sshd >/dev/null
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
