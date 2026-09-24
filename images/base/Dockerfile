# 基座电脑。部署端拉取此镜像后才能创建电脑。
# PID 1 由控制器以 microsandbox --init auto 交给 systemd。
# 虚拟机里不启用 sshd。SSH 在控制器侧接入。

FROM ghcr.io/superradcompany/debian-systemd:12

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        locales \
        openssh-client \
        python3 \
        python-is-python3 \
        ripgrep \
        sudo \
        vim \
        build-essential \
    && rm -rf /var/lib/apt/lists/* \
    && printf 'en_US.UTF-8 UTF-8\n' > /etc/locale.gen \
    && locale-gen

# uid 1000。每台电脑的登录用户。免密 sudo，因为这台电脑属于进来的人。
RUN if id ubuntu >/dev/null 2>&1; then \
        usermod -l box -d /home/box -m ubuntu && groupmod -n box ubuntu; \
    else \
        useradd --create-home --shell /bin/bash --uid 1000 box; \
    fi \
    && printf 'box ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/box \
    && chmod 440 /etc/sudoers.d/box \
    && su - box -c 'git config --global init.defaultBranch main'

# 空机器号。克隆镜像后每台电脑第一次启动自己生成，避免撞车。
# 保留空文件而不是删除，避免 systemd 把这次启动当成首次安装并重置 unit。
RUN : > /etc/machine-id && ln -sf /etc/machine-id /var/lib/dbus/machine-id

# 磁盘扩大后，根分区在下次启动时跟着长。
RUN printf '/dev/vda / ext4 defaults,x-systemd.growfs 0 1\n' > /etc/fstab

EXPOSE 8000

LABEL org.opencontainers.image.title="box-base" \
      org.opencontainers.image.description="Base computer image. Pull onto a node before creating computers." \
      box.login-user="box" \
      box.default-port="8000"

WORKDIR /home/box
USER box
CMD ["/bin/bash"]
