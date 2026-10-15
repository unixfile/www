package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// wOK asks access(2) whether the caller may write.
	wOK = 2
	// joinWait bounds the wait for the server to take a joining tree.
	joinWait = 5 * time.Second
)

// checkDir resolves path and refuses what must not be served. A tree
// exposes everything below it, and home holds ~/.ssh, ~/.gnupg and every
// token file.
func checkDir(path, home string) (string, error) {
	dir, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("bad directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("cannot open %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", &errno{syscall.ENOTDIR, "not a directory: " + dir}
	}
	if hi, err := os.Stat(home); home != "" && err == nil && os.SameFile(info, hi) {
		return "", &errno{syscall.EPERM, "refusing to serve the home directory " + dir + ": it holds ~/.ssh, ~/.gnupg and every token file"}
	}
	return dir, nil
}

// run serves c.dir. It joins the server already on the endpoint, or becomes
// that server.
func run(c config, rep *reporter) int {
	home, _ := os.UserHomeDir()
	dir, err := checkDir(c.dir, home)
	if err != nil {
		return rep.fail(errnoOf(err), err.Error())
	}
	reg, err := openRegistry(c.ep)
	if err != nil {
		return rep.fail(errnoOf(err), "cannot open the registry: "+err.Error())
	}
	if err := reg.lock(); err != nil {
		reg.close()
		return rep.fail(errnoOf(err), "cannot lock the registry "+reg.dir+": "+err.Error())
	}
	lockPath := filepath.Join(dir, lockName(c.ep))
	if l, ok := findLive(lockPath); ok {
		reg.close()
		report(rep, l.url)
		return 0
	}
	if err := syscall.Access(dir, wOK); err != nil {
		reg.close()
		return rep.fail(errnoOf(err), "cannot write a lockfile in "+dir+": "+err.Error())
	}
	ln, err := net.Listen("tcp", c.ep.String())
	switch {
	case err == nil:
		return serve(c, rep, ln, reg, dir, home)
	case errors.Is(err, syscall.EADDRINUSE):
		return join(c.ep, rep, reg, dir, lockPath)
	default:
		reg.close()
		return rep.fail(errnoOf(err), "cannot listen on "+c.ep.String()+": "+err.Error())
	}
}

// join hands dir to the server already on ep and waits until it writes the
// lock. The registry stays locked until the link exists, so the server
// cannot be on its way out.
func join(ep netip.AddrPort, rep *reporter, reg *registry, dir, lockPath string) int {
	link, _, err := reg.add(lockPath)
	reg.close()
	if err != nil {
		return rep.fail(errnoOf(err), "cannot join the server on "+ep.String()+": "+err.Error())
	}
	for end := time.Now().Add(joinWait); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(lockPath); err == nil {
			if l := parseLock(b); l.live(bootID()) {
				report(rep, l.url)
				return 0
			}
		}
		// The server removes a link it cannot take.
		if _, err := os.Lstat(link); err != nil {
			return rep.fail(syscall.EEXIST, "the server on "+ep.String()+" refused "+dir+"; another of its trees may have the same URL")
		}
	}
	os.Remove(link)
	return rep.fail(syscall.ETIMEDOUT, ep.String()+" is taken, but no mdserve there took "+dir)
}

// origin is the URL of the server on ep. Port 80 is HTTP's own and goes
// unwritten.
func origin(ep netip.AddrPort) string {
	return originHost(ep.Addr().String(), ep.Port())
}

func originHost(host string, port uint16) string {
	if port == 80 {
		return "http://" + host
	}
	return "http://" + host + ":" + strconv.Itoa(int(port))
}

// report ends a start with the tree's URL alone, the same whether this run
// started the tree or found it served.
func report(rep *reporter, url string) {
	rep.send("url", url)
	rep.finish()
}

// serve runs the server on ln with dir as its first tree. The caller holds
// the registry lock, which serve releases once dir is adopted and a crashed
// server's leftovers are gone.
func serve(c config, rep *reporter, ln net.Listener, reg *registry, dir, home string) int {
	h := &hub{ep: c.ep, home: home, reg: reg, ln: ln, srv: newServer(), w: newWatcher(), done: make(chan struct{})}
	defer h.w.close()
	defer reg.close()
	filter, err := writeFilter()
	if err != nil {
		ln.Close()
		return rep.fail(errnoOf(err), "cannot write pandoc filter: "+err.Error())
	}
	h.filter = filter
	defer os.Remove(filter)
	h.dropLeftovers()
	h.w.add(reg.dir)
	t, err := h.adopt(filepath.Join(dir, lockName(c.ep)), "")
	reg.unlock()
	if err != nil {
		ln.Close()
		return rep.fail(errnoOf(err), "cannot serve "+dir+": "+err.Error())
	}
	h.srv.stop = h.stop
	h.srv.about = newAbout(c.ep)
	h.httpd = &http.Server{Handler: h.srv.handler(), ReadHeaderTimeout: 10 * time.Second}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-sigs:
			h.shutdown()
		case <-h.done:
		}
	}()
	go h.loop()

	url := origin(c.ep) + t.prefixURL()
	report(rep, url)
	if err := h.httpd.Serve(ln); !h.halted.Load() {
		h.shutdown()
		<-h.done
		fmt.Fprintf(os.Stderr, "E%d: %s\n", syscall.EIO, err)
		return int(syscall.EIO)
	}
	<-h.done
	return 0
}

