package main

// TODO: refactor this atrocity, it is supposed to be readable as one file
// also see the "TODO: this is so slow" comment

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"io"
	"log"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	oss "github.com/Jigsaw-Code/outline-go-tun2socks/outline/shadowsocks"
	"github.com/Jigsaw-Code/outline-go-tun2socks/shadowsocks"
	"github.com/eycorsican/go-tun2socks/core"
	"github.com/eycorsican/go-tun2socks/proxy/dnsfallback"
	"github.com/eycorsican/go-tun2socks/tun"
	"github.com/fsnotify/fsnotify"
	"github.com/mattn/go-gtk/glib"
	"github.com/mattn/go-gtk/gtk"
	"golang.org/x/sys/unix"
)

const (
	openLog  = "openlog"
	openList = "openlist"
)

type proxy struct {
	displayName,
	flag,
	host, password, cipher string
	port int
}

var (
	status        = "Disconnected"
	selectedProxy = 0
	proxies       []proxy
	menu          *gtk.Menu
	filesig       <-chan []proxy
	disconnectsig chan struct{}
	pipe          *os.File // Only as superuser
	gateway       string
)

func listpath() string {
	if os.Getuid() == 0 {
		panic("can't get list of root")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		log.Panic(err)
	}
	return path.Join(dir, ProxyListName)
}

func configpath() string {
	if os.Getuid() == 0 {
		panic("can't get list of root")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		log.Panic(err)
	}
	return dir
}

func logpath() string {
	if os.Getuid() == 0 {
		panic("can't get log of root")
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		log.Panic(err)
	}
	return path.Join(dir, LogName)
}

func xdgopen(path string) error {
	return NewShell().Exec("xdg-open", path).Start()
}

func composeMenu() {
	if menu != nil {
		menu.Destroy()
	}
	menu = gtk.NewMenu()

	i := gtk.NewMenuItemWithLabel(status)
	i.SetSensitive(false)
	menu.Append(i)

	if len(proxies) == 0 {
		i := gtk.NewMenuItemWithLabel("No servers")
		i.SetSensitive(false)
		menu.Append(i)
	}
	for pi, p := range proxies {
		i := gtk.NewImageMenuItem()
		l := gtk.NewLabel("")
		l.SetSingleLineMode(true)
		l.SetAlignment(0, 0)
		if pi == selectedProxy && status != "Disconnected" {
			l.SetMarkup("<b>" + p.displayName + "</b>")
		} else {
			l.SetMarkup(p.displayName)
		}
		i.Add(l)
		pi2 := pi
		i.Connect("activate", func() {
			selectedProxy = pi2
			go tun2socks(p.port, p.password, p.cipher, p.host, disconnectsig)
			tunenable(gateway, proxies[selectedProxy].host)
			status = "Connected"
			composeMenu()
		})
		menu.Append(i)
	}
	menu.Append(gtk.NewSeparatorMenuItem())

	i = gtk.NewMenuItemWithLabel("Edit list")
	i.Connect("activate", func() {
		pipe.Write([]byte(openList))
	})
	menu.Append(i)

	i = gtk.NewMenuItemWithLabel("Open logs (todo, use stdout)")
	i.SetSensitive(false)
	// i.Connect("activate", func() {
	// 	pipe.Write([]byte(openLog))
	// })
	menu.Append(i)

	i = gtk.NewMenuItemWithLabel("About")
	i.Connect("activate", func() {
		about := gtk.NewAboutDialog()
		about.SetProgramName("Shadowban")
		about.SetComments(`One-executable Shadowsocks/Outline client`)
		about.SetWebsite(`https://github.com/neputevshina/shadowban`)
		about.SetCopyright(`© neputevshina 2022`)
		about.Run()
		about.Destroy()
	})
	menu.Append(i)

	if status == "Disconnected" {
		i = gtk.NewMenuItemWithLabel("Quit")
		i.Connect("activate", func() {
			gtk.MainQuit()
		})
		menu.Append(i)
	} else {
		i = gtk.NewMenuItemWithLabel("Disconnect")
		i.Connect("activate", func() {
			tundisable(gateway)
			disconnectsig <- struct{}{}
			status = "Disconnected"
			composeMenu()
		})
		menu.Append(i)

		i = gtk.NewMenuItemWithLabel("Disconnect and quit")
		i.Connect("activate", func() {
			tundisable(gateway)
			gtk.MainQuit()
		})
		menu.Append(i)
	}

	menu.ShowAll()
	return
}

