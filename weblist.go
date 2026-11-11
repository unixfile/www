package www

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// listing is the repos a host or account holds, and what told.
type listing struct {
	at     time.Time
	source string   // what answered, eg "the GitHub API"
	asked  string   // the URL asked
	failed []string // the sources that failed before it, and why
	repos  []listed
}

type listed struct{ key, desc string }

func (l *listing) fresh() time.Duration {
	if len(l.repos) == 0 {
		return missFresh
	}
	return listFresh
}

// list lists the path key, by the first source that answers: the forge's
// API, a web frontend's index, then the cache.
func (w *web) list(j *webJob, key string, probed bool) {
	segs := strings.Split(key, "/")
	l := &listing{at: time.Now()}
	for _, src := range listSources(segs) {
		w.step(j, "GET "+src.url+", "+src.name)
		repos, err := w.ask(src)
		if err == nil && len(repos) > 0 {
			l.source, l.asked, l.repos = src.name, src.url, repos
			break
		}
		if err == nil {
			err = errors.New("no repos")
		}
		l.failed = append(l.failed, src.name+": "+err.Error())
		w.step(j, src.name+": "+err.Error())
	}
	// A stranger must not learn what others read, so a public server
	// never lists the cache.
	if l.source == "" && !w.public {
		l.source = "the cache"
		w.step(j, "no source lists "+key+", so the cache does")
		for _, k := range w.onDiskKeys() {
			if strings.HasPrefix(k, key+"/") {
				l.repos = append(l.repos, listed{key: k})
			}
		}
		slices.SortFunc(l.repos, func(a, b listed) int { return strings.Compare(a.key, b.key) })
	}
	if len(l.repos) == 0 && probed && len(l.failed) == 0 {
		l.failed = append(l.failed, "no source lists "+key)
	}
	logInfo("listed %d repos at %s by %s", len(l.repos), key, l.source)
	w.mu.Lock()
	w.lists[key] = l
	w.mu.Unlock()
	w.end(j, nil)
}

// listSource is one place that may list a path's repos.
type listSource struct {
	name, url string
	parse     func(body []byte, next *string) ([]listed, error)
}

var (
	cgitRepo   = regexp.MustCompile(`<td class='(?:top|sub)level-repo'><a (?:title='[^']*' )?href='([^']+)'`)
	stagitRepo = regexp.MustCompile(`<td><a href="([^"/]+)/log\.html">`)
)

// listSources are the sources for the path segs, in the order to ask them.
func listSources(segs []string) []listSource {
	host, rest := segs[0], strings.Join(segs[1:], "/")
	var out []listSource
	jsonList := func(field string) func([]byte, *string) ([]listed, error) {
		return func(b []byte, _ *string) ([]listed, error) {
			var v []map[string]any
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, errors.New("not a list of repos")
			}
			var out []listed
			for _, m := range v {
				name, _ := m[field].(string)
				desc, _ := m["description"].(string)
				if name != "" {
					out = append(out, listed{key: host + "/" + name, desc: desc})
				}
			}
			return out, nil
		}
	}
	switch {
	case host == "github.com" && len(segs) == 2:
		out = append(out, listSource{"the GitHub API", "https://api.github.com/users/" + rest + "/repos?per_page=100", jsonList("full_name")})
	case host == "gitlab.com" && len(segs) >= 2:
		if len(segs) == 2 {
			out = append(out, listSource{"the GitLab API", "https://gitlab.com/api/v4/users/" + rest + "/projects?per_page=100", jsonList("path_with_namespace")})
		}
		out = append(out, listSource{"the GitLab API", "https://gitlab.com/api/v4/groups/" + url.PathEscape(rest) + "/projects?per_page=100&include_subgroups=true", jsonList("path_with_namespace")})
	case host == "bitbucket.org" && len(segs) == 2:
		out = append(out, listSource{"the Bitbucket API", "https://api.bitbucket.org/2.0/repositories/" + rest + "?pagelen=100",
			func(b []byte, next *string) ([]listed, error) {
				var v struct {
					Values []struct{ Full_name, Description string }
					Next   string
				}
				if err := json.Unmarshal(b, &v); err != nil {
					return nil, errors.New("not a list of repos")
				}
				*next = v.Next
				var out []listed
				for _, r := range v.Values {
					out = append(out, listed{key: host + "/" + r.Full_name, desc: r.Description})
				}
				return out, nil
			}})
	case len(segs) == 2 && !slices.Contains([]string{"github.com", "gitlab.com", "bitbucket.org"}, host):
		out = append(out, listSource{"the Forgejo API", webScheme + "://" + host + "/api/v1/users/" + rest + "/repos?limit=50", jsonList("full_name")})
	}
	if _, forge := depths[host]; forge || host == "gitlab.com" {
		return out
	}
	base := webScheme + "://" + strings.Join(segs, "/") + "/"
	out = append(out, listSource{"gitweb's project index", base + "?a=project_index", func(b []byte, _ *string) ([]listed, error) {
		var out []listed
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 0 || strings.ContainsAny(f[0], "<>\"") {
				return nil, errors.New("not a project index")
			}
			name, err := url.QueryUnescape(f[0])
			if err != nil {
				return nil, errors.New("not a project index")
			}
			out = append(out, listed{key: strings.Join(segs, "/") + "/" + strings.TrimSuffix(name, ".git")})
		}
		return out, nil
	}})
	out = append(out, listSource{"the frontend's front page", base, func(b []byte, _ *string) ([]listed, error) {
		var out []listed
		for _, m := range cgitRepo.FindAllSubmatch(b, -1) {
			p := strings.Trim(string(m[1]), "/")
			out = append(out, listed{key: host + "/" + strings.TrimSuffix(p, ".git")})
		}
		for _, m := range stagitRepo.FindAllSubmatch(b, -1) {
			out = append(out, listed{key: strings.Join(segs, "/") + "/" + string(m[1])})
		}
		return out, nil
	}})
	return out
}

var webClient = &http.Client{Timeout: 20 * time.Second}

// ask reads src, following the forge's pages up to 10.
func (w *web) ask(src listSource) ([]listed, error) {
	var out []listed
	u := src.url
	for page := 1; page <= 10 && u != ""; page++ {
		req, err := http.NewRequestWithContext(w.ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", progName)
		client := webClient
		if w.public {
			client = publicClient
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, errors.New(resp.Status)
		}
		next := ""
		got, err := src.parse(b, &next)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		switch {
		case next != "":
			u = next
		case strings.Contains(src.url, "api") && len(got) >= 50 && !strings.Contains(src.url, "bitbucket"):
			u = src.url + "&page=" + strconv.Itoa(page+1)
		default:
			u = ""
		}
	}
	return out, nil
}