// hub keeps the served trees in line with their locks and the registry. It
// adopts a tree that joins, withdraws a tree whose lock is gone, and stops
// the server with the last tree.
type hub struct {
	ep     netip.AddrPort
	home   string
	filter string
	reg    *registry
	w      *watcher
	srv    *server
	ln     net.Listener
	httpd  *http.Server
	// mu orders changes to the trees within this process, as the registry
	// flock does across processes.
	mu     sync.Mutex
	halted atomic.Bool
	done   chan struct{} // closed once open connections are drained
}

func (h *hub) lock() {
	h.mu.Lock()
	h.reg.lock()
}

func (h *hub) unlock() {
	h.reg.unlock()
	h.mu.Unlock()
}

// dropLeftovers clears what a crashed server left in the registry. Every
// link there names a stale lock, since no other server can hold this
// endpoint while this one starts.
func (h *hub) dropLeftovers() {
	links, _ := h.reg.links()
	for link, target := range links {
		if filepath.Base(target) == lockName(h.ep) {
			os.Remove(target)
		}
		os.Remove(link)
	}
}

// adopt serves the tree whose lock is lockPath and writes the lock. link is
// the registry entry a joining mdserve made, or "" to make one.
func (h *hub) adopt(lockPath, link string) (*tree, error) {
	if !filepath.IsAbs(lockPath) || filepath.Base(lockPath) != lockName(h.ep) {
		return nil, &errno{syscall.EINVAL, "not a lock for " + h.ep.String() + ": " + lockPath}
	}
	dir, err := checkDir(filepath.Dir(lockPath), h.home)
	if err != nil {
		return nil, err
	}
	prefix, label := mount(dir, h.home)
	if t := h.srv.byPrefix(prefix); t != nil {
		return nil, &errno{syscall.EEXIST, "the URL " + prefix + " already serves " + t.dir}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	t := newTree(root, dir, prefix, label, h.filter)
	if t.lock, err = createLock(lockPath, dir, origin(h.ep)+t.prefixURL()); err != nil {
		root.Close()
		return nil, fmt.Errorf("cannot write lockfile: %w", err)
	}
	if link == "" {
		t.link, t.linkInfo, err = h.reg.add(lockPath)
	} else {
		t.link = link
		t.linkInfo, err = os.Lstat(link)
	}
	if err != nil {
		t.lock.remove()
		root.Close()
		return nil, fmt.Errorf("cannot register: %w", err)
	}
	t.wd = h.w.add(dir)
	h.srv.add(t)
	return t, nil
}

// withdraw stops serving t and removes its lock and link. A link that is
// not the one the server knows is a new join for the same directory, and
// stays. The caller holds the lock.
func (h *hub) withdraw(t *tree) {
	if !h.srv.remove(t) {
		return
	}
	t.lock.remove()
	if info, err := os.Lstat(t.link); err == nil && os.SameFile(info, t.linkInfo) {
		os.Remove(t.link)
	}
	h.w.remove(t.wd)
	t.root.Close()
}

// sync withdraws every tree whose lock is gone and adopts every new link.
// When no tree is left it stops the server. It reports whether the server
// still runs.
func (h *hub) sync() bool {
	h.lock()
	defer h.unlock()
	if h.halted.Load() {
		return false
	}
	for _, t := range h.srv.list() {
		if t.lock.gone() {
			h.withdraw(t)
		}
	}
	served := map[string]bool{}
	for _, t := range h.srv.list() {
		served[t.link] = true
	}
	links, _ := h.reg.links()
	for link, target := range links {
		if served[link] {
			continue
		}
		if _, err := h.adopt(target, link); err != nil {
			os.Remove(link)
		}
	}
	if len(h.srv.list()) == 0 {
		h.halt()
		return false
	}
	return true
}

// loop syncs whenever the watcher sees a change, and every second when
// inotify is unavailable.
func (h *hub) loop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-h.w.wake:
		case <-tick.C:
			if !h.w.poll.Load() {
				continue
			}
		}
		if !h.sync() {
			return
		}
	}
}

// stop withdraws trees at the press of a button. The loop then stops the
// server if no tree is left.
func (h *hub) stop(trees []*tree) {
	h.lock()
	for _, t := range trees {
		h.withdraw(t)
	}
	h.unlock()
	h.w.signal()
}

// shutdown withdraws every tree and stops the server, as on a signal.
func (h *hub) shutdown() {
	h.lock()
	for _, t := range h.srv.list() {
		h.withdraw(t)
	}
	h.halt()
	h.unlock()
	h.w.signal()
}

// halt closes the listener while the registry is locked, so a joining
// mdserve finds the address free and starts a new server instead of
// linking into this one. Open connections drain in the background. The
// caller holds the lock.
func (h *hub) halt() {
	if h.halted.Swap(true) {
		return
	}
	h.ln.Close()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.httpd.Shutdown(ctx)
		close(h.done)
	}()
}
