package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// releaseURL is the GitHub repository that install.sh and the release
// workflow use. The asset names below must match release.yml.
const releaseURL = "https://github.com/jordantdavis/quickproxy"

// maxDownload limits each download, so a bad server cannot fill memory.
const maxDownload = 100 << 20

// updater replaces the running binary with a release from GitHub. Its fields
// are separate so that tests can use a fake server and a temporary file.
type updater struct {
	client  *http.Client
	baseURL string
	goos    string
	goarch  string
	exe     string
	current string
	out     io.Writer
}

// runUpdate runs "quickproxy update". It returns the process exit code:
// 0 on success, 1 when -check finds a newer release, 2 on an error.
func runUpdate(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("update", flag.ContinueOnError)
	fset.SetOutput(stderr)
	tag := fset.String("version", "", "install this release tag, for example v1.0.0 (default: latest)")
	check := fset.Bool("check", false, "print the current and latest versions and exit; exit code 1 means an update exists")
	force := fset.Bool("force", false, "install even if the version is the same or this is a development build")
	fset.Usage = func() {
		fmt.Fprintln(stderr, "usage: quickproxy update [-check] [-version vX.Y.Z] [-force]")
		fset.PrintDefaults()
	}
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fset.NArg() > 0 {
		fset.Usage()
		return 2
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "quickproxy update:", err)
		return 2
	}
	u := &updater{
		client:  &http.Client{Timeout: 2 * time.Minute},
		baseURL: releaseURL,
		goos:    runtime.GOOS,
		goarch:  runtime.GOARCH,
		exe:     exe,
		current: version,
		out:     stdout,
	}
	code, err := u.run(context.Background(), *tag, *check, *force)
	if err != nil {
		fmt.Fprintln(stderr, "quickproxy update:", err)
	}
	return code
}

func (u *updater) run(ctx context.Context, tag string, check, force bool) (int, error) {
	if !supportedPlatform(u.goos, u.goarch) {
		return 2, fmt.Errorf("no release builds for %s/%s", u.goos, u.goarch)
	}
	explicit := tag != ""
	if !explicit {
		var err error
		if tag, err = u.latestTag(ctx); err != nil {
			return 2, err
		}
	}

	if check {
		fmt.Fprintf(u.out, "current: %s\nlatest:  %s\n", u.current, tag)
		if newer, err := isNewer(tag, u.current); err != nil || newer {
			return 1, nil
		}
		return 0, nil
	}

	if !force && !explicit {
		if u.current == "dev" {
			return 2, errors.New("this is a development build; use -force or -version to replace it")
		}
		newer, err := isNewer(tag, u.current)
		if err != nil {
			return 2, err
		}
		if !newer {
			fmt.Fprintf(u.out, "quickproxy %s is up to date\n", u.current)
			return 0, nil
		}
	}

	name := fmt.Sprintf("quickproxy_%s_%s_%s", tag, u.goos, u.goarch)
	fmt.Fprintf(u.out, "Downloading quickproxy %s for %s/%s\n", tag, u.goos, u.goarch)
	archive, err := u.download(ctx, tag, name+".tar.gz")
	if err != nil {
		return 2, err
	}
	bin, err := extractBinary(archive, name)
	if err != nil {
		return 2, err
	}
	path, err := replaceExecutable(u.exe, bin, tag)
	if err != nil {
		return 2, err
	}
	fmt.Fprintf(u.out, "Updated quickproxy %s -> %s at %s. Restart quickproxy to use the new version.\n", u.current, tag, path)
	return 0, nil
}

func supportedPlatform(goos, goarch string) bool {
	return (goos == "linux" || goos == "darwin") && (goarch == "amd64" || goarch == "arm64")
}

// latestTag reads the tag from the redirect of the releases/latest page, as
// install.sh does. This avoids the GitHub API rate limit.
func (u *updater) latestTag(ctx context.Context) (string, error) {
	client := *u.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.baseURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not find the latest release: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || loc == "" {
		return "", fmt.Errorf("could not find the latest release: %s", resp.Status)
	}
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if !strings.HasPrefix(tag, "v") {
		return "", errors.New("no release found")
	}
	return tag, nil
}

// download gets the archive and checksums.txt for tag, and returns the
// archive if its SHA-256 sum matches the line for it in checksums.txt.
func (u *updater) download(ctx context.Context, tag, file string) ([]byte, error) {
	base := u.baseURL + "/releases/download/" + tag + "/"
	archive, err := u.get(ctx, base+file)
	if err != nil {
		return nil, err
	}
	sums, err := u.get(ctx, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	var want string
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == file {
			want = f[0]
			break
		}
	}
	if want == "" {
		return nil, fmt.Errorf("no checksum for %s in checksums.txt", file)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != strings.ToLower(want) {
		return nil, fmt.Errorf("checksum mismatch for %s", file)
	}
	return archive, nil
}

func (u *updater) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download failed: %s: %w", url, err)
	}
	if len(body) > maxDownload {
		return nil, fmt.Errorf("download failed: %s: larger than %d bytes", url, maxDownload)
	}
	return body, nil
}

// extractBinary returns the file <name>/quickproxy from the gzip tar archive.
// It reads no other entry, so a path such as ../x in the archive has no effect.
func extractBinary(archive []byte, name string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	tr := tar.NewReader(zr)
	want := name + "/quickproxy"
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("archive has no %s", want)
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if h.Name != want || h.Typeflag != tar.TypeReg {
			continue
		}
		return io.ReadAll(io.LimitReader(tr, maxDownload))
	}
}

// replaceExecutable writes bin next to exe, runs it with -version to make
// sure that it works and reports tag, and then renames it over exe. A
// running process keeps the old file open, so it is not affected. It
// returns the path that it replaced.
func replaceExecutable(exe string, bin []byte, tag string) (string, error) {
	path, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".quickproxy-update-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("cannot write to %s; run the command with permission to write there, or reinstall with install.sh and INSTALL_DIR", dir)
		}
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	out, err := exec.Command(tmp.Name(), "-version").Output()
	if err != nil {
		return "", fmt.Errorf("new binary does not run: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), tag) {
		return "", fmt.Errorf("new binary reports version %q, want %s", strings.TrimSpace(string(out)), tag)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

// isNewer reports whether release tag a is newer than b.
func isNewer(a, b string) (bool, error) {
	c, err := compareVersions(a, b)
	return c > 0, err
}

// compareVersions compares two tags of the form vMAJOR.MINOR.PATCH with an
// optional -suffix. A tag without a suffix is newer than the same tag with
// one. Two suffixes compare as strings.
func compareVersions(a, b string) (int, error) {
	pa, sa, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	pb, sb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case sa == sb:
		return 0, nil
	case sa == "":
		return 1, nil
	case sb == "":
		return -1, nil
	}
	return strings.Compare(sa, sb), nil
}

func parseVersion(v string) ([3]int, string, error) {
	var n [3]int
	s, ok := strings.CutPrefix(v, "v")
	if !ok {
		return n, "", fmt.Errorf("bad version %q", v)
	}
	s, suffix, _ := strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return n, "", fmt.Errorf("bad version %q", v)
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return n, "", fmt.Errorf("bad version %q", v)
		}
		n[i] = x
	}
	return n, suffix, nil
}
