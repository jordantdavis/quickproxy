package main

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var (
	// paramSegRe matches a pattern segment that is a whole parameter, e.g. "{val}".
	paramSegRe = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	// placeholderRe finds parameter references inside a target path segment.
	placeholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// segment is one part of a route pattern: a literal (decoded) or a parameter name.
type segment struct {
	lit   string
	param string
}

// tplPart is one piece of a target path template: escaped literal text or a parameter.
type tplPart struct {
	lit   string
	param string
}

// Route maps a public path pattern to a backend URL.
type Route struct {
	Pattern string
	Target  string

	segs   []segment
	scheme string
	host   string
	tpl    []tplPart // backend base path template; empty means "/"
}

// Match is the result of matching a request path against the table.
// All paths are in escaped (wire) form.
type Match struct {
	Route  *Route
	Params map[string]string
	Prefix string // public prefix with values filled in, e.g. /in/42/path
	Base   string // backend base path, e.g. /api/42 ("" for the root)
	Rest   string // remainder after the prefix, "" or starting with "/"
}

// BackendPath is the escaped path sent to the backend.
func (m *Match) BackendPath() string {
	if p := m.Base + m.Rest; p != "" {
		return p
	}
	return "/"
}

// Table is an immutable, precedence-sorted set of routes.
type Table struct {
	routes []*Route
}

// Routes returns the routes in match order.
func (t *Table) Routes() []*Route { return t.routes }

// NewTable compiles and validates a pattern -> target map.
func NewTable(m map[string]string) (*Table, error) {
	var errs []error
	shapes := map[string]string{}
	t := &Table{}
	for pattern, target := range m {
		r, err := compileRoute(pattern, target)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		shape := shapeKey(r.segs)
		if other, dup := shapes[shape]; dup {
			errs = append(errs, fmt.Errorf("route %q: same shape as %q", pattern, other))
			continue
		}
		shapes[shape] = pattern
		t.routes = append(t.routes, r)
	}
	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
		return nil, errors.Join(errs...)
	}
	sort.Slice(t.routes, func(i, j int) bool { return lessSpecific(t.routes[i].segs, t.routes[j].segs) })
	return t, nil
}

// lessSpecific orders a before b when a must be tried first:
// more segments first, then a literal beats a parameter at the first difference.
func lessSpecific(a, b []segment) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	for k := range a {
		ap, bp := a[k].param != "", b[k].param != ""
		if ap != bp {
			return !ap
		}
		if !ap && a[k].lit != b[k].lit {
			return a[k].lit < b[k].lit // deterministic order only
		}
	}
	return false
}

// shapeKey identifies patterns that would match exactly the same requests.
func shapeKey(segs []segment) string {
	var b strings.Builder
	for _, s := range segs {
		if s.param != "" {
			b.WriteString("\x00p")
		} else {
			b.WriteString("\x00l" + s.lit)
		}
	}
	return b.String()
}

func compileRoute(pattern, target string) (*Route, error) {
	segs, err := parsePattern(pattern)
	if err != nil {
		return nil, fmt.Errorf("route %q: %w", pattern, err)
	}
	params := map[string]bool{}
	for _, s := range segs {
		if s.param != "" {
			params[s.param] = true
		}
	}

	u, err := url.Parse(target)
	switch {
	case err != nil:
		return nil, fmt.Errorf("route %q: bad target: %w", pattern, err)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("route %q: target scheme must be http or https", pattern)
	case u.Host == "":
		return nil, fmt.Errorf("route %q: target has no host", pattern)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return nil, fmt.Errorf("route %q: target must not have a query or fragment", pattern)
	}

	tpl, err := parseTemplate(strings.TrimSuffix(u.Path, "/"), params)
	if err != nil {
		return nil, fmt.Errorf("route %q: %w", pattern, err)
	}
	return &Route{Pattern: pattern, Target: target, segs: segs, scheme: u.Scheme, host: u.Host, tpl: tpl}, nil
}

