FROM ghcr.io/astral-sh/uv:0.9.18@sha256:5713fa8217f92b80223bc83aac7db36ec80a84437dbc0d04bbc659cae030d8c9 AS uv
FROM ghcr.io/osgeo/gdal:ubuntu-small-3.11.5@sha256:5557af93bbedb58f69481c6c4a147e49eae2ce7d590a26c64b586195da65fb5b

WORKDIR /app
ENV PATH="/opt/geo-worker/bin:${PATH}" PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PROJ_NETWORK=OFF
COPY requirements.lock .
# Keep the GDAL bindings that ship with the base image.
RUN --mount=from=uv,source=/uv,target=/bin/uv \
    uv venv --python /usr/bin/python3 --system-site-packages /opt/geo-worker && \
    uv pip install --python /opt/geo-worker/bin/python --no-deps --no-cache -r requirements.lock

COPY *.py .
CMD ["python", "worker.py"]
