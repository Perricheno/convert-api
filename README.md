# Convert API

A self-hosted HTTP API for [Filee](https://github.com/KnifeLemon/Filee), built on Filee's CLI and conversion engines. Huge thanks to **KnifeLemon** and the Filee contributors for making the original project available under the MIT license. The original Filee source and its [license](LICENSE) remain in this repository; see [Filee's README](FILEE-README.md) for its features and engine details.

## Run

```sh
docker build -t convert-api ./api
docker run --rm -p 127.0.0.1:8080:8080 -e CONVERT_API_KEY=change-me convert-api
curl -f -X POST http://127.0.0.1:8080/convert \
  -H 'X-API-Key: change-me' -F 'file=@document.docx' -F 'format=pdf' -o document.pdf
```

`GET /health` is the liveness endpoint. `GET /formats?extension=docx` returns Filee’s available target formats for that input, with optional-engine targets listed separately; it requires the API key when configured. `POST /convert` accepts exactly one multipart `file` (up to 40 MiB) and `format` (extension); multiple input files are rejected rather than ignored. It returns the converted file, or a ZIP if conversion creates multiple files or an extracted directory. Output is capped at 200 MiB and 5,000 files. Availability and fidelity depend on Filee's format matrix and installed engines; unsupported pairs return an error. When Filee finds no ruled PDF tables for CSV/XLSX, HTTP 422 includes `code: PDF_TABLES_NOT_FOUND`; scanned or borderless tables may not be detected. Markdown and HTML inputs may include up to 50 multipart `assets` files named `figure_0.png`, `figure_1.jpg`, `figure_2.pdf`, etc. (maximum 30 MiB each; total multipart request at most 41 MiB); reference those names in the source. Requests are synchronous and time out after 120 seconds. The default concurrency is the smaller of the CPU count and four. Set `CONVERT_CONCURRENCY` to control simultaneous conversions; excess requests receive 503 and `Retry-After`. Each request gets an isolated temporary workspace which is removed afterwards. Run multiple instances behind a load balancer to scale horizontally. Set `CONVERT_API_KEY` and send it in `X-API-Key`; if unset the conversion endpoint is unauthenticated, suitable only for a trusted internal network. Keep the API private or add TLS, authentication, rate limits, and stronger per-job sandboxing at the edge before exposing it publicly.

On SIGTERM the API stops accepting requests and allows up to 150 seconds for active requests to finish. Configure the container runtime's stop grace period to at least 165 seconds so it does not kill active work prematurely.

The container runs as a non-root user and uses a pinned, checksum-verified Linux x64 Filee release, plus LibreOffice, Pandoc, Ghostscript and Filee’s checksum-verified GPL FFmpeg build from [BtbN](https://github.com/BtbN/FFmpeg-Builds). The app does not execute uploaded scripts. Conversion of untrusted documents still involves third-party parsers: use resource limits and network isolation. The default image targets Linux x64.

The current image pins Filee **2.5.0**. This adds upstream resource limits for
malformed documents and archives, plus document and media conversion improvements.
It does not guarantee faster conversion for every format. Compare representative
documents on the same host when upgrading.

## Keeping up with Filee

The upstream Filee source history is retained. To incorporate upstream source changes: `git remote add upstream https://github.com/KnifeLemon/Filee.git`, then `git fetch upstream` and merge the desired upstream branch into your branch, resolving conflicts and running tests. The scheduled source-sync workflow also proposes upstream source changes as a pull request when they merge cleanly; conflicts need manual resolution. The API image pins its own Filee release and checksum in `api/Dockerfile`; `.github/workflows/update-filee.yml` checks upstream releases and proposes a version update as a pull request. Both update workflows explicitly start the API smoke test on the proposed branch. Review its result and the update before merging and deploying it. It does not silently update a running instance.

Release metadata downloads retry transient HTTP/network failures up to three times
with bounded backoff. Permanent errors and invalid metadata still stop the update;
failed downloads leave the Dockerfile unchanged. Test this boundary with
`python3 -m unittest discover -s api -p test_update_filee.py`.

## Server workspace

Keep host-local maintenance material inside this checkout's ignored `.local/`
directory: `.local/worktrees/` for linked Git worktrees, `.local/update-runs/`
for build logs and verification evidence, and `.local/maintenance/` for archived
one-off scripts. Use `git worktree move` when relocating a registered worktree.
Archived scripts and reports preserve their original contents and historical
paths; they describe completed operations and are not reusable deployment commands.
The private `.local/README.md` records the current host layout and migration details.

The existing Perricheno deployment still uses `/root/perricheno/filee-service/`
and `/root/perricheno/scripts/update-filee-service.sh`. Its systemd updater is
independent of these maintenance archives.
