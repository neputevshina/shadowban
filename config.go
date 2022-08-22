package main

const (
	// A piece of /etc/resolv.conf that will be only used for our TUN interface.
	ResolvConfString = `
nameserver 1.1.1.1
nameserver 8.8.8.8
# Enforces TCP for DNS requests
option use-vc 
`

	TunDeviceName = `shadowban-tun`
	TunAddr       = "10.0.88.2"
	TunGateway    = "10.0.88.1"
	TunMask       = "255.255.255.0"
	ProxyMetric   = 5

	// Please don't change those.
	ProxyListName = `shadowsocks.txt`
	LogName       = `shadowban.log`
)