func filewatch() <-chan []proxy {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Println("can't create watcher:", err)
	}

	err = watcher.Add(path.Dir(*watch))
	if err != nil {
		log.Println("can't watch proxy list path:", err)
	}
	fw := make(chan []proxy, 0)

	go func() {
		defer watcher.Close()
		for {
			select {
			case err, ok := <-watcher.Errors:
				if !ok {
					panic("close???")
				}
				log.Println("watch error:", err)

			case event, ok := <-watcher.Events:
				if !ok {
					panic("close???")
				}
				println("read")
				if event.Op == fsnotify.Write {
					px, err := parseProxies()
					if err != nil {
						log.Println(px)
					}
					fw <- px
				}

			}
		}
	}()
	return fw
}

func parseProxies() (px []proxy, err error) {
	f, err := os.Open(*watch)
	defer f.Close()
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	lines := -1
	for {
		s, err := br.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return px, nil
			}
			return px, err
		}
		lines++
		s = s[:len(s)-1]
		s = bytes.TrimSpace(s)
		if s[0] != '#' {
			host, port, ci, pw, fail := parseURL(string(s))
			if fail {
				log.Println("on line ", lines)
			}
			px = append(px, proxy{
				displayName: string(pw[len(pw)-4:]) + "@" + host,
				host:        host,
				port:        port,
				password:    pw,
				cipher:      ci,
				flag:        "unknown", // TODO geoipdat
			})
		}
	}
}

func touch(p string) {
	f, err := os.Open(p)
	defer f.Close()
	if errors.Is(err, os.ErrNotExist) {
		f, _ := os.Create(p)
		_ = f.Close()
	}
}

func chaintoggleipv6(sh *Shell, enable string) {
	if sh.Error != nil {
		return
	}
	// Disable IPv6 to mitigate traffic leaking (as they say in outline_proxy_controller).
	// Write directrly to /proc/sys instead of using sysctl so change will not survive reboot.
	// Enables Windows-like fix if something eventually fucked up.
	f, err := os.OpenFile("/proc/sys/net/ipv6/conf/all/disable_ipv6", os.O_WRONLY, 0600)
	defer f.Close()
	if err == nil {
		_, err = f.Write([]byte(enable))
	}
	sh.Error = err
}

func tuninit() (gateway string, err error) {
	// taken, deconvoluted and interpolated from outline_proxy_controller. must be root
	var out []byte
	sh := NewShell()
	out, sh.Error = sh.Output("ip tuntap list")
	if sh.Error == nil && !bytes.Contains(out, []byte(TunDeviceName)) {
		sh.ChainRun("ip tuntap add dev", TunDeviceName, "mode tun")
	}
	sh.MustChainRun("ip addr replace", TunAddr+"/24", "dev", TunDeviceName)
	sh.MustChainRun("ip link set", TunDeviceName, "up")

	sh.MustChainRun("resolvconf -a", TunDeviceName, In{bytes.NewBuffer([]byte(ResolvConfString))})

	// we try to detect the best interface as early as possible before
	// outline mess up with the routing table. But if we fail, we try
	// again when the connect request comes in
	defaultvia, err := sh.Output("ip route show default")
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`via (.*) dev (.*)`)
	m := re.FindSubmatch(defaultvia)
	if m == nil {
		panic("no link or they've changed “ip” command interface?:\n" + string(defaultvia))
	}
	// I can't find any reference to those in outline_proxy_controller's code
	// clientToServerRoutingInterface := m[2]
	// clientLocalIP := m[3]
	return string(m[1]), sh.Error
}

func tunenable(gateway, ip string) error {
	sh := NewShell()
	// TODO: it will add every proxy address to route list thus polluting it.
	sh.ChainRun("ip route replace", ip, "via", gateway, "metric", ProxyMetric)
	sh.ChainRun("ip route change default via", TunAddr)
	chaintoggleipv6(sh, "1")
	return sh.Error
}

func tundisable(gateway string) error {
	sh := NewShell()
	sh.ChainRun("ip route change default via", gateway)
	chaintoggleipv6(sh, "0")
	return sh.Error
}

