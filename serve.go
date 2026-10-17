package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
		logInfo("already served: %s", dir)
		if c.secret {
			rep.send("note", "already served; --secret applies to a new share. Delete "+lockPath+" and share again to change it.")
		}
		report(rep, c.ep, l.url)
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
	case errors.Is(err, syscall.EADDRINUSE) && !reg.serves(c.ep):
		// The registry is locked, so a server of ours on ep is either up
		// with a tree or gone. Something else holds the address.
		reg.close()
		return rep.fail(syscall.EADDRINUSE, taken(c.ep))
	case errors.Is(err, syscall.EADDRINUSE):
		return join(c.ep, rep, reg, dir, lockPath, c.token)
	default:
		reg.close()
		return rep.fail(errnoOf(err), "cannot listen on "+c.ep.String()+": "+err.Error())
	}
}

// join hands dir to the server already on ep and waits until it writes the
// lock. The registry stays locked until the link exists, so the server
// cannot be on its way out.
func join(ep netip.AddrPort, rep *reporter, reg *registry, dir, lockPath, token string) int {
	link, _, err := reg.add(lockPath, token)
	reg.close()
	if err != nil {
		return rep.fail(errnoOf(err), "cannot join the server on "+ep.String()+": "+err.Error())
	}
	for end := time.Now().Add(joinWait); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(lockPath); err == nil {
			if l := parseLock(b); l.live(bootID()) {
				logInfo("joined the server on %s: %s", ep, dir)
				report(rep, ep, l.url)
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
// unwritten. A server on 0.0.0.0 answers on every address, and the
// machine's name is the one to hand out.
func origin(ep netip.AddrPort) string {
	host := ep.Addr().String()
	if ep.Addr().IsUnspecified() {
		if name, err := os.Hostname(); err == nil && name != "" {
			host = name
		}
	}
	return originHost(host, ep.Port())
}

// shareOrigin is origin with the address's name when one is known: the
// reverse lookup's answer, if it resolves back to the address. A visitor
// on a network with that DNS reads mate.peer.home, not 10.173.42.3.
func shareOrigin(ep netip.AddrPort) string {
	if !shared(ep) || ep.Addr().IsUnspecified() {
		return origin(ep)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	names, _ := net.DefaultResolver.LookupAddr(ctx, ep.Addr().String())
	for _, name := range names {
		name = strings.TrimSuffix(name, ".")
		ips, _ := net.DefaultResolver.LookupNetIP(ctx, "ip4", name)
		for _, ip := range ips {
			if ip.Unmap() == ep.Addr() {
				return originHost(name, ep.Port())
			}
		}
	}
	return origin(ep)
}

func originHost(host string, port uint16) string {
	if port == 80 {
		return "http://" + host
	}
	return "http://" + host + ":" + strconv.Itoa(int(port))
}

// report ends a start with the tree's URL alone, the same whether this run
// started the tree or found it served. A share adds a note on stderr: a
// firewall drops other machines before mdserve sees them, and a test from
// this machine passes through loopback, so only a capture tells.
func report(rep *reporter, ep netip.AddrPort, url string) {
	rep.send("url", url)
	if shared(ep) {
		port := strconv.Itoa(int(ep.Port()))
		dest := ep.String()
		if ep.Addr().IsUnspecified() {
			dest = "port " + port
		}
		rep.send("note", "note: other machines connect to "+dest+"; a firewall here may drop them unseen.")
		rep.send("note", "      Check with: sudo tcpdump -ni "+ifaceOf(ep.Addr())+" tcp port "+port)
	}
	rep.finish()
}

// ifaceOf names the interface that holds a, or "any" for 0.0.0.0 or an
// address no interface holds.
func ifaceOf(a netip.Addr) string {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		addrs, _ := i.Addrs()
		for _, ad := range addrs {
			if n, ok := ad.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap() == a {
					return i.Name
				}
			}
		}
	}
	return "any"
}

// serve runs the server on ln with dir as its first tree. The caller holds
// the registry lock, which serve releases once dir is adopted and a crashed
// server's leftovers are gone.
func serve(c config, rep *reporter, ln net.Listener, reg *registry, dir, home string) int {
	logInfo("server starts on %s: %s", c.ep, build())
	go warmLexers()
	showBuilt = c.stamp
	gitCache = newCache(defaultCache)
	if c.cache != "" {
		n, _ := parseSize(c.cache)
		gitCache = newCache(n)
	}
	h := &hub{ep: c.ep, home: home, reg: reg, ln: ln, srv: newServer(), w: newWatcher(lockMask),
		done: make(chan struct{}), fresh: map[net.Conn]bool{}}
	defer h.w.close()
	defer reg.close()
	h.dropLeftovers()
	h.w.add(reg.dir)
	h.srv.shared = shared(c.ep)
	h.origin = shareOrigin(c.ep)
	t, err := h.adopt(filepath.Join(dir, lockName(c.ep)), "", c.token)
	reg.unlock()
	if err != nil {
		ln.Close()
		return rep.fail(errnoOf(err), "cannot serve "+dir+": "+err.Error())
	}
	h.srv.stop = h.stop
	h.srv.about = newAbout(c.ep)
	h.srv.live = newLive()
	defer h.srv.live.close()
	h.httpd = &http.Server{Handler: h.srv.handler(), ReadHeaderTimeout: 10 * time.Second, ConnState: h.track,
		ErrorLog: log.New(logWriter{}, "", 0)}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		select {
		case sig := <-sigs:
			h.shutdown(signalName(sig))
		case <-h.done:
		}
	}()
	go h.loop()

	url := h.origin + t.prefixURL()
	report(rep, c.ep, url)
	if err := h.httpd.Serve(ln); !h.halted.Load() {
		h.shutdown("cannot serve: " + err.Error())
		<-h.done
		logErr("E%d: %s", syscall.EIO, err)
		// Detached, stderr is the log itself.
		if rep.pipe == nil {
			fmt.Fprintf(os.Stderr, "E%d: %s\n", syscall.EIO, err)
		}
		return int(syscall.EIO)
	}
	<-h.done
	logInfo("server exits")
	return 0
}

// signalName names the signals serve stops on, as kill(1) does.
func signalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return sig.String()
}

