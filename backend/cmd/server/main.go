package main

import (
	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/mdm-customer/backend/module"
)

// ⚠️ 这个文件永远只有这一行。装配的全部逻辑（Bootstrap OTel、开池、连
// NATS、Listen HTTP 与 gRPC、装信号处理器、优雅关停）都在
// besdk.RunStandalone 里，而它是全组件唯一允许读进程环境变量的地方
// （§12.5.3）。
//
// 合并进外壳时，外壳启动器调的是同一个 module.New——这是 §1.5 原则二
// 「合并只发生在部署形态上」唯一能被机器守住的形态，也是 §13.7 拆回
// 门禁能过的前提。
func main() { besdk.RunStandalone(module.New) }
