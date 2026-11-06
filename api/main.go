package main

import (
	"archive/zip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var extRE = regexp.MustCompile(`^[a-z0-9_]{1,20}(\.[a-z0-9_]{1,10})?$`)

const maxSize = 40 << 20

func fail(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func main() {
	limit := runtime.NumCPU()
	if v, err := strconv.Atoi(os.Getenv("CONVERT_CONCURRENCY")); err == nil && v > 0 {
		limit = v
	}
	if limit > 32 {
		limit = 32
	}
	slots := make(chan struct{}, limit)
	apiKey := os.Getenv("CONVERT_API_KEY")
	cli := os.Getenv("FILEE_CLI")
	if cli == "" {
		cli = "/opt/filee/filee-cli"
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	// Resolve capabilities from the same Filee installation that converts files.
	// Cache successful results: the available engines stay fixed for a container.
	var formats sync.Map
	http.HandleFunc("/formats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			fail(w, 405, "Method not allowed")
			return
		}
		if apiKey != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(apiKey)) != 1 {
			fail(w, 401, "Unauthorized")
			return
		}
		ext := strings.ToLower(r.URL.Query().Get("extension"))
		if !extRE.MatchString(ext) {
			fail(w, 400, "Invalid input extension")
			return
		}
		if cached, ok := formats.Load(ext); ok {
			w.Header().Set("Content-Type", "application/json")
			w.Write(cached.([]byte))
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "5")
			fail(w, 503, "Converter is busy")
			return
		}
		dir, err := os.MkdirTemp("", "filee-formats-")
		if err != nil {
			fail(w, 500, "Cannot create workspace")
			return
		}
		defer os.RemoveAll(dir)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, "formats", ext, "--json")
		cmd.Env = append(os.Environ(), "HOME="+dir, "FILEE_DATA_DIR="+filepath.Join(dir, "filee-data"))
		output, err := cmd.Output()
		if err != nil || !json.Valid(output) {
			fail(w, 422, "Unsupported input format")
			return
		}
		formats.Store(ext, output)
		w.Header().Set("Content-Type", "application/json")
		w.Write(output)
	})
	http.HandleFunc("/convert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fail(w, 405, "Method not allowed")
			return
		}
		if apiKey != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(apiKey)) != 1 {
			fail(w, 401, "Unauthorized")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "5")
			fail(w, 503, "Converter is busy")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxSize+(1<<20))
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			fail(w, 400, "Invalid or oversized upload")
			return
		}
		defer r.MultipartForm.RemoveAll()
		target := strings.ToLower(r.FormValue("format"))
		if !extRE.MatchString(target) {
			fail(w, 400, "Invalid output format")
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			fail(w, 400, "Missing file")
			return
		}
		defer file.Close()
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
		lowerName := strings.ToLower(header.Filename)
		for _, compound := range []string{"tar.gz", "tar.bz2", "tar.xz", "kepub.epub"} {
			if strings.HasSuffix(lowerName, "."+compound) {
				ext = compound
				break
			}
		}
		if !extRE.MatchString(ext) {
			fail(w, 400, "Invalid input extension")
			return
		}
		dir, err := os.MkdirTemp("", "filee-")
		if err != nil {
			fail(w, 500, "Cannot create workspace")
			return
		}
		defer os.RemoveAll(dir)
		input := filepath.Join(dir, "input."+ext)
		outdir := filepath.Join(dir, "out")
		os.Mkdir(outdir, 0700)
		dst, err := os.OpenFile(input, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			fail(w, 500, "Cannot save upload")
			return
		}
		n, err := io.Copy(dst, io.LimitReader(file, maxSize+1))
		dst.Close()
		if err != nil || n > maxSize {
			fail(w, 413, "File too large")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, "convert", input, "--to", target, "-o", outdir)
		cmd.Env = append(os.Environ(), "HOME="+dir, "FILEE_DATA_DIR="+filepath.Join(dir, "filee-data"), "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_CACHE_HOME="+filepath.Join(dir, "cache"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("conversion failed: %s", output)
			fail(w, 422, "Filee could not convert this file to the requested format")
			return
		}
		entries, err := os.ReadDir(outdir)
		if err != nil || len(entries) == 0 {
			fail(w, 422, "Filee produced no output for this conversion")
			return
		}
		var files []string
		for _, e := range entries {
			if e.Type().IsRegular() {
				files = append(files, e.Name())
			}
		}
		if len(files) == 0 {
			fail(w, 422, "Filee produced no output")
			return
		}
		if len(files) == 1 {
			name := files[0]
			f, err := os.Open(filepath.Join(outdir, name))
			if err != nil {
				fail(w, 500, "Cannot read output")
				return
			}
			defer f.Close()
			ct := mime.TypeByExtension(filepath.Ext(name))
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Disposition", `attachment; filename="converted.`+target+`"`)
			io.Copy(w, f)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="converted.zip"`)
		z := zip.NewWriter(w)
		for _, name := range files {
			f, err := os.Open(filepath.Join(outdir, name))
			if err != nil {
				continue
			}
			entry, err := z.Create(name)
			if err == nil {
				io.Copy(entry, f)
			}
			f.Close()
		}
		z.Close()
	})
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