// hub keeps the served trees in line with their locks and the registry. It
// adopts a tree that joins, withdraws a tree whose lock is gone, and stops
// the server with the last tree.
type hub struct {
	ep     netip.AddrPort
	origin string // the URL of this server, a name where it has one
	home   string
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
	// fresh holds the connections that have sent no request yet.
	fresh   map[net.Conn]bool
	freshMu sync.Mutex
}

// track keeps fresh up to date.
func (h *hub) track(c net.Conn, state http.ConnState) {
	h.freshMu.Lock()
	defer h.freshMu.Unlock()
	if state == http.StateNew {
		h.fresh[c] = true
	} else {
		delete(h.fresh, c)
	}
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
		if lockPath, _ := splitTarget(target); filepath.Base(lockPath) == lockName(h.ep) {
			if os.Remove(lockPath) == nil {
				logInfo("removed %s, left by a crashed server", lockPath)
			}
		}
		os.Remove(link)
	}
}

// adopt serves the tree whose lock is lockPath and writes the lock. link is
// the registry entry a joining mdserve made, or "" to make one. token puts
// a shared tree under a secret path.
func (h *hub) adopt(lockPath, link, token string) (*tree, error) {
	if !filepath.IsAbs(lockPath) || filepath.Base(lockPath) != lockName(h.ep) {
		return nil, &errno{syscall.EINVAL, "not a lock for " + h.ep.String() + ": " + lockPath}
	}
	dir, err := checkDir(filepath.Dir(lockPath), h.home)
	if err != nil {
		return nil, err
	}
	prefix, label := mount(dir, h.home)
	if h.srv.shared {
		prefix, label = shareMount(dir, token)
	}
	if t := h.srv.byPrefix(prefix); t != nil {
		return nil, &errno{syscall.EEXIST, "the URL " + prefix + " already serves " + t.dir}
	}
	if h.srv.shared {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, err
		}
		if bad, mode, ok := othersRead(real, real); !ok {
			return nil, &errno{syscall.EACCES, fmt.Sprintf("other users cannot read %s (mode %03o), so a share of it would serve nothing", bad, mode.Perm())}
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	t := newTree(root, dir, prefix, label)
	t.shared, t.secret = h.srv.shared, token != ""
	if t.lock, err = createLock(lockPath, dir, h.origin+t.prefixURL()); err != nil {
		root.Close()
		return nil, fmt.Errorf("cannot write lockfile: %w", err)
	}
	if link == "" {
		t.link, t.linkInfo, err = h.reg.add(lockPath, token)
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
	logInfo("serves %s at %s", dir, h.where(t))
	return t, nil
}

// where is t's URL for the log; a secret tree's would hold its token.
func (h *hub) where(t *tree) string {
	if t.secret {
		return h.origin + "/<token>/"
	}
	return h.origin + t.prefixURL()
}

// withdraw stops serving t for the cause why and removes its lock and link.
// A link that is not the one the server knows is a new join for the same
// directory, and stays. The caller holds the lock.
func (h *hub) withdraw(t *tree, why string) {
	if !h.srv.remove(t) {
		return
	}
	logInfo("withdraws %s: %s", t.dir, why)
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
			h.withdraw(t, "its lock "+t.lock.path+" was deleted or replaced")
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
		lockPath, token := splitTarget(target)
		if _, err := h.adopt(lockPath, link, token); err != nil {
			logErr("E%d: refused %s: %v", errnoOf(err), lockPath, err)
			os.Remove(link)
		}
	}
	if len(h.srv.list()) == 0 {
		h.halt("its last tree is gone")
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
func (h *hub) stop(trees []*tree, why string) {
	h.lock()
	for _, t := range trees {
		h.withdraw(t, why)
	}
	h.unlock()
	h.w.signal()
}

// shutdown withdraws every tree and stops the server, as on a signal.
func (h *hub) shutdown(why string) {
	h.lock()
	for _, t := range h.srv.list() {
		h.withdraw(t, why)
	}
	h.halt(why)
	h.unlock()
	h.w.signal()
}

// halt closes the listener while the registry is locked, so a joining
// mdserve finds the address free and starts a new server instead of
// linking into this one. Open connections drain in the background. The
// caller holds the lock.
func (h *hub) halt(why string) {
	if h.halted.Swap(true) {
		return
	}
	logInfo("server stops: %s", why)
	h.ln.Close()
	// Live reload streams never end on their own, and Shutdown waits for them.
	h.srv.live.close()
	// Shutdown also waits five seconds for a connection that has sent no
	// request, in case one is on its way. Browsers open such connections
	// ahead of need, and a stopping server has no page for them.
	h.freshMu.Lock()
	for c := range h.fresh {
		c.Close()
	}
	h.freshMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.httpd.Shutdown(ctx)
		close(h.done)
	}()
}
