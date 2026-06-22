FROM python:3.12-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends gcc g++ git curl nodejs npm && \
    npm install -g @modelcontextprotocol/server-github && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY src/ ./src/

RUN adduser --disabled-password --gecos '' tendril && \
    mkdir -p /app/data/dynamic_skills /app/logs /data /logs /workspace && \
    chown -R tendril:tendril /app /data /logs /workspace && \
    chmod -R 755 /app

USER tendril

# The Go Stem spawns this container as an ephemeral CLI worker:
#   docker run --rm ... core-tendril:latest -m src.tendrilloop
# No long-running server; ENTRYPOINT is python for direct -m invocation.
ENTRYPOINT ["python"]
