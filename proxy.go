package main

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type ctxKey struct{}

// Server routes requests by path prefix to backend apps.
type Server struct {
	table atomic.Pointer[Table]
	proxy *httputil.ReverseProxy
	log   *slog.Logger
}

// NewServer returns a Server that uses t until SetTable is called.
func NewServer(t *Table, log *slog.Logger) *Server {
	s := &Server{log: log}
	s.table.Store(t)
	s.proxy = &httputil.ReverseProxy{
		Rewrite:        rewrite,
		ModifyResponse: modifyResponse,
		FlushInterval:  -1, // stream (SSE etc.) without buffering
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			m := r.Context().Value(ctxKey{}).(*Match)
			log.Error("backend error", "pattern", m.Route.Pattern, "target", m.Route.Target, "err", err)
			http.Error(w, "502 bad gateway: backend for "+m.Route.Pattern+" is not reachable", http.StatusBadGateway)
		},
	}
	return s
}

// SetTable swaps in a new route table. Safe during live requests.
func (s *Server) SetTable(t *Table) { s.table.Store(t) }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w}
	pattern, backend := "-", "-"
	defer func() {
		s.log.Info("request", "method", r.Method, "path", r.URL.RequestURI(), "pattern", pattern,
			"backend", backend, "status", sw.status, "dur", time.Since(start).Round(time.Microsecond))
	}()

	raw := r.URL.EscapedPath()
	switch {
	case raw == "/_health":
		sw.Write([]byte("ok\n"))
		return
	case raw == "/":
		s.serveIndex(sw)
		return
	case hasDotSegment(raw):
		http.Error(sw, "400 bad request: dot segments in path", http.StatusBadRequest)
		return
	}

	m, ok := s.table.Load().Match(raw)
	if !ok {
		http.NotFound(sw, r)
		return
	}
	pattern = m.Route.Pattern
	if m.Rest == "" {
		target := m.Prefix + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(sw, r, target, http.StatusPermanentRedirect)
		return
	}
	backend = m.Route.scheme + "://" + m.Route.host + m.BackendPath()
	s.proxy.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxKey{}, m)))
}

func rewrite(pr *httputil.ProxyRequest) {
	m := pr.In.Context().Value(ctxKey{}).(*Match)
	raw := m.BackendPath()
	path, err := url.PathUnescape(raw)
	if err != nil {
		path = raw
	}
	pr.Out.URL.Scheme = m.Route.scheme
	pr.Out.URL.Host = m.Route.host
	pr.Out.URL.Path = path
	pr.Out.URL.RawPath = raw
	pr.Out.Host = "" // send the backend's own host

	// This proxy listens on loopback behind `tailscale serve`, so the inbound
	// forwarding headers come from Tailscale and are kept.
	if xff := pr.In.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		pr.Out.Header["X-Forwarded-For"] = xff
	}
	pr.SetXForwarded()
	for _, h := range []string{"X-Forwarded-Host", "X-Forwarded-Proto"} {
		if v := pr.In.Header.Get(h); v != "" {
			pr.Out.Header.Set(h, v)
		}
	}
	pr.Out.Header.Set("X-Forwarded-Prefix", m.Prefix)
}

func modifyResponse(resp *http.Response) error {
	m := resp.Request.Context().Value(ctxKey{}).(*Match)
	if loc := resp.Header.Get("Location"); loc != "" {
		resp.Header.Set("Location", m.rewriteLocation(loc))
	}
	if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
		out := make([]string, len(cookies))
		for i, line := range cookies {
			out[i] = line
			c, err := http.ParseSetCookie(line)
			if err != nil || c.Path == "" {
				continue
			}
			if p, ok := m.publicPath(c.Path); ok && p != c.Path {
				c.Path = p
				out[i] = c.String()
			}
		}
		resp.Header["Set-Cookie"] = out
	}
	return nil
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>quickproxy</title>
<style>body{font:15px system-ui,sans-serif;margin:2rem auto;max-width:48rem;padding:0 1rem}
td{padding:.25rem 1rem .25rem 0}code{font-size:.95em}</style></head>
<body><h1>quickproxy</h1><table>
{{range .}}<tr><td>{{if .Link}}<a href="{{.Link}}"><code>{{.Pattern}}</code></a>{{else}}<code>{{.Pattern}}</code>{{end}}</td><td><code>{{.Target}}</code></td></tr>
{{else}}<tr><td>No routes.</td></tr>{{end}}
</table></body></html>
`))

func (s *Server) serveIndex(w http.ResponseWriter) {
	type row struct{ Pattern, Target, Link string }
	var rows []row
	for _, r := range s.table.Load().Routes() {
		link := ""
		if !strings.Contains(r.Pattern, "{") {
			link = r.Pattern + "/"
		}
		rows = append(rows, row{r.Pattern, r.Target, link})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	indexTmpl.Execute(w, rows)
}

// statusWriter records the response status. Unwrap lets http.ResponseController
// reach Flush and Hijack on the real writer (needed for SSE and WebSockets).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
