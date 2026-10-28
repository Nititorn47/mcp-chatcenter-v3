# pam-chatcenter-mcp-proxy

MCP proxy สำหรับ Chat Center API ที่รับ MCP จาก client แล้วส่งต่อไปยัง
`/chatcenter/api/v3/mcp` พร้อมใส่ `Authorization: Bearer <secret>` ให้เอง

รองรับ 2 transport:

- `http` — เปิด `POST /mcp` บนพอร์ต 8080 (ใช้กับ centermcp platform)
- `stdio` — newline-delimited JSON-RPC บน stdin/stdout (ใช้กับ Claude Desktop/Cursor)
- `both` — เปิดทั้งสองแบบพร้อมกัน

## Environment

| ตัวแปร | ค่าเริ่มต้น | ความหมาย |
| --- | --- | --- |
| `CHATCENTER_MCP_URL` | (required) | URL ของ MCP ปลายทาง เช่น `https://chatcenter.example.com/chatcenter/api/v3/mcp` |
| `CHATCENTER_API_BEARER` | (ว่าง) | Bearer secret ของ Chat Center API (ใส่หรือไม่ใส่คำว่า `Bearer ` ก็ได้) |
| `CHATCENTER_ALLOW_CLIENT_BEARER` | `true` | ถ้า client ส่ง `Authorization` มาเอง ให้ใช้ค่านั้นแทน env |
| `CHATCENTER_HTTP_TIMEOUT` | `60s` | timeout ของ request ไปยัง upstream |
| `CHATCENTER_TLS_INSECURE` | `false` | ข้ามการตรวจ TLS (ใช้เฉพาะ dev) |
| `MCP_TRANSPORT` | `http` | `http`, `stdio` หรือ `both` |
| `MCP_HTTP_ADDR` | `:8080` หรือ `:$PORT` | listen address ของ HTTP (centermcp ใส่ `PORT` ให้อัตโนมัติ) |
| `MCP_HTTP_PATH` | `/mcp` | path ของ MCP endpoint |

Query string ของ client จะถูก merge กับ query ของ `CHATCENTER_MCP_URL`
จึงใช้ `?tools=...&mode=readonly` ของ Chat Center API ผ่าน proxy ได้

## รันในเครื่อง

```bash
cp .env.example .env
set -a && . ./.env && set +a
go run .
```

ทดสอบ:

```bash
curl -sS http://127.0.0.1:8080/healthz
curl -sS -X POST http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'
```

โหมด stdio สำหรับ Claude Desktop:

```json
{
  "mcpServers": {
    "chatcenter": {
      "command": "/path/to/mcp-proxy",
      "env": {
        "MCP_TRANSPORT": "stdio",
        "CHATCENTER_MCP_URL": "https://chatcenter.example.com/chatcenter/api/v3/mcp",
        "CHATCENTER_API_BEARER": "your-secret"
      }
    }
  }
}
```

## Docker

```bash
docker build -t pam-chatcenter-mcp-proxy:local .
docker run --rm -p 8080:8080 --env-file .env pam-chatcenter-mcp-proxy:local
```

`docker-build.sh` ใช้ contract เดียวกับ centermcp build worker:

```bash
printf '%s' "$DOCKER_HUB_TOKEN" | bash ./docker-build.sh <image-repository> <image-tag> <docker-hub-username>
```

สคริปต์จะ login, build ด้วย `docker buildx` ตาม `PLATFORMS` (ค่าเริ่มต้น
`linux/amd64`) แล้ว push image ให้

## Deploy ผ่าน centermcp

1. push repository นี้ขึ้น GitHub (HTTPS)
2. สร้าง MCP แบบ `custom` ใน centermcp โดยระบุ repository นี้
3. ตั้งค่า environment ในหน้า MCP:
   - `CHATCENTER_MCP_URL`
   - `CHATCENTER_API_BEARER` (ทำเครื่องหมายเป็น secret)
4. กด Start — platform จะ build ด้วย `docker-build.sh` ใน repo นี้ และ probe
   `initialize` ที่ `/mcp` ให้อัตโนมัติ

## พฤติกรรมของ proxy

- ใส่ `Content-Type: application/json` และ
  `Accept: application/json, text/event-stream` ให้ทุก request
- ตัด `Origin`, `Cookie`, `Host` และ header ที่ไม่จำเป็นออก ไม่ส่งต่อไป upstream
- forward `MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name`, `Mcp-Session-Id`,
  `Last-Event-ID`, `Idempotency-Key` ตามที่ client ส่งมา
- ถ้าเป็น protocol `2026-07-28` และ client ไม่ได้ส่ง `Mcp-Method`/`Mcp-Name`
  จะสร้างจาก JSON-RPC body ให้
- โหมด stdio จะจำ `protocolVersion` จาก `initialize` และ `Mcp-Session-Id`
  จาก response เพื่อใช้กับ request ถัดไป
- SSE response ถูกส่งต่อแบบ streaming ทั้ง HTTP และ stdio
