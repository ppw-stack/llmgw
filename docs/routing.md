# 路由设计（v1）

## 1. 目标

v1 的路由只有一个动作：**根据请求协议和模型名，查到唯一的目标供应商**。

```
(protocol, model) ──查表──> Target{provider, base_url, upstream_model}
```

没有规则引擎、没有权重、没有优先级、没有 fallback、没有熔断。

## 2. 索引构建

启动或配置重载时，先把每个供应商解析成路由就绪形态（内置端点在这里展开两个 base URL），再遍历模型列表：

```go
for _, p := range providers {
    eff := p.Effective() // preset 或自定义 URL + 模型
    for _, ep := range []struct{protocol, baseURL string}{
        {openai, eff.OpenAIBaseURL},
        {anthropic, eff.AnthropicBaseURL},
    } {
        if ep.baseURL == "" {
            continue // 只填了一个 base URL 的供应商只开放对应协议
        }
        for alias, upstream := range eff.Models {
            k := key{ep.protocol, alias}
            if _, dup := idx[k]; dup {
                return fmt.Errorf("model %q duplicated under protocol %q", alias, ep.protocol)
            }
            idx[k] = Target{Provider: p.Name, BaseURL: ep.baseURL, UpstreamModel: upstream}
        }
    }
}
```

**唯一硬约束**：同一协议下模型名不能重复。冲突直接拒绝加载，报出两个来源。这样路由结果永远可预测。

## 3. 查询

```go
func (r *Router) Resolve(protocol, model string) (Target, error) {
    if t, ok := (*r.snapshot.Load())[key{protocol, model}]; ok {
        return t, nil
    }
    return Target{}, &ModelNotFound{Model: model, Protocol: protocol}
}
```

`ModelNotFound` 会提示该模型是否存在于另一个协议，帮助用户改 base_url：

```
model "glm-4.6" is not configured for the OpenAI protocol;
it is available via the Anthropic endpoint (/anthropic)
```

## 4. 协议隔离

协议来自 URL 前缀，在 ingress 阶段确定，路由层不接收 `headers`，从结构上杜绝跨协议命中。

## 5. 演进路径

`router` 包对外只暴露 `Resolve(ctx, protocol, model)`。将来引入规则时：

1. 在查表之前插入规则匹配；
2. 候选从单个 `Target` 变为 `[]Target`；
3. 选择策略（权重/优先级/fallback/熔断）在 router 内部实现。

ingress、transport、日志、管理台均无需改动。

## 6. 测试

- 表驱动：`(protocol, model)` → 期望 Target / 未命中。
- 冲突检测：同协议重复模型 → 加载失败，错误包含两个来源。
- 跨协议提示：模型只在另一协议存在时，错误信息给出提示。
- 热更新：替换快照后进行中的请求仍使用旧 Target。
