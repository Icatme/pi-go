# pi-go / pi-ai 同步计划

## 目标
将 pi-ai (TypeScript) 最新功能变更同步到 pi-go (Go)，保持核心语义和行为一致。

## 约束
- 仅实现 OpenAI OAuth 和 Kimi Coding provider
- 不新增其他 provider 实现
- 不移植本地 AI / 本地推理支持
- 优先保证测试覆盖和行为一致性

---

## 阶段一：类型系统与核心语义同步

### 文件：`pkg/pigo/types.go`

- [ ] 添加 `ModelThinkingLevel` 类型（`"off" | ThinkingLevel`）
- [ ] 添加 `ThinkingLevelMap` 类型：`map[ModelThinkingLevel]string`
- [ ] 在 `Model` 结构体中添加 `ThinkingLevelMap` 字段
- [ ] 在 `AssistantMessage` 中添加 `ResponseModel string` 字段
- [ ] 在 `AssistantMessage` 中添加 `Diagnostics []AssistantMessageDiagnostic` 字段
- [ ] 添加 `TextSignatureV1` 结构体：`{ V int; ID string; Phase string }`
- [ ] 添加 `ProviderResponse` 结构体：`{ Status int; Headers map[string]string }`
- [ ] 将 `Transport` 和 `ThinkingBudgets` 从 `complete.go` 移入 `types.go`
- [ ] 更新所有引用这些类型的文件

### 文件：`pkg/pigo/models.go`

- [ ] 添加 `GetSupportedThinkingLevels(model Model) []ModelThinkingLevel`
- [ ] 添加 `ClampThinkingLevel(model Model, level ModelThinkingLevel) ModelThinkingLevel`
- [ ] 更新 `SupportsXHigh` 支持 `sonnet-4-6` 模式

### 测试：`pkg/pigo/models_test.go`

- [ ] 添加 `GetSupportedThinkingLevels` 测试
- [ ] 添加 `ClampThinkingLevel` 测试
- [ ] 更新 `SupportsXHigh` 测试覆盖新模型

---

## 阶段二：溢出检测增强

### 文件：`pkg/pigo/overflow.go`

- [ ] 添加 `nonOverflowPatterns` 正则列表，排除限流/服务不可用错误：
  - `^(Throttling error|Service unavailable):`
  - `/rate limit/i`
  - `/too many requests/i`
- [ ] 在 `IsContextOverflow` 中先检查 `nonOverflowPatterns`，匹配则返回 false
- [ ] 添加 Xiaomi MiMo 风格检测：
  - `stopReason == "length" && usage.output == 0 && input+cacheRead >= contextWindow*0.99`
- [ ] 添加 `request_too_large` 和 `context[_ ]length[_ ]exceeded` 模式

### 测试：`pkg/pigo/overflow_test.go`

- [ ] 添加限流错误被排除的测试
- [ ] 添加 Xiaomi MiMo `length` + `output=0` 检测测试
- [ ] 添加 `request_too_large` (Anthropic 413) 测试

---

## 阶段三：消息转换增强

### 文件：`pkg/pigo/transform_messages.go`

- [ ] 添加 `downgradeUnsupportedImages()` 函数：
  - 当 `model.input` 不包含 `"image"` 时，将 `ImageContent` 替换为占位符文本
  - 用户消息占位符：`"(image omitted: model does not support images)"`
  - 工具结果占位符：`"(tool image omitted: model does not support images)"`
  - 合并连续的占位符为单个
- [ ] 在 `TransformMessages` 开头调用 `downgradeUnsupportedImages`
- [ ] 确保 `isSameModel` 比较包含 `model.id` 精确匹配

### 测试：`pkg/pigo/transform_messages_test.go`

- [ ] 添加非视觉模型图片降级测试
- [ ] 添加工具结果中图片降级测试
- [ ] 添加连续占位符合并测试

---

## 阶段四：API 注册表功能扩展

### 文件：`pkg/pigo/api_registry.go`

- [ ] 在 `apiRegistryEntry` 中添加 `sourceId string` 字段
- [ ] 修改 `RegisterAPIModule` 签名支持 `sourceId`
- [ ] 添加 `UnregisterAPIModules(sourceId string)` 函数
- [ ] 添加 `ListAPIModules() []API` 函数
- [ ] 添加 `GetAPIModule(api API) *APIModule` 函数

### 测试：`pkg/pigo/api_registry_test.go`

- [ ] 添加 `sourceId` 注册和注销测试
- [ ] 添加 `ListAPIModules` 测试

---

## 阶段五：环境变量与认证系统

