#!/usr/bin/env python3
"""Pin the latest upstream release and its published Linux x64 checksum."""
import json, re, urllib.request
from pathlib import Path

def get(url):
    req = urllib.request.Request(url, headers={"User-Agent": "convert-api-updater", "Accept": "application/vnd.github+json"})
    with urllib.request.urlopen(req, timeout=30) as response:
        return response.read()
release = json.loads(get("https://api.github.com/repos/KnifeLemon/Filee/releases/latest"))
version = release["tag_name"].removeprefix("v")
if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
    raise SystemExit("Unexpected release tag")
archive = f"Filee-{version}-linux-x64.tar.gz"
checksums = next(a["browser_download_url"] for a in release["assets"] if a["name"] == f"Filee-{version}-linux-x64-SHA256SUMS.txt")
lines = get(checksums).decode().splitlines()
sha = next(line.split()[0] for line in lines if line.split()[-1].lstrip("*") == archive)
if not re.fullmatch(r"[a-fA-F0-9]{64}", sha):
    raise SystemExit("Invalid checksum")
path = Path(__file__).with_name("Dockerfile")
text = path.read_text()
text = re.sub(r"(?m)^ARG FILEE_VERSION=.*$", f"ARG FILEE_VERSION={version}", text)
text = re.sub(r"(?m)^ARG FILEE_SHA256=.*$", f"ARG FILEE_SHA256={sha.lower()}", text)
path.write_text(text)
