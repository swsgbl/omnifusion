// seed.go 是能力分数据的内置种子：官方 feed 的冻结副本（seed.json，
// 随二进制分发），全新安装且首次拉取不可达（典型：直连受限网络）时
// @quality 依然开箱即用。签名 feed 拉取成功或 store 重放都会整体覆盖
// 种子——种子只兜底，不参与防回滚与验签链路。
// 维护纪律：发布新版本官方 feed（catalog/feed.json 升 version）时，
// 将其内容同步拷贝到 seed.json 一起提交。
package catalogfeed

import (
	_ "embed"
)

//go:embed seed.json
var seedJSON []byte

// SeedFeed 解析内置种子（结构校验与正式 feed 同一入口；损坏返回错误，
// 调用方跳过即可——种子是纯兜底）。
func SeedFeed() (*Feed, error) {
	return ParseFeed(seedJSON)
}
