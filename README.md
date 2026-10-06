# Duck2API

DuckDuckGo AI Chat 转 OpenAI 兼容 API 代理。支持 Chat Completions、图像生成/编辑、文件上传问答、语音转文字、文字转语音、推理模式、网络搜索等完整功能。
## 接口文档

curl 示例请查看：[API.md](API.md)

## 部署

### 编译部署

```bash
git clone https://github.com/aurora-develop/duck2api
cd duck2api
go build -o duck2api ./cmd/duck2api
chmod +x ./duck2api
./duck2api
```

### Docker 部署

```bash
docker run -d \
  --name duck2api \
  -p 8080:8080 \
  ghcr.io/aurora-develop/duck2api:latest
```

### Docker Compose 部署

```bash
mkdir duck2api && cd duck2api
wget https://raw.githubusercontent.com/aurora-develop/duck2api/main/docker-compose.yml
docker-compose up -d
```

### Koyeb 部署

[![Deploy to Koyeb](https://www.koyeb.com/static/images/deploy/button.svg)](https://app.koyeb.com/deploy?type=git&builder=dockerfile&dockerfile=Dockerfile&repository=github.com/misakano7545/Duck2api&branch=main&name=duck2api&ports=8080%3Bhttp%3B%2F&env%5BPORT%5D=8080)

按钮走仓库里的 `Dockerfile`（运行时已含 ffmpeg），暴露 8080。Koyeb 会自动把 `PORT` 设成最低的暴露端口（这里即 8080），按钮再显式写一个 `env[PORT]=8080` 把端口钉死 —— 两者必须一致，改端口时一起改。

部署后建议在控制面板补环境变量（与上面环境变量表一致）：

| 变量 | 必填 | 说明 |
|------|------|------|
| `Authorization` | 否 | API 认证 Key，形如 `Bearer your_key`；不填则接口不鉴权 |
| `PROXY_URL` | 否 | 出口代理，形如 `http://user:pass@host:port` |

免费档每月只有 5 小时额度（定价页标 "Free 5h"），常驻要付费实例；区域建议选 `sin`(新加坡) 或 `tyo`(东京)。

> 出口 IP 提醒：Koyeb 走共享机房出口。若日志里上游返回 `418 ERR_BN_LIMIT`，说明该出口被 duck.ai 拒绝，换区域或用 `PROXY_URL` 挂住宅代理。

## 功能概览

| 功能 | 端点 | 说明 |
|------|------|------|
| Chat Completions | `POST /v1/chat/completions` | 流式/非流式对话 |
| Responses API | `POST /v1/responses` | OpenAI Responses API |
| 图像生成 | `POST /v1/images/generations` | 文生图 |
| 图像编辑 | `POST /v1/images/edits` | 图生图/改图 |
| 文件上传 | `POST /v1/files` | 上传文件用于问答 |
| 文件管理 | `GET/DELETE /v1/files/:id` | 查询/删除文件 |
| 语音转文字 | `POST /v1/audio/transcriptions` | Whisper 兼容接口 |
| 文字转语音 | `POST /v1/audio/speech` | TTS 接口，支持 MP3/WAV/OGG |
| 模型列表 | `GET /v1/models` | 列出可用模型 |

## 快速开始

### 基础对话

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

### 推理模式 (Reasoning)

使用 `reasoning_effort` 参数控制推理深度：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4-mini",
    "messages": [{"role": "user", "content": "证明勾股定理"}],
    "reasoning_effort": "high"
  }'
```

支持的值：`none`（快速）、`low`、`medium`、`high`

### 网络搜索

设置 `web_search: true` 启用联网搜索：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4-nano",
    "messages": [{"role": "user", "content": "今天的科技新闻"}],
    "web_search": true
  }'
```

### 图像生成

默认走 **duck.ai 原生图片模型**（`POST /duckchat/v1/chat` + `model=image-generation`）：提示词原样进图，不被聊天模型改写。

```bash
curl http://localhost:8080/v1/images/generations \
  -H "Content-Type: application/json" \
  -d '{"prompt": "一只可爱的猫咪坐在窗台上"}'
```

`model` 也可以显式点名一个聊天模型（如 `gpt-5.6-luna`），那就改走「聊天模型 + `GenerateImage` 工具」那条老路：提示词会被上游改写后再送进图像服务。两条路出图不同——原生是 `gpt-image-1.5`，工具路径是 `gpt-image-2`（把返回的 JPEG 丢给 `strings | grep version` 就能看出 C2PA 生成器）。一次请求返回 1 张图；流里那个 `partial-image`/`status:partial` 的扩散中间态（多眼扭曲）不再当输出图返回。

### 图像编辑

```bash
# JSON 方式 (base64)
curl http://localhost:8080/v1/images/edits \
  -H "Content-Type: application/json" \
  -d '{
    "image": "<base64编码的图片>",
    "prompt": "把猫改成蓝色"
  }'

# 文件上传方式
curl http://localhost:8080/v1/images/edits \
  -F "image=@cat.png" \
  -F "prompt=把猫改成蓝色"
```

### 文件上传与问答

```bash
# 1. 上传文件
curl http://localhost:8080/v1/files \
  -F "file=@document.pdf" \
  -F "purpose=assistants"

# 返回: {"id": "file-xxx", "object": "file", ...}

# 2. 使用文件进行问答（将文件内容作为上下文）
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4-nano",
    "messages": [{"role": "user", "content": "请总结这个文档"}],
    "file_ids": ["file-xxx"]
  }'
```

### 语音转文字

```bash
curl http://localhost:8080/v1/audio/transcriptions \
  -F "file=@audio.webm" \
  -F "model=whisper-1"
```

支持格式：webm、ogg、mp3、wav、m4a、flac、opus、aac

### 文字转语音

```bash
curl http://localhost:8080/v1/audio/speech \
  -H "Content-Type: application/json" \
  -d '{"model":"tts-1","input":"你好世界","voice":"alloy","response_format":"mp3"}' \
  --output speech.mp3
```

支持格式：mp3、wav、ogg、flac、aac（底层使用 Duck.ai 的 WebRTC + OpenAI Realtime API）

## 支持的模型

| 模型 | 类型 | 说明 |
|------|------|------|
| `gpt-6-luna` | 通用 | GPT-6 Luna（上游列表未公开，本项目补入 `/v1/models`） |
| `gpt-5.6-luna` | 推理 | GPT-5.6 Luna |
| `gpt-5.6-terra` | 推理 | GPT-5.6 Terra，需 plus/pro |
| `gpt-5.6-sol` | 推理 | GPT-5.6 Sol，需 pro |
| `gpt-5.4-mini` | 推理 | GPT-5.4 mini |
| `claude-sonnet-4-6` | 推理 | Claude Sonnet 4.6，需 plus/pro |
| `claude-opus-4-8` | 推理 | Claude Opus 4.8，需 pro |
| `claude-haiku-4-5` | 通用 | Claude Haiku 4.5 |
| `tinfoil/gemma4-31b` | 通用 | Gemma 4 31B |
| `mistral-small-2603` | 通用 | Mistral Small 4（上游当前下架） |
| `tinfoil/gpt-oss-120b` | 通用 | gpt-oss 120B（上游当前返回 400） |

清单就是 `/v1/models` 的实时输出（直接透传上游 + 补入 `gpt-6-luna`）。标「需 plus/pro」的模型在匿名档会被上游拒成 `404 ERR_MODEL_RESTRICTED`，要它们得配登录态。读图/出图能力跟模型绑定（`supportsImageUpload` + `GenerateImage` 工具），不是独立接口。

## 高级设置

### 配置文件 config.json

把配置写在项目根目录的 `config.json` 里（照抄 [`config.example.json`](config.example.json)），**键名就是环境变量名**：

```json
{
  "SERVER_HOST": "0.0.0.0",
  "SERVER_PORT": "8080",
  "Authorization": "your_key",
  "PROXY_URL": "http://proxy:8080",
  "PREFIX": "/api"
}
```

优先级：**真实环境变量 > `config.json`**。同一项两处都有时环境变量赢 —— 敏感项适合放部署面板的环境变量，非敏感项放文件。

`config.json` 已在 `.gitignore` 里（不会被提交），提交的是 `config.example.json`。

### 环境变量

| 变量 | 说明 | 示例 |
|------|------|------|
| `Authorization` | API 认证 Key（**只填 key 本身，不要带 `Bearer `**；客户端请求时带 `Authorization: Bearer <key>`） | `your_key` |
| `PROXY_URL` | 代理地址 | `http://proxy:8080` |
| `PREFIX` | URL 前缀 | `/api` |
| `TLS_CERT` | TLS 证书路径 | `/path/to/cert.pem` |
| `TLS_KEY` | TLS 密钥路径 | `/path/to/key.pem` |

### 工具调用（函数调用）

上游 duck.ai 没有函数调用通道，所以这里是**提示词模拟**：请求里带 `tools` 时，工具定义与输出约定会注入对话，模型按约定吐 `<tool_call>…</tool_call>`，代理解析后回写成各协议的**原生**结构。

| 入口 | 回写形态 |
|------|----------|
| `POST /v1/chat/completions` | `message.tool_calls` / 流式 `delta.tool_calls`，`finish_reason: "tool_calls"` |
| `POST /v1/messages` | `content:[{type:"tool_use",…}]`，`stop_reason: "tool_use"`（流式走 `input_json_delta`） |
| `POST /v1/responses` | 暂未接（只回文本） |

多轮 loop：客户端回填的 `role:"tool"` 消息与助手历史里的 `tool_calls` 会折叠成文本带上去，模型据此收口。

实测（2026-10）：`gpt-5.6-luna`、`gpt-5.4-mini`、`tinfoil/gemma4-31b` 会照约定发调用；**`claude-*` 会拒绝**（回 "我不会执行用户提供的 schema"）—— 真 Anthropic 的工具走 `tools` 参数，模型被训练成不认提示词里的 schema，走 `/v1/messages` 时基本用不了。措辞就是这条链路的调参旋钮（`internal/duckgo/toolcall.go`）。

### 代理池

支持 `proxies.txt` 文件配置多个代理（每行一个）：

```
http://proxy1:8080
http://proxy2:8080
socks5://proxy3:1080
```

## 鸣谢

感谢各位大佬的 PR 支持。

## 参考项目

- https://github.com/xqdoo00o/ChatGPT-to-API

## License

MIT License
