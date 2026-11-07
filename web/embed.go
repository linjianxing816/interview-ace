// Package web 把前端构建产物编译进二进制。
//
// 用 embed 而不是 StaticFile：StaticFile 依赖运行时工作目录，
// 从别的目录启动服务就会 404。嵌进去之后二进制自带前端，拷哪都能跑。
//
// dist 是 `npm run build` 的产物（Vite 生成，文件名带 hash）。
// 改了前端必须重新 build 再 go build，否则嵌进去的还是旧版本——
// 这个顺序搞反过一次，表现为「代码明明改了，页面没变」。
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
