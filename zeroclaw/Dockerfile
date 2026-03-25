FROM ghcr.io/zeroclaw-labs/zeroclaw:debian

# 1. 切换回 root，以便安装系统软件包
USER root

# 2. 安装所需依赖（必须用 root）
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    git \
    ca-certificates \
    build-essential \
    python3 \
    python3-pip \
    python3-venv \        
    nodejs \
    npm \
    ffmpeg \             
    jq \                 
    unzip \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# 安全加固：npm 全局包装到非 root 目录
ENV NPM_CONFIG_PREFIX=/zeroclaw-data/.npm-global
ENV PATH="/zeroclaw-data/.npm-global/bin:${PATH}"

# 常用 npm 全局工具（按需取消注释）
# RUN npm install -g tsx typescript

# pip 安全加固：禁止隐式提权
ENV PIP_NO_CACHE_DIR=1 \
    PYTHONDONTWRITEBYTECODE=1

# 预建技能目录结构
RUN mkdir -p \
    /zeroclaw-data/workspace/skills \
    /zeroclaw-data/workspace/memory \
    /zeroclaw-data/workspace/logs \
    /zeroclaw-data/workspace/backups \
    /zeroclaw-data/workspace/temp \
    /zeroclaw-data/.zeroclaw \
    && chown -R 65534:65534 /zeroclaw-data

# 4. 切换回原镜像的非 root 用户
USER 65534:65534

WORKDIR /zeroclaw-data

CMD ["zeroclaw"]
