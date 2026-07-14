FROM python:3.12-alpine

ENV PYTHONUNBUFFERED=1

RUN apk add --no-cache git

# Hash-locked test dependency (#257): --require-hashes fails the build on any
# artifact whose sha256 does not match pytest-requirements.txt, so a tampered
# or re-published wheel cannot slip in under an unpinned `pip install pytest`.
COPY pytest-requirements.txt ./pytest-requirements.txt
RUN pip install --no-cache-dir --require-hashes -r pytest-requirements.txt

WORKDIR /app

COPY src/main.py /opt/opentendril-python/main.py

ENTRYPOINT ["python", "/opt/opentendril-python/main.py"]
