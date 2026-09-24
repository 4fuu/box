# Base computer. A node must pull this image before it can create computers.
# The controller runs it with podman --systemd=always. sshd is the login path.
# Client keys are not in the image. The controller writes them onto the volume.

FROM debian:12

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    container=podman

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        locales \
        openssh-server \
        python3 \
        python-is-python3 \
        ripgrep \
        sudo \
        systemd \
        systemd-sysv \
        vim \
        build-essential \
    && rm -rf /var/lib/apt/lists/* \
    && printf 'en_US.UTF-8 UTF-8\n' > /etc/locale.gen \
    && locale-gen \
    && rm -f /usr/sbin/policy-rc.d

RUN useradd --create-home --shell /bin/bash --uid 1000 box \
    && printf 'box ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/box \
    && chmod 440 /etc/sudoers.d/box \
    && su - box -c 'git config --global init.defaultBranch main'

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
    && systemctl enable ssh.service

# Empty machine id. Each computer generates its own on first boot.
# Keep the empty file so systemd does not treat boot as a first install.
RUN : > /etc/machine-id && ln -sf /etc/machine-id /var/lib/dbus/machine-id

# Persistent state lives on the volume the controller mounts here.
RUN mkdir -p /var/lib/box \
    && chown box:box /var/lib/box

EXPOSE 2222 8000

LABEL org.opencontainers.image.title="box-base" \
      org.opencontainers.image.description="Base computer image. Pull onto a node before creating computers." \
      box.login-user="box" \
      box.ssh-port="2222" \
      box.default-port="8000"

STOPSIGNAL SIGRTMIN+3
WORKDIR /home/box
USER root
CMD ["/sbin/init"]
