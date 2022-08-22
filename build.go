package main

import _ "embed"

//go:generate curl https://raw.githubusercontent.com/v2fly/geoip/release/geoip.dat -o geoip.dat

//go:embed geoip.dat
var geoipdat []byte
