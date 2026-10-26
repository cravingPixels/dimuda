// Package web serves the dashboard page and a JSON API.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

//go:embed templates/*.html
var templates embed.FS

type Source interface {
	Report() (scanner.Report, bool)
	Scanning() bool
	RunScan(ctx context.Context) bool
}

type Server struct {
	src  Source
	tmpl *template.Template
	log  *slog.Logger
	// scanCtx outlives the HTTP request that triggers a manual scan.
	scanCtx context.Context
}

func New(scanCtx context.Context, src Source, log *slog.Logger) (*Server, error) {
	tmpl, err := template.New("").Funcs(funcs).ParseFS(templates, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{src: src, tmpl: tmpl, log: log, scanCtx: scanCtx}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /scan", s.scan)
	mux.HandleFunc("GET /api/report", s.apiReport)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

type summary struct {
	Upstream, Rebuild, Digest, Unpackaged, Errors, Current int
}

type page struct {
	Report   scanner.Report
	Ready    bool
	Scanning bool
	Summary  summary
}

func summarize(r scanner.Report) summary {
	var s summary
	for _, img := range r.Images {
		switch {
		case img.Kind.IsUpstream():
			s.Upstream++
		case img.Kind == versioning.KindRebuild:
			s.Rebuild++
		case img.Kind == versioning.KindDigest:
			s.Digest++
		case img.Err != "":
			s.Errors++
		default:
			s.Current++
		}
		if img.Unpackaged {
			s.Unpackaged++
		}
	}
	return s
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	report, ready := s.src.Report()
	p := page{Report: report, Ready: ready, Scanning: s.src.Scanning(), Summary: summarize(report)}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", p); err != nil {
		s.log.Error("render index", "err", err)
	}
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	go s.src.RunScan(s.scanCtx)
	// Give the goroutine a moment to take the lock so the redirected page shows "scanning".
	time.Sleep(100 * time.Millisecond)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) apiReport(w http.ResponseWriter, _ *http.Request) {
	report, ready := s.src.Report()
	if !ready {
		http.Error(w, "first scan still running", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

var funcs = template.FuncMap{
	"short": func(digest string) string {
		digest = strings.TrimPrefix(digest, "sha256:")
		if len(digest) > 12 {
			return digest[:12]
		}
		return digest
	},
	"join": strings.Join,
	"ago": func(t time.Time) string {
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return pluralize(int(d.Minutes()), "minute") + " ago"
		case d < 48*time.Hour:
			return pluralize(int(d.Hours()), "hour") + " ago"
		}
		return pluralize(int(d.Hours()/24), "day") + " ago"
	},
	"date": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02")
	},
	"upstream": func(k versioning.Kind) bool { return k.IsUpstream() },
	"filterKind": func(img scanner.ImageReport) string {
		switch {
		case img.Kind.IsUpstream():
			return "upstream"
		case img.Kind == versioning.KindRebuild || img.Kind == versioning.KindDigest:
			return string(img.Kind)
		case img.Err != "":
			return "error"
		}
		return "current"
	},
	"lower": strings.ToLower,
}

func pluralize(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
