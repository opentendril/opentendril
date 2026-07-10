FROM python:3.12-alpine

ENV PYTHONUNBUFFERED=1

RUN apk add --no-cache git
RUN pip install --no-cache-dir pytest

WORKDIR /app

COPY src/main.py /opt/opentendril-python/main.py

ENTRYPOINT ["python", "/opt/opentendril-python/main.py"]
