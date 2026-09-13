# docker build -f services/anti-evasion-service/Dockerfile -t soteria/anti-evasion-service .
FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1
WORKDIR /app
COPY services/anti-evasion-service/requirements.txt ./
RUN pip install -r requirements.txt
COPY services/anti-evasion-service/antievasion ./antievasion
COPY services/anti-evasion-service/sources.yaml ./
COPY services/anti-evasion-service/fixtures ./fixtures
RUN mkdir -p /data && useradd -r -u 10002 antievasion && chown antievasion /data
USER antievasion
ENV DB_PATH=/data/anti-evasion.db PORT=8087 SOURCES=/app/sources.yaml WATCHLIST=/app/fixtures/watchlist.yaml
VOLUME ["/data"]
EXPOSE 8087
ENTRYPOINT ["python", "-m", "antievasion"]
CMD ["run"]