func parsePattern(p string) ([]segment, error) {
	switch {
	case !strings.HasPrefix(p, "/"):
		return nil, errors.New("must start with /")
	case p == "/":
		return nil, errors.New(`"/" is reserved for the index page`)
	case strings.HasSuffix(p, "/"):
		return nil, errors.New("must not end with /")
	}
	var segs []segment
	seen := map[string]bool{}
	for _, part := range strings.Split(p[1:], "/") {
		switch {
		case part == "":
			return nil, errors.New("empty segment")
		case part == "." || part == "..":
			return nil, errors.New(`"." and ".." segments are not allowed`)
		case strings.ContainsAny(part, "{}"):
			m := paramSegRe.FindStringSubmatch(part)
			if m == nil {
				return nil, fmt.Errorf("segment %q: a parameter must be a whole segment like {name}", part)
			}
			if seen[m[1]] {
				return nil, fmt.Errorf("parameter {%s} used twice", m[1])
			}
			seen[m[1]] = true
			segs = append(segs, segment{param: m[1]})
		default:
			lit, err := url.PathUnescape(part)
			if err != nil {
				return nil, fmt.Errorf("segment %q: %w", part, err)
			}
			segs = append(segs, segment{lit: lit})
		}
	}
	if p == "/_health" {
		return nil, errors.New(`"/_health" is reserved`)
	}
	return segs, nil
}

// parseTemplate turns a decoded target path like "/api/{val}" into template parts.
func parseTemplate(p string, params map[string]bool) ([]tplPart, error) {
	if p == "" {
		return nil, nil
	}
	var parts []tplPart
	addLit := func(s string) {
		if n := len(parts); n > 0 && parts[n-1].param == "" {
			parts[n-1].lit += s
		} else {
			parts = append(parts, tplPart{lit: s})
		}
	}
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		addLit("/")
		last := 0
		for _, loc := range placeholderRe.FindAllStringSubmatchIndex(seg, -1) {
			name := seg[loc[2]:loc[3]]
			if !params[name] {
				return nil, fmt.Errorf("target uses {%s}, which the pattern does not define", name)
			}
			addLit(url.PathEscape(seg[last:loc[0]]))
			parts = append(parts, tplPart{param: name})
			last = loc[1]
		}
		addLit(url.PathEscape(seg[last:]))
	}
	return parts, nil
}

func (r *Route) buildBase(vals map[string]string) string {
	var b strings.Builder
	for _, p := range r.tpl {
		if p.param != "" {
			b.WriteString(vals[p.param])
		} else {
			b.WriteString(p.lit)
		}
	}
	return b.String()
}

// Match finds the most specific route for an escaped request path.
func (t *Table) Match(rawPath string) (*Match, bool) {
	if !strings.HasPrefix(rawPath, "/") {
		return nil, false
	}
	raw := strings.Split(rawPath[1:], "/")
	dec := make([]string, len(raw))
	for i, s := range raw {
		if d, err := url.PathUnescape(s); err == nil {
			dec[i] = d
		} else {
			dec[i] = s
		}
	}

	for _, rt := range t.routes {
		n := len(rt.segs)
		if len(raw) < n {
			continue
		}
		vals := map[string]string{}
		ok := true
		for k, s := range rt.segs {
			if s.param != "" {
				if dec[k] == "" || dec[k] == "." || dec[k] == ".." {
					ok = false
					break
				}
				vals[s.param] = raw[k]
			} else if dec[k] != s.lit {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		m := &Match{
			Route:  rt,
			Params: vals,
			Prefix: "/" + strings.Join(raw[:n], "/"),
			Base:   rt.buildBase(vals),
		}
		if len(raw) > n {
			m.Rest = "/" + strings.Join(raw[n:], "/")
		}
		return m, true
	}
	return nil, false
}

// publicPath maps an escaped backend path (may carry ?query or #frag) back to the
// public URL space. ok is false when the path is outside this route's backend base.
func (m *Match) publicPath(p string) (string, bool) {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return p, false
	}
	if m.Base == "" {
		return m.Prefix + p, true
	}
	if !strings.HasPrefix(p, m.Base) {
		return p, false
	}
	tail := p[len(m.Base):]
	if tail == "" || tail[0] == '/' || tail[0] == '?' || tail[0] == '#' {
		return m.Prefix + tail, true
	}
	return p, false
}

// rewriteLocation maps a backend redirect target into the public URL space.
func (m *Match) rewriteLocation(loc string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	if u.Host != "" {
		if !strings.EqualFold(u.Host, m.Route.host) {
			return loc // some other site
		}
		u.Scheme, u.Host, u.User = "", "", nil
		if u.Path == "" {
			u.Path = "/"
		}
	} else if u.Scheme != "" {
		return loc
	}
	if !strings.HasPrefix(u.Path, "/") {
		return loc // relative reference; the browser resolves it against the public URL
	}
	if p, ok := m.publicPath(u.String()); ok {
		return p
	}
	return loc
}

// hasDotSegment reports whether an escaped path has a "." or ".." segment.
func hasDotSegment(rawPath string) bool {
	for _, s := range strings.Split(rawPath, "/") {
		if d, err := url.PathUnescape(s); err == nil && (d == "." || d == "..") {
			return true
		}
	}
	return false
}
