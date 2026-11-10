package www

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// publicUsage is the public executable's usage, in docopt notation as
// usageText is. TestUsageIsDocopt checks it too.
var publicUsage = strings.NewReplacer("@NAME@", publicName, "@BASE@", progName, "@ENV@", envPrefix).Replace(`Usage:
  @NAME@ --disk=SIZE (-a ADDR | --share[=HOST]) [--debug] [--cache=SIZE]
      [--base=PATH]
  @NAME@ -h | --help
  @NAME@ -V | --version

Options:
  -a ADDR, --addr=ADDR  serve on this loopback address, IP or IP:PORT, eg
                        behind a proxy
  --share[=HOST]        serve on the address of the default route, or on
                        HOST, an address or name beyond loopback with an
                        optional :PORT, eg 0.0.0.0; a bare --share takes
                        @ENV@SHARE when it is set
  --disk=SIZE           disk the server may fill with online repos, eg 6G;
                        it drops the repos read longest ago
  --cache=SIZE          memory the server may spend on cached git answers,
                        eg 64M; 0 caches nothing [default: 32M]
  --base=PATH           serve every URL below PATH, eg /@BASE@, for a proxy
                        that gives the server that part of a domain
  --debug               show above each page's header, small and faint,
                        the binary's build time, the seconds since, its
                        commit and the page's render time
  -V, --version         print when the binary was built and its commit
  -h, --help            print this help

It serves only repos fetched from a forge, never a tree here, in the
foreground, to anyone who reaches the address. It lists no cache, shows no
stop or bug button and opens nothing.
`)

// Public runs the public executable on the process's arguments and exits.
func Public() {
	c, err := parsePublicArgs(os.Args[1:])
	if err != nil {
		noteRun()
		code := failNow(errnoOf(err), err.Error())
		io.WriteString(os.Stderr, publicUsage)
		os.Exit(code)
	}
	if lowPortOK() {
		defaultPort = 80
	}
	if c.help {
		io.WriteString(os.Stdout, publicUsage+logHelp())
		os.Exit(0)
	}
	if c.version {
		fmt.Println(versionLine(built, buildCommit))
		os.Exit(0)
	}
	ep, err := endpointOf(c, os.Getuid())
	if err != nil {
		noteRun()
		os.Exit(failNow(errnoOf(err), err.Error()))
	}
	c.ep = ep
	setBase(c.base)
	setLogEndpoint(endpointName(ep))
	noteRun()
	os.Exit(servePublic(c))
}

// parsePublicArgs reads the public executable's options as parseArgs
// reads the reader's: the GNU way, values joined or apart.
func parsePublicArgs(args []string) (config, error) {
	c := config{public: true}
	value := func(i *int, opt, joined string, has bool) (string, error) {
		switch {
		case joined != "":
			return joined, nil
		case has || *i+1 == len(args):
			return "", &errno{syscall.EINVAL, "option " + opt + " needs a value"}
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if i+1 < len(args) {
				return c, &errno{syscall.EINVAL, publicName + " takes no directory, got " + args[i+1]}
			}
		case strings.HasPrefix(a, "--"):
			name, val, has := strings.Cut(a[2:], "=")
			switch name {
			case "addr", "disk", "cache":
				v, err := value(&i, "--"+name, val, has)
				if err != nil {
					return c, err
				}
				if name == "addr" {
					c.addr = v
					continue
				}
				if _, err := parseSize(v); err != nil {
					return c, err
				}
				if name == "disk" {
					c.disk = v
				} else {
					c.cache = v
				}
			case "base":
				v, err := value(&i, "--base", val, has)
				if err != nil {
					return c, err
				}
				if c.base, err = checkBase(v); err != nil {
					return c, err
				}
			case "share":
				if has && val == "" {
					return c, &errno{syscall.EINVAL, "option --share= needs an address"}
				}
				c.sharing, c.share = true, val
			case "debug", "help", "version":
				if has {
					return c, &errno{syscall.EINVAL, "option --" + name + " takes no value"}
				}
				*map[string]*bool{"debug": &c.debug, "help": &c.help, "version": &c.version}[name] = true
			default:
				return c, &errno{syscall.EINVAL, "unknown option --" + name}
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'a':
					v, err := value(&i, "-a", a[j+1:], false)
					if err != nil {
						return c, err
					}
					c.addr = v
					j = len(a)
				case 'h':
					c.help = true
				case 'V':
					c.version = true
				default:
					return c, &errno{syscall.EINVAL, "unknown option -" + string(a[j])}
				}
			}
		default:
			return c, &errno{syscall.EINVAL, publicName + " takes no directory, got " + a}
		}
	}
	if c.help || c.version {
		return c, nil
	}
	if c.disk == "" {
		return c, &errno{syscall.EINVAL, publicName + " needs --disk, since any visitor can make it clone"}
	}
	// The default address is the user's own, where a reader's server runs.
	if c.addr == "" && !c.sharing {
		return c, &errno{syscall.EINVAL, publicName + " needs -a or --share"}
	}
	if c.addr != "" && c.sharing {
		return c, &errno{syscall.EINVAL, "-a and --share name one address each; give one"}
	}
	return c, nil
}

// servePublic serves online repos on c.ep until a signal, for strangers:
// no tree of the machine, no registry, no lock and no other address. A
// service manager keeps it running, so it never detaches. Its server is
// never made local, so the executable holds no stop form, bug form or
// index, and it prints its URL itself, since the reporter can open a
// browser.
func servePublic(c config) int {
	logInfo("public server starts on %s: %s", c.ep, build())
	go warmLexers()
	debugOn, bugOff = c.debug, true
	size := int64(defaultCache)
	if c.cache != "" {
		size, _ = parseSize(c.cache)
	}
	gitCache.reset(size)
	limit, _ := parseSize(c.disk)
	root, err := cacheDir()
	if err != nil {
		return failNow(errnoOf(err), "cannot find the cache: "+err.Error())
	}
	ln, err := net.Listen("tcp", c.ep.String())
	if err != nil {
		return failNow(errnoOf(err), "cannot listen on "+c.ep.String()+": "+err.Error())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := newWeb(ctx, root, limit)
	if err != nil {
		ln.Close()
		return failNow(errnoOf(err), "cannot serve online repos from "+root+": "+err.Error())
	}
	w.public, w.srv.public = true, true
	publicNet.Store(true)
	w.slots = make(chan struct{}, cloneSlots)
	w.srv.live = newLive()
	httpd := &http.Server{Handler: w.srv.handler(), ReadHeaderTimeout: 10 * time.Second,
		ErrorLog: log.New(logWriter{}, "", 0)}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	why := make(chan string, 1)
	go func() {
		why <- signalName(<-sigs)
		// Live pages hold their streams open; closing them lets
		// Shutdown finish.
		w.srv.live.close()
		cancel()
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		httpd.Shutdown(stop)
	}()
	logInfo("serves online repos at %s from %s, within %s", origin(c.ep), w.root, sizeText(limit))
	fmt.Println(origin(c.ep) + "/")
	for _, line := range firewallNote(c.ep) {
		fmt.Fprintln(os.Stderr, line)
	}
	if err := httpd.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		logErr("E%d: %s", syscall.EIO, err)
		fmt.Fprintf(os.Stderr, "E%d: %s\n", syscall.EIO, err)
		return int(syscall.EIO)
	}
	w.wg.Wait()
	logInfo("public server exits: %s", <-why)
	return 0
}
