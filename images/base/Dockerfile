# Base computer. A node must pull this image before it can create computers.
# The release workflow copies a linux box binary into this directory before
# building. This image does not compile one.
# The controller runs it with podman --systemd=always. sshd is the login path.
# Client keys are not in the image. The controller writes them onto the volume.
# The server's GitHub key is also written at start, not baked in here.

FROM fedora:44

ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    container=podman

# Agent tools, not a desktop. fd-find ships as fd. gh reads GH_TOKEN from the
# environment the controller injects at start, so it does not need gh auth login.
RUN dnf -y upgrade && dnf -y install \
        bash \
        ca-certificates \
        curl \
        git \
        gh \
        jq \
        less \
        openssh-clients \
        openssh-server \
        ripgrep \
        fd-find \
        bat \
        fzf \
        zoxide \
        hyperfine \
        tokei \
        dust \
        sudo \
        systemd \
        vim \
        gcc \
        make \
        tar \
        gzip \
        unzip \
        xz \
        which \
        findutils \
        procps-ng \
        iproute \
        bind-utils \
        rsync \
    && dnf clean all

# Fedora has no sd package. The skill expects the find-and-replace tool on PATH.
ARG TARGETARCH
RUN case "$TARGETARCH" in \
        amd64) triple=x86_64-unknown-linux-musl ;; \
        arm64) triple=aarch64-unknown-linux-musl ;; \
        *) echo "no sd build for $TARGETARCH" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/sd.tar.gz "https://github.com/chmln/sd/releases/download/v1.1.0/sd-v1.1.0-${triple}.tar.gz" \
    && mkdir -p /tmp/sd \
    && tar -xzf /tmp/sd.tar.gz -C /tmp/sd \
    && find /tmp/sd -type f -name sd -exec install -m 0755 {} /usr/local/bin/sd \; \
    && rm -rf /tmp/sd /tmp/sd.tar.gz \
    && test -x /usr/local/bin/sd

# Official mise binary. Go and Rust come from mise's built-in backends,
# installed into the box user's data dir so the toolchain survives on the volume.
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh \
    && mise --version

RUN useradd --create-home --shell /bin/bash --uid 1000 box \
    && printf 'box ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/box \
    && chmod 440 /etc/sudoers.d/box \
    && su - box -c 'git config --global init.defaultBranch main' \
    && printf '%s\n' 'eval "$(mise activate bash)"' >> /home/box/.bashrc \
    && chown box:box /home/box/.bashrc

# Persistent toolchain and home state. The controller mounts the volume here
# and bind-mounts it over the paths the user actually writes to.
RUN mkdir -p /var/lib/box/home /var/lib/box/mise \
    && chown -R box:box /var/lib/box

USER box
RUN mise use --global go@latest rust@stable node@lts python@3 \
    && mise exec -- go version \
    && mise exec -- rustc --version \
    && mise exec -- cargo --version \
    && mise exec -- node --version \
    && mise exec -- python3 --version
USER root

# The image has no host key and no client keys. The controller mounts a
# per-computer host key and authorized_keys. sshd will not start without that mount.
COPY skills/box/SKILL.md /home/box/.agents/skills/box/SKILL.md
RUN chown -R box:box /home/box/.agents

# Loopback only. The controller publishes this port. Password login stays off.
RUN mkdir -p /etc/ssh/sshd_config.d \
    && printf '%s\n' \
        'Port 2222' \
        'ListenAddress 127.0.0.1' \
        'PasswordAuthentication no' \
        'KbdInteractiveAuthentication no' \
        'PermitRootLogin no' \
        'AllowUsers box' \
        'HostKey /etc/ssh/ssh_host_ed25519_key' \
        'AuthorizedKeysFile /home/box/.ssh/authorized_keys' \
        > /etc/ssh/sshd_config.d/box.conf \
    && systemctl enable sshd.service

# Empty machine id. Each computer generates its own on first boot.
# Keep the empty file so systemd does not treat boot as a first install.
RUN mkdir -p /var/lib/dbus \
    && : > /etc/machine-id \
    && ln -sf /etc/machine-id /var/lib/dbus/machine-id

COPY box /usr/local/bin/box
RUN chmod 0755 /usr/local/bin/box

EXPOSE 2222

LABEL org.opencontainers.image.title="box-base" \
      org.opencontainers.image.description="Base computer image. Pull onto a node before creating computers." \
      box.login-user="box" \
      box.ssh-port="2222"

STOPSIGNAL SIGRTMIN+3
WORKDIR /home/box
CMD ["/usr/sbin/init"]
