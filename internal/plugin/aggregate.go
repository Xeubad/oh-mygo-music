package plugin

import "encoding/json"

// Aggregate 是聚合来源的可选控制接口；普通来源不需要实现这些本机设置操作。
// 返回公开状态，避免界面读取插件普通存储或凭据来拼装自己的业务状态。
func (p *Plugin) Aggregate(action string, input map[string]any) (json.RawMessage, error) {
	var out json.RawMessage
	err := p.call("aggregate", map[string]any{"action": action, "input": input}, &out)
	return out, err
}
