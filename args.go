package www

import (
	"net/netip"
	"strings"
	"syscall"
)

type config struct {
	dir     string
	addr    string
	sharing bool
	share   string // the address --share names; "" takes the default route's
	secret  bool
	token   string // the secret path's token, from WWW_SECRET or drawn at random
	ep      netip.AddrPort
	fg      bool
	noopen  bool
	persist bool   // write a persistent lock, so the tree is served after reboot
	debug   bool   // show the build stamp and the index's times
	noBug   bool   // hide the bug button and refuse reports
	cache   string // the memory for cached git answers, eg 32M; "" takes defaultCache
	disk    string // the disk for online repos, eg 4G; "" takes defaultDisk
	public  bool   // serve only online repos, to the open web; the public executable sets it
	help    bool
	version bool
	restart bool
	force   bool // --restart takes servers of other binaries too
	list    bool
	kill    bool
	commit  string // the commit --commit looks up
}

// usageText is written in docopt notation, except that --share takes its
// HOST optionally, as GNU optional arguments do, which docopt cannot say.
// TestUsageIsDocopt checks it with docopt-ng. A pattern goes on over
// indented lines.
var usageText = strings.NewReplacer("@NAME@", progName, "@ENV@", envPrefix).Replace(`Usage:
  @NAME@ [-p] [--foreground] [--no-open] [-a ADDR] [--debug] [--no-bug]
      [--cache=SIZE] [--disk=SIZE] [--] [DIR]
  @NAME@ [-p] [--foreground] [--no-open] --share[=HOST] [--secret] [--debug]
      [--no-bug] [--cache=SIZE] [--disk=SIZE] [--] [DIR]
  @NAME@ --restart [-f] [--no-open] [--debug] [--no-bug]
      [-a ADDR | --share[=HOST]] [--] [DIR]
  @NAME@ -l [-a ADDR | --share[=HOST]] [--] [DIR]
  @NAME@ --kill [-a ADDR | --share[=HOST]]
  @NAME@ --commit=REV [--] [DIR]
  @NAME@ -h | --help
  @NAME@ -V | --version

Options:
  -a ADDR, --addr=ADDR  serve on this loopback address, IP or IP:PORT; the
                        port defaults to 80 when @NAME@ may bind it, else
                        8080, and the IP to 127.31.N.1, N = uid - 1000
  --share[=HOST]        share the tree on the address of the default route,
                        or on HOST, an address or name beyond loopback with
                        an optional :PORT, eg 192.168.1.5, 0.0.0.0 or
                        mate.peer.home; anyone who reaches it reads every
                        file in the tree that other users could read
  --secret              serve a shared tree under the token in @ENV@SECRET,
                        or a random one, so only those holding the link
                        reach it
  --foreground          when no server runs on the address yet, serve in the
                        foreground until Ctrl-C
  --no-open             print the URL without opening a browser
  -p, --persist         write a .LOCK instead of a .lock: the tree is served
                        again after a reboot or a stopped server, by the
                        next server on its address or by --restart
  --debug               show what serves debugging: above each page's
                        header, small and faint, the binary's build time,
                        the seconds since, its commit and the page's render
                        time; on the index, each server's build and start
                        and each lock's time; with --restart, on the new
                        servers
  --no-bug              hide the bug button at the foot of each page, which
                        files a numbered bug in this program, not in the
                        tree, with the page and the server's state; a share
                        never shows it; with --restart, on the new servers
  --cache=SIZE          memory a new server may spend on cached git answers,
                        eg 64M; 0 caches nothing [default: 32M]
  --disk=SIZE           disk a new server may fill with online repos, eg 4G;
                        it drops the repos read longest ago [default: 2G]
  --restart             replace every server this binary started with one
                        running it now, serving the same trees, and start
                        one for its persistent trees that have none; with an
                        address from -a or --share, only the server there,
                        whichever binary runs it; with DIR, serve it too and
                        open it unless a page of it was open
  -f, --force           with --restart, take the servers and persistent
                        trees of other binaries too, eg the package's
  -l, --list            print each served tree's directory, URL and the
                        binary that serves it as keyval; with DIR, only the
                        trees at or below it
  --kill                stop every server and print the trees it served,
                        as with --list; with an address from -a or --share,
                        only the server there
  --commit=REV          print as keyval the commit REV names in DIR's repo:
                        its full hash, its stardate and its subject; REV is
                        a stardate, eg 261024.4, a hash or any name git
                        takes
  -V, --version         print when the binary was built and its commit
  -h, --help            print this help
`)

// switches maps each long boolean option to its field; shorts names the
// ones with a short form.
func (c *config) switches() map[string]*bool {
	return map[string]*bool{
		"foreground": &c.fg,
		"no-open":    &c.noopen,
		"persist":    &c.persist,
		"debug":      &c.debug,
		"no-bug":     &c.noBug,
		"help":       &c.help,
		"version":    &c.version,
		"restart":    &c.restart,
		"force":      &c.force,
		"list":       &c.list,
		"kill":       &c.kill,
	}
}

