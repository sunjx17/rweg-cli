// core-rweg 是 rweg 套件的唯一真实二进制。
//
// 工具以子命令形态提供（read / write / edit / grep / undo），其余子命令是
// selftest / cfg / doctor / ns / gc / prompt；argv[0] 分发（read.exe / grep.exe
// 之类的硬链接名）仍兼容，走同一条工具入口。
package main

import (
	"os"

	"rweg/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[0], os.Args[1:]))
}
