package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
)

// serveFile answers a request for a file route. The route serves one regular
// file at the prefix itself. Paths below the prefix, directories and other
// kinds of files give 404.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, m *Match) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The file can change at any time, for example when an agent edits a
	// plan. no-cache makes the browser ask again; a 304 is still possible.
	w.Header().Set("Cache-Control", "no-cache")
	if m.Rest != "" {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(m.Route.fsPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Error("file route", "pattern", m.Route.Pattern, "path", m.Route.fsPath, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		s.log.Error("file route: not a regular file", "pattern", m.Route.Pattern, "path", m.Route.fsPath)
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// redirectToSlash sends a 308 to the prefix with a trailing slash, and keeps
// the query string.
func redirectToSlash(w http.ResponseWriter, r *http.Request, m *Match) {
	target := m.Prefix + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}
