package main

import (
	pkga "example.com/m/path-a/pkg"
	pkgb "example.com/m/path-b/pkg"
)

var values []interface{}

var (
	g []interface{}
)

func main() {
	g = append(g, pkga.NewIfaceDirect(), pkga.NewIfaceInDirect(), pkgb.NewIfaceDirect(), pkgb.NewIfaceInDirect())

	_ = *(*int)(nil)
}