// Copy-pasted from github.com/Jigsaw-Code/outline-go-tun2socks/outline/electron/main.go
// TODO: replace this utter garbage with custom (refactored from go-shadowsocks2) shadowsocks impl
// BIG TODO: replace ugly c dependency with a pure go tcp/ip stack.
// 	we are using it just for reading and writing from and to a “file” which a tun device really
// 	seems to be.
func tun2socks(proxyPort int, proxyPassword, proxyCipher, proxyHost string, kill <-chan struct{}) {
	const (
		mtu        = 1500
		udpTimeout = 30 * time.Second
		persistTun = true // Linux: persist the TUN interface after the last open file descriptor is closed.
	)
	var args struct {
		tunAddr           string
		tunGw             string
		tunMask           string
		tunName           string
		tunDNS            string
		proxyHost         string
		proxyPort         int
		proxyPassword     string
		proxyCipher       string
		logLevel          string
		checkConnectivity bool
		dnsFallback       bool
		version           bool
	}

	args.tunAddr = TunAddr  // "TUN interface IP address"
	args.tunGw = TunGateway // "TUN interface gateway"
	args.tunMask = TunMask  // "TUN interface network mask; prefixlen for IPv6"
	args.tunName = TunDeviceName
	args.proxyHost = proxyHost
	args.proxyPort = proxyPort
	args.proxyPassword = proxyPassword
	args.proxyCipher = proxyCipher
	args.logLevel = "info"
	args.dnsFallback = false
	args.checkConnectivity = false
	args.version = false

	// Validate proxy flags
	if args.proxyHost == "" {
		log.Panic("Must provide a Shadowsocks proxy host name or IP address")
	} else if args.proxyPort <= 0 || args.proxyPort > 65535 {
		log.Panic("Must provide a valid Shadowsocks proxy port [1:65535]")
	} else if args.proxyPassword == "" {
		log.Panic("Must provide a Shadowsocks proxy password")
	} else if args.proxyCipher == "" {
		log.Panic("Must provide a Shadowsocks proxy encryption cipher")
	}

	if args.checkConnectivity {
		connErrCode, err := oss.CheckConnectivity(args.proxyHost, args.proxyPort, args.proxyPassword, args.proxyCipher)
		log.Println("Connectivity checks error code:", connErrCode)
		if err != nil {
			log.Println("Failed to perform connectivity checks:", err)
		}
		return
	}

	// Open TUN device
	dnsResolvers := strings.Split(args.tunDNS, ",")
	tunDevice, err := tun.OpenTunDevice(args.tunName, args.tunAddr, args.tunGw, args.tunMask, dnsResolvers, persistTun)
	if err != nil {
		log.Panic("Failed to open TUN device:", err)
	}
	// Output packets to TUN device
	core.RegisterOutputFn(tunDevice.Write)

	// Register TCP and UDP connection handlers
	core.RegisterTCPConnHandler(
		shadowsocks.NewTCPHandler(args.proxyHost, args.proxyPort, args.proxyPassword, args.proxyCipher))
	if args.dnsFallback {
		// UDP connectivity not supported, fall back to DNS over TCP.
		log.Println("Registering DNS fallback UDP handler")
		core.RegisterUDPConnHandler(dnsfallback.NewUDPHandler())
	} else {
		core.RegisterUDPConnHandler(
			shadowsocks.NewUDPHandler(args.proxyHost, args.proxyPort, args.proxyPassword, args.proxyCipher, udpTimeout))
	}

	// Configure LWIP stack to receive input data from the TUN device
	lwipWriter := core.NewLWIPStack()
	go func() {
		_, err := io.CopyBuffer(lwipWriter, tunDevice, make([]byte, mtu))
		if err != nil {
			log.Println("Failed to write data to network stack:", err)
			return
		}
	}()

	log.Println("tun2socks running...")
	<-kill
	lwipWriter.Close()
	log.Println("manual connection close")
}

var formats = `	Supported formats:
	- ss://<cipher>:<password>@<server ip>:<port>[/<anything>]
	- ss://<base-64 encoded cipher and password>@<server ip>:<port>[/<anything>] (Outline)
	- ss://<base-64 encoded connection data>[#<optional tag>]`

func parseURL(s string) (addr string, port int, cipher, password string, fail bool) {
	addr, port, cipher, password = parseurl(s)
	if cipher == "" {
		log.Println("must provide server address and port")
		fail = true
	}
	if password == "" {
		log.Println("must provide server address and port")
		fail = true
	}
	if addr == "" {
		log.Println("must provide server address and port")
		fail = true
	}
	return
}

var base64Regexp = regexp.MustCompile(`ss://([A-Za-z_0-9-]+)(#(.+))?$`)
var base64DecodedRegexp = regexp.MustCompile(`(.+):(.+)@(.+:[0-9]{1,6})`)
var outlineRegexp = regexp.MustCompile(`ss://([A-Za-z_0-9-]+)@(.+:[0-9]{1,6})`)

