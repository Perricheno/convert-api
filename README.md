# Convert API

A self-hosted HTTP API for [Filee](https://github.com/KnifeLemon/Filee), built on Filee's CLI and conversion engines. Huge thanks to **KnifeLemon** and the Filee contributors for making the original project available under the MIT license. The original Filee source and its [license](LICENSE) remain in this repository; see [Filee's README](FILEE-README.md) for its features and engine details.

## Run

```sh
docker build -t convert-api ./api
docker run --rm -p 127.0.0.1:8080:8080 -e CONVERT_API_KEY=change-me convert-api
curl -f -X POST http://127.0.0.1:8080/convert \
  -H 'X-API-Key: change-me' -F 'file=@document.docx' -F 'format=pdf' -o document.pdf
```

`GET /health` is the liveness endpoint. `GET /formats?extension=docx` returns Filee’s available target formats for that input, with optional-engine targets listed separately; it requires the API key when configured. `POST /convert` accepts one multipart `file` (up to 40 MiB) and `format` (extension). It returns the converted file, or a ZIP if conversion creates multiple files or an extracted directory. Output is capped at 200 MiB and 5,000 files. Availability and fidelity depend on Filee's format matrix and installed engines; unsupported pairs return an error. Requests are synchronous and time out after 120 seconds. Set `CONVERT_CONCURRENCY` to control simultaneous conversions; excess requests receive 503 and `Retry-After`. Each request gets an isolated temporary workspace which is removed afterwards. Run multiple instances behind a load balancer to scale horizontally. Set `CONVERT_API_KEY` and send it in `X-API-Key`; if unset the conversion endpoint is unauthenticated, suitable only for a trusted internal network. Keep the API private or add TLS, authentication, rate limits, and stronger per-job sandboxing at the edge before exposing it publicly.

The container runs as a non-root user and uses a pinned, checksum-verified Linux x64 Filee release, plus LibreOffice, Pandoc, Ghostscript and FFmpeg. The app does not execute uploaded scripts. Conversion of untrusted documents still involves third-party parsers: use resource limits and network isolation. The default image targets Linux x64.

## Keeping up with Filee

The upstream Filee source history is retained. To incorporate upstream source changes: `git remote add upstream https://github.com/KnifeLemon/Filee.git`, then `git fetch upstream` and merge the desired upstream branch into your branch, resolving conflicts and running tests. The scheduled source-sync workflow also proposes upstream source changes as a pull request when they merge cleanly; conflicts need manual resolution. The API image pins its own Filee release and checksum in `api/Dockerfile`; `.github/workflows/update-filee.yml` checks upstream releases and proposes a version update as a pull request. Review the checksum and test the update before merging and deploying it. It does not silently update a running instance.