### 文件：`pkg/pigo/auth_env.go` / `pkg/pigo/auth.go`

- [ ] 扩展 `GetEnvAPIKey` 支持多环境变量查找：
  - GitHub Copilot: `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN`
  - Anthropic: `ANTHROPIC_OAUTH_TOKEN` 优先于 `ANTHROPIC_API_KEY`
- [ ] 添加 `FindEnvKeys(provider Provider) []string` 函数
- [ ] 保持 `.env` 文件读取行为

### 测试：`pkg/pigo/auth_test.go`

- [ ] 添加多环境变量查找测试
- [ ] 添加 `FindEnvKeys` 测试

---

## 阶段六：诊断与工具函数

### 新文件：`pkg/pigo/diagnostics.go`

- [ ] `DiagnosticErrorInfo` 结构体
- [ ] `AssistantMessageDiagnostic` 结构体
- [ ] `FormatThrownValue(error) string`
- [ ] `ExtractDiagnosticError(error) DiagnosticErrorInfo`
- [ ] `CreateAssistantMessageDiagnostic(type string, error any, details map[string]any) AssistantMessageDiagnostic`
- [ ] `AppendAssistantMessageDiagnostic(message *AssistantMessage, diagnostic AssistantMessageDiagnostic)`

### 新文件：`pkg/pigo/json_parse.go` + `json_parse_test.go`

- [ ] `RepairJSON(json string) string` - 转义控制字符、修复无效转义
- [ ] `ParseJSONWithRepair(json string) (any, error)`
- [ ] `ParseStreamingJSON(partialJSON string) map[string]any` - 处理不完整 JSON

### 新文件：`pkg/pigo/session_resources.go`

- [ ] `SessionResourceCleanup` 类型
- [ ] `RegisterSessionResourceCleanup(cleanup SessionResourceCleanup) func()`
- [ ] `CleanupSessionResources(sessionId string)`

---

## 阶段七：选项构建同步

### 文件：`pkg/pigo/simple_options.go`

- [ ] 确保 `buildBaseProviderStreamOptions` 中 `maxTokens` 默认值为 `min(model.MaxTokens, 32000)`
- [ ] 确保 `SimpleStreamOptions` 包含所有 TS 版本字段

### 文件：`pkg/pigo/provider_options_common.go`

- [ ] 确保 `CommonProviderOptions` 包含 `OnResponse` 回调字段
- [ ] 确保 `ProviderStreamOptions` 包含 `OnResponse` 字段

---

## 阶段八：工具参数验证增强

### 文件：`pkg/pigo/validation.go`

- [ ] 评估添加类型强制转换（coercion）逻辑
- [ ] 保持现有 `anyOf`/`oneOf`/`allOf` 支持
- [ ] 确保 `enum` 验证在类型验证之前执行

### 测试：`pkg/pigo/validation_test.go`

- [ ] 补充 `anyOf`/`oneOf` 测试
- [ ] 补充 `enum` + `type` 组合测试

---

## 阶段九：测试同步

| 测试文件 | 同步内容 |
|---------|---------|
| `transform_messages_test.go` | 图片降级测试 |
| `transform_messages_additional_test.go` | redacted thinking、signed empty thinking |
| `overflow_test.go` | NON_OVERFLOW 排除、Xiaomi MiMo 检测 |
| `models_test.go` | `GetSupportedThinkingLevels`、`ClampThinkingLevel` |
| `toolcall_ids_test.go` | OpenAI Responses ID 规范化 |
| `context_json_test.go` | 完整 content block 覆盖 |
| `auth_test.go` | OAuth 刷新、多环境变量 |
| `stream_test.go` | 背压、丢弃事件计数 |
| `registry_test.go` | API 注销、sourceId 追踪 |
| `handoff_test.go` | Kimi<->Codex 双向场景 |

---

## 阶段十：Provider 保持兼容

- [ ] 不新增 provider（Google, Mistral, Azure 等）
- [ ] 检查 `provider_openai_codex.go` 流式处理是否需要同步
- [ ] 检查 `provider_kimi.go` 流式处理是否需要同步
- [ ] 确保 `provider_anthropic_protocol.go` 与 TS 版本工具命名一致

---

## 实施顺序建议

1. `types.go` - 作为基础
2. `overflow.go` - 核心稳定性功能
3. `transform_messages.go` - 核心兼容性功能
4. `models.go` - reasoning level 支持
5. `api_registry.go` - 注册机制增强
6. `diagnostics.go`, `json_parse.go`, `session_resources.go` - 新增辅助模块
7. `auth_env.go` - 环境集成增强
8. 所有测试文件补充
