# run `make build` to build local/elemental-toolkit image
ARG TOOLKIT_REPO=local/elemental-toolkit
ARG VERSION=latest
ARG OS_IMAGE=registry.opensuse.org/opensuse/tumbleweed
ARG OS_VERSION=latest

FROM ${TOOLKIT_REPO}:${VERSION} AS toolkit

# OS base image of our choice
FROM ${OS_IMAGE}:${OS_VERSION} AS os
ARG REPO
ARG VERSION
ENV REPO=${REPO}
ENV VERSION=${VERSION}

# Add bleeding edge repo for snapper
RUN zypper ar https://download.opensuse.org/repositories/home:/aschnell:/snapper/openSUSE_Tumbleweed/home:aschnell:snapper.repo

# Install kernel, systemd, dracut, grub2 and other required tools
RUN ARCH=$(uname -m); \
    if [[ "${ARCH}" != "riscv64" ]]; then \
      ADD_PKGS+=" shim"; \
      [[ "${ARCH}" == "aarch64" ]] && ARCH="arm64"; \
    fi; \
    zypper --non-interactive removerepo repo-update || true; \
    zypper --non-interactive --gpg-auto-import-keys install --no-recommends -- \
      kernel-default \
      device-mapper \
      dracut \
      grub2 \
      grub2-${ARCH}-efi \
      haveged \
      systemd \
      NetworkManager \
      openssh-server \
      openssh-clients \
      timezone \
      parted \
      e2fsprogs \
      dosfstools \
      mtools \
      xorriso \
      findutils \
      gptfdisk \
      rsync \
      squashfs \
      lvm2 \
      tar \
      gzip \
      vim \
      which \
      less \
      sudo \
      curl \
      sed \
      iproute2 \
      podman \
      audit \
      patterns-microos-selinux \
      btrfsprogs \
      btrfsmaintenance \
      snapper \
      xterm-resize \
      ${ADD_PKGS} && \
    zypper clean --all

# Just add the elemental cli
COPY --from=toolkit /usr/bin/elemental /usr/bin/elemental

# Enable essential services
RUN systemctl enable NetworkManager.service && \
    systemctl enable sshd.service

# Workaround to make sure there are no pending sysusers to be created (boo#1231244)
RUN systemd-sysusers

# This is for automatic testing purposes, do not do this in production.
RUN echo "PermitRootLogin yes" > /etc/ssh/sshd_config.d/rootlogin.conf

# SELinux in enforce mode
#RUN sed -i "s|SELINUX=.*|SELINUX=enforcing|g" /etc/selinux/config

# Add default snapshotter setup
ADD snapshotter.yaml /etc/elemental/config.d/snapshotter.yaml

# Add specific Grub bootargs for RISC-V
ADD riscv_bootargs.cfg /tmp/riscv_bootargs.cfg
RUN ARCH=$(uname -m); \
    [[ "${ARCH}" == "riscv64" ]] && mv -f /tmp/riscv_bootargs.cfg /etc/elemental/bootargs.cfg || true

# Generate initrd with required elemental services
RUN ARCH=$(uname -m); \
    # We want to keep the default features for x86_64 to test them
    if [[ "${ARCH}" != "x86_64" ]]; then \
      # riscv64 needs a specific Grub configuration and arm64 needs some specific firmwares
      FEATURES="autologin boot-assessment cloud-config-defaults cloud-config-essentials dracut-config elemental-rootfs elemental-setup elemental-sysroot grub-config"; \
      [[ "${ARCH}" == "aarch64" ]] && FEATURES+=" arm-firmware grub-default-bootargs"; \
    fi; \
    elemental --debug init --force ${FEATURES}

# Update os-release file with some metadata
RUN echo IMAGE_REPO=\"${REPO}\"             >> /etc/os-release && \
    echo IMAGE_TAG=\"${VERSION}\"           >> /etc/os-release && \
    echo IMAGE=\"${REPO}:${VERSION}\"       >> /etc/os-release && \
    echo TIMESTAMP="`date +'%Y%m%d%H%M%S'`" >> /etc/os-release && \
    echo GRUB_ENTRY_NAME=\"Elemental\"      >> /etc/os-release

# Good for validation after the build
CMD ["/bin/bash"]
