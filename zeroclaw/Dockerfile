FROM ghcr.io/zeroclaw-labs/zeroclaw:debian

USER 65534:65534  # 或原镜像用户
# 安装基础依赖
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

# 创建技能目录
RUN mkdir -p /zeroclaw-data/workspace/skills

WORKDIR /zeroclaw-data

CMD ["zeroclaw"]
