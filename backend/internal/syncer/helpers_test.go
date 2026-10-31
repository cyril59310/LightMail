package syncer

import (
	"net/url"
	"strconv"
)

type urlT = url.URL

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

func fmtInt(n int) string { return strconv.Itoa(n) }
