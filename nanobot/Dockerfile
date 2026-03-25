FROM suzukirenz/nanobot:latest

# 安装 git（如果基础镜像里没有）
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*

# 拷贝启动脚本
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /app

EXPOSE 18790

ENTRYPOINT ["/entrypoint.sh"]
CMD ["gateway"]
