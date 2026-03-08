FROM mlikiowa/napcat-docker:base

RUN useradd --no-log-init -d /app napcat

WORKDIR /app

# 从远程拉取所需文件（NapCat.Shell.zip 来自 Release，其余来自主仓库）
RUN apt-get install -y --no-install-recommends git && \
    \
    # 下载 NapCat.Shell.zip（自动获取最新版本）
    LATEST=$(curl -sf https://api.github.com/repos/NapNeko/NapCatQQ/releases/latest \
        | grep '"tag_name"' | cut -d'"' -f4) && \
    curl -L -o /app/NapCat.Shell.zip \
        https://github.com/NapNeko/NapCatQQ/releases/download/${LATEST}/NapCat.Shell.zip && \
    \
    # 从主仓库拉取 entrypoint.sh 和 templates 目录
    git clone --depth=1 --filter=blob:none --sparse \
        https://github.com/NapNeko/NapCat-Docker.git /tmp/napcat-docker && \
    cd /tmp/napcat-docker && \
    git sparse-checkout set entrypoint.sh templates && \
    cp entrypoint.sh /app/entrypoint.sh && \
    cp -r templates /app/templates && \
    \
    # 清理临时文件和 git
    rm -rf /tmp/napcat-docker && \
    apt-get remove -y git && apt-get autoremove -y && apt-get clean

# 安装Linux QQ
RUN arch=$(arch | sed s/aarch64/arm64/ | sed s/x86_64/amd64/) && \
    curl -o linuxqq.deb https://dldir1v6.qq.com/qqfile/qq/QQNT/7516007c/linuxqq_3.2.25-45758_${arch}.deb && \
    dpkg -i --force-depends linuxqq.deb && rm linuxqq.deb && \
    chmod +x entrypoint.sh && \
    echo "(async () => {await import('file:///app/napcat/napcat.mjs');})();" > /opt/QQ/resources/app/loadNapCat.js && \
    sed -i 's|"main": "[^"]*"|"main": "./loadNapCat.js"|' /opt/QQ/resources/app/package.json

VOLUME /app/napcat/config
VOLUME /app/.config/QQ

ENTRYPOINT ["bash", "entrypoint.sh"]
