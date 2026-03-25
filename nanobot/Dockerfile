FROM suzukirenz/nanobot:latest

# git 官方镜像已装，这行可以删掉
# RUN apt-get update && apt-get install -y --no-install-recommends git ...

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /app
EXPOSE 18790
ENTRYPOINT ["/entrypoint.sh"]
CMD ["gateway"]
