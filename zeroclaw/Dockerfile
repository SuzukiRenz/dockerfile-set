FROM ghcr.io/zeroclaw-labs/zeroclaw:debian

# 1. 切换回 root，以便安装系统软件包
USER root

# 2. 安装所需依赖（必须用 root）
RUN apt update && apt install -y \
    curl \
    git \
    ca-certificates \
    build-essential \
    python3 \
    python3-pip \
    nodejs \
    npm \
    && rm -rf /var/lib/apt/lists/*

# 可选：升级 npm
# RUN npm install -g npm@latest

# 3. 创建技能目录并确保归属正确
RUN mkdir -p /zeroclaw-data/workspace/skills && \
    chown -R 65534:65534 /zeroclaw-data/workspace/skills

# 4. 切换回原镜像的非 root 用户
USER 65534:65534

WORKDIR /zeroclaw-data

CMD ["zeroclaw"]