// -f and -n are reserved for --force and --dry-run in every tool of the
// user's, so --foreground and --no-open have no short form.
var shorts = map[byte]string{'p': "persist", 'h': "help", 'V': "version", 'l': "list", 'f': "force"}

// parseArgs reads options the GNU way. Short ones combine, as in -pl, and
// take a value joined or apart: -aIP or -a IP. Long ones take --addr IP or
// --addr=IP. Options may follow the directory, and "--" ends them.
func parseArgs(args []string) (config, error) {
	var c config
	sw := c.switches()
	var dirs []string
	value := func(i *int, opt, joined string) (string, error) {
		if joined != "" {
			return joined, nil
		}
		if *i+1 == len(args) {
			return "", &errno{syscall.EINVAL, "option " + opt + " needs a value"}
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			dirs = append(dirs, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			if name == "addr" {
				if hasVal && val == "" {
					return c, &errno{syscall.EINVAL, "option --addr needs a value"}
				}
				v, err := value(&i, "--addr", val)
				if err != nil {
					return c, err
				}
				c.addr = v
				continue
			}
			// --share, like --secret, takes its address only joined; alone
			// it takes the default route's.
			if name == "share" {
				if hasVal && val == "" {
					return c, &errno{syscall.EINVAL, "option --share= needs an address"}
				}
				c.sharing, c.share = true, val
				continue
			}
			// --secret takes no token: one on the command line shows in
			// ps to every local user and stays in shell history.
			if name == "secret" {
				if hasVal {
					return c, &errno{syscall.EINVAL, "--secret takes no token, since ps shows it; set " + secretEnv}
				}
				c.secret = true
				continue
			}
			if name == "commit" {
				v, err := value(&i, "--commit", val)
				if err != nil {
					return c, err
				}
				if v == "" {
					return c, &errno{syscall.EINVAL, "option --commit needs a value"}
				}
				c.commit = v
				continue
			}
			if name == "cache" || name == "disk" {
				v, err := value(&i, "--"+name, val)
				if err != nil {
					return c, err
				}
				if _, err := parseSize(v); err != nil {
					return c, err
				}
				if name == "cache" {
					c.cache = v
				} else {
					c.disk = v
				}
				continue
			}
			if name == "public" {
				return c, &errno{syscall.EINVAL, "--public is an executable of its own, " + publicName}
			}
			p, ok := sw[name]
			switch {
			case !ok:
				return c, &errno{syscall.EINVAL, "unknown option --" + name}
			case hasVal:
				return c, &errno{syscall.EINVAL, "option --" + name + " takes no value"}
			}
			*p = true
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				if a[j] == 'a' {
					v, err := value(&i, "-a", a[j+1:])
					if err != nil {
						return c, err
					}
					c.addr = v
					break
				}
				name, ok := shorts[a[j]]
				if !ok {
					return c, &errno{syscall.EINVAL, "unknown option -" + string(a[j])}
				}
				*sw[name] = true
			}
		default:
			dirs = append(dirs, a)
		}
	}
	if c.commit != "" && (c.restart || c.list || c.kill || c.fg || c.noopen || c.persist || c.debug || c.noBug ||
		c.sharing || c.secret || c.addr != "" || c.cache != "" || c.disk != "") {
		return c, &errno{syscall.EINVAL, "--commit takes a directory and no other option"}
	}
	if c.force && !c.restart {
		return c, &errno{syscall.EINVAL, "--force goes with --restart"}
	}
	if c.restart && c.fg {
		return c, &errno{syscall.EINVAL, "--restart takes no --foreground"}
	}
	if c.persist && (c.restart || c.list || c.kill) {
		return c, &errno{syscall.EINVAL, "--persist goes with a start, not with --restart, --list or --kill"}
	}
	if c.list && (c.restart || c.fg) {
		return c, &errno{syscall.EINVAL, "--list takes no --restart or --foreground"}
	}
	if c.kill && (len(dirs) > 0 || c.fg || c.restart || c.list) {
		return c, &errno{syscall.EINVAL, "--kill takes no directory, --foreground, --restart or --list"}
	}
	if c.addr != "" && c.sharing {
		return c, &errno{syscall.EINVAL, "-a and --share name one address each; give one"}
	}
	if c.secret && (!c.sharing || c.restart || c.list || c.kill) {
		return c, &errno{syscall.EINVAL, "--secret goes with --share, and not with --restart, --list or --kill"}
	}
	switch len(dirs) {
	case 0:
		// --list without a directory lists every tree, and --restart
		// without one serves no tree beyond those it restarts.
		if !c.list && !c.kill && !c.restart {
			c.dir = "."
		}
	case 1:
		c.dir = dirs[0]
	default:
		return c, &errno{syscall.EINVAL, "one directory at most, got " + strings.Join(dirs, " ")}
	}
	return c, nil
}
