# Base computer. A node must pull this image before it can create computers.
# The controller runs it with podman --systemd=always. sshd is the login path.
# Client keys are not in the image. The controller writes them onto the volume.
# The server's GitHub key is also written at start, not baked in here.

FROM fedora:44

ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    container=podman

RUN dnf -y upgrade && dnf -y install \
        bash \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        openssh-clients \
        openssh-server \
        python3 \
        ripgrep \
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
RUN mise use --global go@latest rust@stable \
    && mise exec -- go version \
    && mise exec -- rustc --version \
    && mise exec -- cargo --version
USER root

# Loopback only. The controller publishes this port. Password login stays off.
RUN mkdir -p /etc/ssh/sshd_config.d \
    && printf '%s\n' \
        'Port 2222' \
        'ListenAddress 127.0.0.1' \
        'PasswordAuthentication no' \
        'KbdInteractiveAuthentication no' \
        'PermitRootLogin no' \
        'AllowUsers box' \
        > /etc/ssh/sshd_config.d/box.conf \
    && ssh-keygen -A \
    && systemctl enable sshd.service

# Empty machine id. Each computer generates its own on first boot.
# Keep the empty file so systemd does not treat boot as a first install.
RUN : > /etc/machine-id && ln -sf /etc/machine-id /var/lib/dbus/machine-id

EXPOSE 2222

LABEL org.opencontainers.image.title="box-base" \
      org.opencontainers.image.description="Base computer image. Pull onto a node before creating computers." \
      box.login-user="box" \
      box.ssh-port="2222"

STOPSIGNAL SIGRTMIN+3
WORKDIR /home/box
CMD ["/usr/sbin/init"]