func parseurl(s string) (addr string, port int, cipher, password string) {
	split := func(hostport string) (host string, port int) {
		a := strings.Split(hostport, ":")
		if len(a) != 2 {
			log.Println("can't recognize addr:port pair ", hostport)
			return
		}
		port, _ = strconv.Atoi(a[1])
		return a[0], port
	}

	if ss := base64Regexp.FindStringSubmatch(s); base64Regexp.MatchString(s) {
		dat, err := base64.RawURLEncoding.DecodeString(ss[1])
		if err != nil {
			log.Println(err)
			return
		}
		// Shadowsocks config “specification” gives an example of a password containing
		// a slash, a semicolon and an '@'.
		// Let's suppose that someone actually generated a password containing
		// those characters and not try to parse it as an URL.
		so := base64DecodedRegexp.FindSubmatch(dat)
		if so == nil {
			log.Println("can't recognize base64 encoded url ", string(dat))
			return
		}
		cipher = string(so[1])
		password = string(so[2])
		addr, port = split(string(ss[2]))
		return
	} else if ss := outlineRegexp.FindStringSubmatch(s); outlineRegexp.MatchString(s) {
		dat, err := base64.RawURLEncoding.DecodeString(ss[1])
		if err != nil {
			log.Println(err)
			return
		}
		bs := bytes.Split(dat, []byte{':'})
		cipher = string(bs[0])
		password = string(bs[1])
		addr, port = split(string(ss[2]))
		return
	}

	u, err := url.Parse(s)
	if err != nil {
		log.Panic("")
	}
	if u.User == nil {
		return u.Host, 0, "", ""
	}
	password, _ = u.User.Password()
	addr, port = split(addr)
	cipher = u.User.Username()
	return
}

var display = flag.String("display", "", "")
var xauthority = flag.String("xauthority", "", "")
var pipename = flag.String("pipe", "", "")
var watch = flag.String("watch", "", "")

// FIXME: work with sudo.
func main() {
	log.Default().SetFlags(log.Lshortfile | log.LstdFlags)
	rand.Seed(time.Now().Unix())
	flag.Parse()
	where, err := os.Executable()
	if err != nil {
		log.Panic(err)
	}
	if os.Getuid() != 0 { // Require elevation.
		// Create those files as a normal user for convenience.
		touch(listpath())
		touch(logpath())

		*pipename = "/tmp/shadowban" + strconv.FormatUint(rand.Uint64(), 16)
		cmd := exec.Command(
			"pkexec", where,
			"-display", os.Getenv("DISPLAY"),
			"-xauthority", os.Getenv("XAUTHORITY"),
			"-pipe", *pipename,
			"-watch", listpath(),
		)
		// TODO: logs
		// logfile, err := os.OpenFile(logpath(), os.O_APPEND, 0600)
		// if err != nil {
		// 	log.Panic(err)
		// }
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Start()

		// Bodge to mitigate a stalled pipe.
		go func() {
			cmd.Wait()
			os.Exit(0)
		}()
		if err != nil {
			log.Panic("no pkexec?:" + err.Error())
		}

		unix.Mkfifo(*pipename, 0600) // root can write to anything
		pipe, err := os.OpenFile(*pipename, os.O_RDONLY, os.ModeNamedPipe)
		defer pipe.Close()
		if err != nil {
			log.Panic(err)
		}
		buf := make([]byte, 8192)
		// TODO: this is so slow i can feel it, find ways to open a text editor faster.
		// maybe i should just make it other way around and keep the smaller service part
		// in root where gui actually runs as normal user.
		// also it does not work with sudo.
		for {
			n, err := pipe.Read(buf)
			if err == io.EOF {
				return
			} else if err != nil {
				panic(err)
			}
			switch string(buf[:n]) {
			case openLog:
				xdgopen(logpath())
			case openList:
				xdgopen(listpath())
			}
		}
	}
	// Superuser zone.

	pipe, err = os.OpenFile(*pipename, os.O_RDWR, os.ModeNamedPipe)
	defer pipe.Close()
	if err != nil {
		log.Fatal("probaby you have sudoed, can't open a pipe: ", err)
	}

	os.Setenv("XAUTHORITY", *xauthority)
	os.Setenv("DISPLAY", *display)
	gtk.Init(nil)
	glib.SetApplicationName("shadowban")

	gateway, err = tuninit()
	if err != nil {
		log.Panic("can't set up tun interface:" + err.Error())
	}

	proxies, err = parseProxies()
	if err != nil {
		log.Panic("can't parse proxies:" + err.Error())
	}
	filesig = filewatch()
	disconnectsig = make(chan struct{}, 0)
	composeMenu()

	si := gtk.NewStatusIconFromStock(gtk.STOCK_NETWORK)
	si.SetTitle("Shadowban")
	si.SetTooltipMarkup("Shadowban")
	si.Connect("popup-menu", func(cbx *glib.CallbackContext) {
		select {
		case p := <-filesig:
			println("upd")
			proxies = p
		default:
		}
		menu.Popup(nil, nil, gtk.StatusIconPositionMenu, si, uint(cbx.Args(0)), uint32(cbx.Args(1)))
	})

	gtk.Main()
}
