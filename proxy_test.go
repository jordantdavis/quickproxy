package main

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// echoBackend returns the path, query and selected headers it received.
func echoBackend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/42/redirect":
			http.Redirect(w, r, "/api/42/landed", http.StatusFound)
			return
		case "/api/42/cookie":
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "1", Path: "/api/42"})
			return
		}
		io.WriteString(w, r.URL.RequestURI()+"|"+r.Header.Get("X-Forwarded-Prefix")+"|"+r.Header.Get("X-Forwarded-Proto"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestProxy(t *testing.T, routes map[string]string) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer(mustTable(t, routes), slog.New(slog.NewTextHandler(io.Discard, nil)))
	front := httptest.NewServer(s)
	t.Cleanup(front.Close)
	return s, front
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func get(t *testing.T, url string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := noRedirect().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestProxy(t *testing.T) {
	be := echoBackend(t)
	_, front := newTestProxy(t, map[string]string{
		"/plain":         be.URL,
		"/in/{val}/path": be.URL + "/api/{val}",
		"/down":          "http://127.0.0.1:1",
	})

	_, body := get(t, front.URL+"/plain/a/b?x=1", "X-Forwarded-Proto", "https")
	if body != "/a/b?x=1|/plain|https" {
		t.Errorf("plain: %q", body)
	}
	_, body = get(t, front.URL+"/in/42/path/items?y=2")
	if body != "/api/42/items?y=2|/in/42/path|http" {
		t.Errorf("param: %q", body)
	}

	resp, _ := get(t, front.URL+"/in/42/path?q=1")
	if resp.StatusCode != 308 || resp.Header.Get("Location") != "/in/42/path/?q=1" {
		t.Errorf("trailing slash redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, _ = get(t, front.URL+"/in/42/path/redirect")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/in/42/path/landed" {
		t.Errorf("location rewrite: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, _ = get(t, front.URL+"/in/42/path/cookie")
	if c := resp.Header.Get("Set-Cookie"); !strings.Contains(c, "Path=/in/42/path") {
		t.Errorf("cookie path: %q", c)
	}

	for path, want := range map[string]int{"/nope/": 404, "/down/": 502, "/_health": 200, "/": 200, "/plain/%2e%2e/x": 400} {
		if resp, _ := get(t, front.URL+path); resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestWebSocketUpgrade(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" || r.Header.Get("Upgrade") != "test" {
			http.Error(w, "bad", 400)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: test\r\nConnection: Upgrade\r\n\r\n")
		brw.Flush()
		line, _ := brw.ReadString('\n')
		brw.WriteString("echo " + line)
		brw.Flush()
	}))
	defer be.Close()
	_, front := newTestProxy(t, map[string]string{"/app/{x}": be.URL})

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /app/1/ws HTTP/1.1\r\nHost: x\r\nUpgrade: test\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	io.WriteString(conn, "ping\n")
	if line, _ := br.ReadString('\n'); line != "echo ping\n" {
		t.Errorf("tunnel: %q", line)
	}
}

func TestSetTable(t *testing.T) {
	be := echoBackend(t)
	s, front := newTestProxy(t, map[string]string{"/old": be.URL})
	s.SetTable(mustTable(t, map[string]string{"/new": be.URL}))
	if resp, _ := get(t, front.URL+"/old/"); resp.StatusCode != 404 {
		t.Errorf("old route still served: %d", resp.StatusCode)
	}
	if resp, _ := get(t, front.URL+"/new/"); resp.StatusCode != 200 {
		t.Errorf("new route: %d", resp.StatusCode)
	}
}
