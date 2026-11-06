package main

import (
	"archive/zip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var extRE = regexp.MustCompile(`^[a-z0-9_]{1,20}(\.[a-z0-9_]{1,10})?$`)
var assetRE = regexp.MustCompile(`^figure_[0-9]{1,3}\.(png|jpe?g)$`)

// Filee launches native converters such as FFmpeg and LibreOffice. Cancel
// their whole process group as well as the CLI when a request ends or times
// out, otherwise abandoned engines can keep consuming CPU and disk space.
func fileeCommand(ctx context.Context, cli string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

const maxSize = 40 << 20
const shutdownGrace = 150 * time.Second

// Stop accepting new requests on SIGTERM, but let conversions already in
// progress finish before the container is replaced. The grace period exceeds
// the converter's own timeout so a stuck native process is still bounded.
func serveUntilShutdown(server *http.Server, listener net.Listener, signals <-chan os.Signal) error {
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		err := <-finished
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func fail(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func main() {
	limit := runtime.NumCPU()
	// Conversions may start memory-heavy native engines. Keep the default
	// bounded; operators can raise it explicitly for larger instances.
	if limit > 4 {
		limit = 4
	}
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
		cmd := fileeCommand(ctx, cli, "formats", ext, "--json")
		cmd.Env = append(os.Environ(), "HOME="+dir, "FILEE_DATA_DIR="+filepath.Join(dir, "filee-data"))
		output, err := cmd.Output()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fail(w, 504, "Converter timed out")
			return
		}
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
		assets := r.MultipartForm.File["assets"]
		if len(assets) > 50 || (len(assets) > 0 && ext != "md" && ext != "html") {
			fail(w, 400, "Invalid accompanying assets")
			return
		}
		for _, asset := range assets {
			name := asset.Filename
			if !assetRE.MatchString(name) || asset.Size > 2<<20 {
				fail(w, 400, "Invalid accompanying asset")
				return
			}
			source, err := asset.Open()
			if err != nil {
				fail(w, 400, "Cannot read asset")
				return
			}
			dest, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
			if err != nil {
				source.Close()
				fail(w, 400, "Duplicate or invalid asset")
				return
			}
			count, copyErr := io.Copy(dest, io.LimitReader(source, (2<<20)+1))
			source.Close()
			dest.Close()
			if copyErr != nil || count > 2<<20 {
				fail(w, 413, "Asset too large")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		cmd := fileeCommand(ctx, cli, "convert", input, "--to", target, "-o", outdir)
		cmd.Env = append(os.Environ(), "HOME="+dir, "FILEE_DATA_DIR="+filepath.Join(dir, "filee-data"), "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_CACHE_HOME="+filepath.Join(dir, "cache"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fail(w, 504, "Converter timed out")
				return
			}
			log.Printf("conversion failed: %s", output)
			fail(w, 422, "Filee could not convert this file to the requested format")
			return
		}
		// Filee may return a directory tree, e.g. when extracting an archive.
		// Keep paths relative, never follow symlinks, and bound the response.
		var files []string
		var total int64
		err = filepath.WalkDir(outdir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			if total > 200<<20 || len(files) >= 5000 {
				return errors.New("output limit exceeded")
			}
			rel, err := filepath.Rel(outdir, path)
			if err != nil {
				return err
			}
			files = append(files, rel)
			return nil
		})
		if err != nil {
			fail(w, 422, "Filee output is too large or could not be read")
			return
		}
		if len(files) == 0 {
			fail(w, 422, "Filee produced no output")
			return
		}

		if len(files) == 1 && target != "folder" && filepath.Dir(files[0]) == "." {
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
			entry, err := z.Create(filepath.ToSlash(name))
			if err == nil {
				io.Copy(entry, f)
			}
			f.Close()
		}
		z.Close()
	})
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		log.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := serveUntilShutdown(server, listener, signals); err != nil {
		log.Fatal(err)
	}
}
