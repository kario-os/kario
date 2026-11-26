# 换机协议 kario-switch/1

> 2026-11-16 定稿的第一版，2026-11-17 按应用作者的说明补齐细节（§1、§2），同日把两台手机连同一个 Wi-Fi 的那条路补成正式的一条（二维码的 `wifi`，§1；Wi-Fi 变了时停下，§7）。手机一侧的接收端已实现（`switchrecv/`、`shell/switch.go`、设置 › 我的设备 › 从旧手机迁移，§7）：单元测试和测试向量 ✓，开发机自测（模拟的热点、模拟的 Wi-Fi）✓，QEMU 上两个镜像的开机自测 ✓（iwd 真的开热点，模拟的旧手机在自己的网络命名空间里连上，经防火墙连到 47800 端口传完、撤销）；同一 Wi-Fi 的 QEMU 用例（模拟的旧手机连手机连着的测试接入点）在两个镜像上 ✓，**未上真机**。旧手机上的「Kario 换机」应用（Expo，安卓和 iOS 一套代码）在 kario-internal 里写，**未上真机**。2026-11-18 加上不装应用、经蓝牙读旧手机的一条路（§8：Kario 当 PBAP、MAP 的客户端）：单元测试、录下的会话回放和开发机自测 ✓，QEMU 用例**还没通过**（暂时跳过，见 [13](13-prototype.md)），**未上真机**。为什么这样做见 kario-internal 的 `strategy/64-accounts-and-migration.md`。

## 0. 做什么

用户在旧手机（安卓或 iPhone）上装「Kario 换机」，用它扫 Kario 手机上的二维码，把通讯录、通话记录、短信、日历、照片和视频传过来。点对点，不经云端；传完接收端关掉，令牌作废。

| 数据 | 安卓 | iOS |
|------|------|-----|
| 通讯录 | ✓（`expo-contacts`） | ✓ |
| 日历 | ✓（`expo-calendar`） | ✓ |
| 照片、视频 | ✓（`expo-media-library`） | ✓ |
| 通话记录 | ✓（原生模块读 `CallLog`，要 `READ_CALL_LOG`） | 系统不给应用读；经蓝牙 PBAP 读（§8，Kario 像车机那样当电话本的客户端），**未上真机** |
| 短信 | ✓（原生模块读 `Telephony.Sms`，要 `READ_SMS`） | 系统不给应用读；iPhone 的 MAP 只给车机发新消息的通知，读不到以前的短信（§8） |

不想在旧手机上装应用时，Kario 也可以经蓝牙读它的通讯录、通话记录（安卓、iPhone）和短信（安卓），见 §8。

Google Play 只让默认短信 / 电话应用和几类例外用 `READ_SMS`、`READ_CALL_LOG`，例外里有「为用户备份和恢复」，要审核。国内多数用户从厂商商店装或直接装安装包：Kario 的接收页可以直接给出安装包（§6）。

## 1. 连接和配对

1. Kario 手机：设置 › 我的设备 › 从旧手机迁移。接收端起来：手机连着 Wi-Fi（有 IPv4 地址）时在 Wi-Fi 的地址上听（**Wi-Fi 路**，旧手机连同一个网络；用户可以改用热点，§7），否则打开个人热点（关着时打开，结束时再关掉）在热点的地址上听（**热点路**）；TCP 47800，HTTP；生成这次的 X25519 密钥对和 32 字节随机令牌。
2. 屏幕上显示二维码，内容是（热点路、Wi-Fi 路）：

   ```
   kario-switch://pair?v=1&host=192.168.43.1&port=47800&ssid=Kario-3F2A&psk=...&token=<base64url>&key=<base64url>&name=<设备名>
   kario-switch://pair?v=1&host=192.168.1.23&port=47800&wifi=<网络名>&token=<base64url>&key=<base64url>&name=<设备名>
   ```

   `key` 是接收端的 X25519 公钥（32 字节），`token` 是令牌，`ssid`、`psk` 是热点的：两个一起出现，或者都没有（Wi-Fi 路）。`wifi` 是 Wi-Fi 路上接收端连着的那个网络的名称（SSID），给应用核对、告诉用户连哪个网络，不带密码：只在 Wi-Fi 路上有，占 `ssid`、`psk` 的位置，和它们不同时出现（两样都有的码是坏的）；是 1 到 32 字节的 UTF-8 文本，名称不是这样（不是 UTF-8、太长）时不带，所以 Wi-Fi 路上也可能没有它（更早的接收端也不带），应用就只说「连 Kario 连着的那个 Wi-Fi」。`host` 是本地的 IPv4 地址（不是主机名）。查询串照 Go 的 `url.Values.Encode` 编码（空格写成 `+`，其他按百分号编码），字段按上面的顺序；`token`、`key` 这里和下面所有地方都是不带填充的 base64url（RFC 4648 §5）。装了应用的手机扫码直接打开应用（深链）。
3. 热点路：应用连上热点（安卓、iOS 都弹系统确认；不行就让用户手动连，Kario 的页面上写着热点的名称和密码）。Wi-Fi 路：应用不连任何网络，先看手机是不是连着 Wi-Fi、平台给得出时是不是 `wifi` 那个网络（或者 `host` 在不在手机的 Wi-Fi 子网里），不是就请用户去连这个网络；连不上接收端或超时时说出可能的原因（不在同一个网络；公共、酒店、办公室的 Wi-Fi 常常隔离设备，两台手机互相访问不了），建议在 Kario 上「改用热点」、再扫新的码。两条路都用 `host:port` 访问接收端。
4. 应用生成一次性的 X25519 密钥对，算出共享密钥：

   ```
   shared = X25519(app 私钥, key)
   k      = HKDF-SHA256(ikm=shared, salt=token, info="kario-switch/1", 64 字节)
   上行密钥 = k[0:32]（应用 → Kario），下行密钥 = k[32:64]
   ```

   `salt` 是令牌的 32 个原始字节，不是它的 base64url 文本。共享密钥全为零（对方的公钥是低阶点）时接收端拒收。

只有扫过这个二维码的人知道 `key` 和 `token`；热点的 WPA2 之外（Wi-Fi 路上网络是和别人共用的，没有这一层），内容再用这个密钥端到端加密，明文的 HTTP 上走的只有信封，所以接收端不用 HTTPS 证书，应用也不用做证书钉扎（RN 的 `fetch` 钉不了）。

## 2. 报文

所有请求和回复的正文都是**信封**：

```
信封 = nonce(12 字节) || AES-256-GCM(密钥, nonce, 明文, aad) || 标签(16 字节)
nonce = 方向(4 字节，上行 0x00000001，下行 0x00000002) || 计数(8 字节，大端，从 0 起，每条加 1)
aad   = HTTP 方法 + " " + 发出的请求目标（路径，有查询串时连同 "?" 和查询串，不含主机），如 "POST /v1/items/contacts"、"PUT /v1/files/7f3c?offset=0"
```

- 查询串在 aad 里，所以 `?offset=N` 也受保护。
- 每个应用公钥（§2.1）有自己的上行、下行计数，各自只增不减。接收端只收计数比这个公钥上一条打开过的信封大的信封；不大的是重放，回 409（`replay`），也不算一个请求（不重新计 30 分钟）。
- 回复用下行密钥、所答请求的 aad 加密，取这个公钥的下一个下行计数；每条加密的回复都用掉一个，出错的回复也一样。应用只要求下行计数比上一条大。
- 请求一条接一条地发，接收端也一次只处理一条。重试是新信封（新计数），不是重发原来的字节。
- 第一个请求 `POST /v1/hello` 在头 `X-Kario-Key` 里带应用的公钥，正文也是信封。之后的请求不再带：接收端用各公钥依次试着打开。
- 头 `Content-Type: application/octet-stream`；明文是 UTF-8 JSON，文件块（§2.3）是原始字节。
- 请求的正文（整个信封）至多 `max_body` 字节（4 MiB）。超过的不读，回 413（`too_large`），用上一条打开过的请求的公钥加密；还没有请求打开过时是明文 JSON。应用收到 413 把这一批分成两半再发。接收端不读超过的正文就回复并关掉连接，手机上的 HTTP 库（OkHttp、NSURLSession）可能先看到连接被重置、读不到 413，所以应用始终按 `max_body` 分批分块，413 只是兜底。
- 错误：回复正文是信封，明文 `{"error": "...", "code": "..."}`（`bad_offset` 另有 `offset`）。打不开的信封（还没 hello、公钥或令牌不对、会话已结束、aad 对不上）回 401，`Content-Type: application/json`，正文是明文 `{"error":"the envelope does not open","code":"bad_key"}`。

### 2.1 hello

```
POST /v1/hello
→ {"app": "kario-switch", "version": "1.0.0", "device": {"brand": "Xiaomi", "model": "...", "os": "android", "os_version": "14"},
   "counts": {"contacts": 812, "call_log": 2000, "sms": 5231, "calendar": 40, "photos": 3120, "videos": 88}}
← {"session": "<id>", "accept": ["contacts", "call_log", "sms", "calendar", "photos", "videos"], "max_batch": 500, "chunk": 1048576, "max_body": 4194304}
```

`accept` 是 Kario 收的种类（用户在 Kario 上勾选的，Kario 上没有去处的去掉，§7），应用只发这些。`app` 不是 `kario-switch` 时回 400（`bad_hello`）。`counts` 可以不全（应用在 `accept` 之后才逐类请求权限），后来的 hello 补上它带的那几类。

应用重启后用新密钥对再 hello 一次，令牌不变：接收端照收，同一会话接着用（去重、文件的进度都还在）。接收端记住这个令牌下见过的每个应用公钥和它最后的计数，所以旧的 hello 被重放时按它自己的计数是重放（409），不会把会话切回旧密钥。一个公钥的 hello 没成功之前，它的其他请求回 409（`no_hello`）。

### 2.2 条目

```
POST /v1/items/<种类>
→ {"items": [...], "last": false}
← {"stored": 500, "skipped": 0}
```

每批至多 `max_batch` 条（多了回 400 `too_many`）；最后一批 `last: true`。种类是 `contacts`、`call_log`、`sms`、`calendar`；别的回 404（`not_found`，照片、视频走 §2.3），不在 `accept` 里的回 403（`not_accepted`）。

重发同一批不重复导入：接收端按种类记下这次会话收下的每条的 SHA-256（按它读出的字段算，字段的顺序、空白不影响），同一条再来（同一批里或以后的批）算跳过；Kario 上已有的也算跳过（§7）。`stored` 是收下的，`skipped` 是跳过的；读不了的两边都不算，列在结束时的 `failed` 里。去处一时收不下（服务不在）时回 503（`unavailable`），这一批什么都没收，应用过一会再发。

每条可以带应用自己的 `id`（和文件的一样，`[A-Za-z0-9._-]{1,64}`），`failed` 里就用它。各种类的条目：

| 种类 | 条目 |
|------|------|
| `contacts` | `{"vcard": "BEGIN:VCARD\r\nVERSION:3.0\r\n..."}`，vCard 3.0 或 4.0（RFC 6350），照片内嵌（`PHOTO`，至多 256 KB）；整条至多 512 KB |
| `call_log` | `{"number", "name", "type": "in"/"out"/"missed"/"rejected", "at": Unix 秒, "duration": 秒}`；号码至多 64 字节，名字至多 128 个字 |
| `sms` | `{"address", "body", "type": "in"/"out", "at": Unix 毫秒, "read": true}`；彩信只传文字部分，正文不能是空的，至多 64 KB |
| `calendar` | `{"ics": "BEGIN:VEVENT\r\n...END:VEVENT"}`（RFC 5545，一个事件一条，重复规则照原样）：从 `BEGIN:VEVENT` 到 `END:VEVENT`，后面有没有 CRLF 都行，里面的组件（`VALARM`）配对，不能再套 `VEVENT` 或 `VCALENDAR`，至多 64 KB；`TZID` 是 IANA 时区名（`Asia/Shanghai`） |

### 2.3 文件

```
POST /v1/files            → {"id": "<应用给的稳定 id>", "kind": "photo"/"video", "name", "size", "mime", "taken_at": Unix 秒, "sha256": "<hex>"}
                          ← {"offset": 0}              已有一部分时是已收到的字节数，已收完是 size
PUT  /v1/files/<id>?offset=N  正文：一个信封，明文是从 N 起的至多 chunk 字节
                          ← {"offset": N + 块长}
```

- `id` 是 `[A-Za-z0-9._-]{1,64}`；文件的 `kind` 是单数 `photo` / `video`，要 `accept` 里有复数的 `photos` / `videos`，不然回 403（`not_accepted`）。`name` 取路径的最后一段、去掉控制字符，至多 128 个字；`size` 大于 0；`sha256` 是 64 位十六进制。不合要求回 400（`bad_offer`）。`mime` 不像 `类型/子类型` 时不用（Kario 按文件名猜）。
- 一个文件至多 4 GiB，超过回 507（`too_big`）；这次会话的文件加起来不能超过会话开始时磁盘的可用空间减 256 MiB，超过回 507（`no_space`）。两种都记进 `failed`。
- 同一个 `id` 再报一次：种类、大小、`sha256` 都一样时回已收到的字节数（收完了是 `size`），不一样时（旧手机上改过）从 0 重来。
- `PUT` 的 `offset` 不是已收到的字节数时回 400，`{"code": "bad_offset", "offset": <已收到的>}`（已收完的文件是 `size`）；块是 1 到 `chunk` 字节，不能超过 `size`，否则回 400（`bad_chunk`）；没报过的 `id` 回 404（`not_found`）。

断了以后应用再 `POST /v1/files` 问一次 `offset`，从那里接着传。收完时接收端校验 `sha256`，不对回 422（`bad_hash`）并丢掉这个文件（再报从 0 开始），`failed` 里记 `sha256`；存不进去回 500（`store_failed`）。

v1 照原样在开头报 `sha256`（应用传之前要先把文件读一遍算出来）；v2 可能改成传完再给，边传边算。

### 2.4 结束

```
POST /v1/done → {}
← {"contacts": 812, "call_log": 2000, "sms": 5231, "calendar": 40, "photos": 3120, "videos": 88, "failed": []}
```

数量是这次会话各类收下的。`failed` 是 `{"kind", "id", "reason"}` 的列表（至多 1000 项，后面的只计数）：条目的 `kind` 是它的种类，文件的是 `photo` / `video`；`id` 是文件的，或条目自带的（没有时为空）；`reason` 是一句英文短语，不含内容（`bad number`、`not a vcard`、`sha256`、`no space`…）。后来又收下的从列表里去掉。

之后接收端关掉，令牌作废（再来的请求都是 401），密钥丢弃，收了一半的文件删掉；30 分钟没有打开的请求也关掉（重放不算）。Kario 上列出这次导入的数量，可以一键撤销（删掉这次导进来的条目和文件，§7）。

### 2.5 错误

| 状态 | `code` | 什么时候 |
|------|--------|----------|
| 400 | `bad_json` | 明文不是该有的 JSON |
| 400 | `bad_hello` | `app` 不是 `kario-switch` |
| 400 | `too_many` | 一批超过 `max_batch` 条 |
| 400 | `bad_offer` | 文件的报告不合要求（§2.3） |
| 400 | `bad_offset` | `offset` 不是已收到的字节数，另带 `offset` |
| 400 | `bad_chunk` | 块是空的、超过 `chunk` 或超出 `size` |
| 401 | `bad_key` | 信封打不开（明文 JSON） |
| 403 | `not_accepted` | 种类不在 `accept` 里 |
| 404 | `not_found` | 没有的路径、种类或文件 |
| 409 | `replay` | 计数不比这个公钥上一条大 |
| 409 | `no_hello` | 这个公钥的 hello 还没成功 |
| 413 | `too_large` | 正文超过 `max_body` |
| 422 | `bad_hash` | 收完的文件 `sha256` 不对，已丢掉 |
| 500 | `store_failed` | 文件存不进去 |
| 503 | `unavailable` | 去处一时收不下这一批，稍后再发 |
| 507 | `too_big`、`no_space` | 文件太大，或放不下了 |

## 3. 安全（照 T/TAF 214-2024 的思路）

- 迁移前：Kario 上用户确认要收哪几类；旧手机上系统权限框逐类同意。
- 迁移中：只在两台手机之间传；内容端到端加密，密钥只在二维码里；令牌一次一用。二维码画在安全界面上（截屏、录屏、投屏和自测快照里遮住）。
- 迁移后：接收端关掉、密钥丢弃（只在内存里，不写盘）；旧手机上的应用不留副本（只读，不写本地缓存，退出时清空）。
- 接收端只接受热点或当前 Wi-Fi 上的地址，不听别的网卡：只绑在这一个 IPv4 地址上，从这块网卡的子网以外来的连接直接断开。Wi-Fi 路上同一网络里的别的设备也连得到这个端口，但没有二维码打不开信封（401）；Wi-Fi 断开、换了网络或者地址变了，接收端就停下（§7）。
- 日志只记种类和数量，不记密钥、令牌、二维码和内容；撤销用的记录只有 ID（§7）。

## 4. 版本

`v=1`。不兼容的改动换 `v=2` 和 `/v2/` 路径；加字段不换版本，未知字段忽略。二维码的 `wifi` 就是这样加的：不认识它的应用把没有 `ssid` 的码照旧当 Wi-Fi 路。

## 5. 测试

应用的测试里有一个照本协议写的模拟接收端，加解密有固定的测试向量（`switch/test/vectors.json`，在 kario-internal，用 Node 的 OpenSSL 生成：固定的密钥、令牌、派生的密钥和信封）。手机上的接收端用同一份向量（拷在 `switchrecv/testdata/switch-vectors.json`），逐字节对照两个方向：

- 双方的公钥、共享密钥、HKDF 的输出，从哪一边算都一样；拿令牌的 base64url 文本当 salt 算出的不一样。
- 配对 URL 逐字节相同，旧手机一侧读回来字段一样。向量里的码是热点路的，不带 `wifi`；Wi-Fi 路的码（向量的令牌和公钥，`wifi` 是带空格、`+` 和中文的名称）两边的测试里各写着同一段文字（`switchrecv/vectors_test.go` 的 `wifiPairingURL`、应用的 `switch/test/pairing.test.ts`），逐字节对照；带 `ssid` 时不带 `wifi`，不是 1 到 32 字节 UTF-8 的名称不带，两样都有的码旧手机一侧拒收。
- 每个信封加密出来就是向量里的字节，解开就是向量里的明文；坏了标签、密文、nonce，aad 少了查询串，换成下行密钥，短于 nonce 加标签的，都打不开。
- 向量里的整次会话对着接收端走一遍：随机数来源给出向量里的会话 id 时，hello、条目、文件报告、文件块、重放（409）、结束的回复都和向量逐字节相同；结束后再 hello 是向量里的 401。

`switchrecv/oldphone` 是照本文另写的旧手机一侧（不用接收端的代码），接收端的测试用它经 HTTP 测：一遍走完各类、重发去重和 Kario 已有的、去处不在时的 503、断点续传和应用重启后的第二次 hello、重放旧 hello 不切回旧密钥、改了一个字节的块（422）、重放（409）、错的令牌和公钥（401）、没勾选的种类（403）、超过 `max_body`（413，分两半再发）、`offset` 不对（400）、文件的上限（507）和报告不合要求、30 分钟（测试里缩短）没有请求就关掉。模糊测试 `switchrecv`：`FuzzEnvelope`（[17](17-app-sandbox.md) §9）。Shell 的单元测试（`shell/switch_test.go`）用假的 phone-smsd 和 telephonyd 走一遍导入和撤销；Wi-Fi 路：码里是网络名、不开热点，等着时改用热点换成热点的码，旧手机已经连上的不换；Wi-Fi 换了网络、地址变了、断开，热点被关掉，会话都停下、说出原因（断开只看到一次不算，两次才算）。开发机自测的「换机」「换机（同一 Wi-Fi）」和 QEMU 上的「换机（模拟器）」「换机（模拟器，同一 Wi-Fi）」用例见 [13](13-prototype.md)。

## 6. 没装应用的手机

通讯录还有一条不用装应用的路：经 CardDAV。旧手机加 Kario 账号（iPhone 设置里自带「添加 CardDAV 账户」，安卓装 DAVx⁵），或者通讯录本来就在 iCloud、Nextcloud、Fastmail 上，Kario 在设置 › 账号里登录同一个账号，通讯录就同步下来（[19](19-core-apps.md) §1.1，和真的服务器**未验证**）。通话记录、短信和照片还是要经本协议或下面的网页。

接收端的页面 `http://host:port/` 显示两样：「Kario 换机」安装包的下载（安卓），和只靠浏览器上传照片的页面（选文件上传，没有端到端加密，只靠热点的 WPA2 和令牌）。**未实现**（`GET /` 现在回 404）。

## 7. Kario 上的接收端

`switchrecv/` 是协议本身（只用 Go 标准库：`crypto/ecdh`、`crypto/hkdf`、AES-GCM、`net/http`），收到的东西经 `Sink` 接口交出去；`shell/switch.go` 把它接进 Shell，设置的页面在 `apps/settings/switch.go`，走仅系统应用可用的 `switch.state` / `switch.start` / `switch.stop` / `switch.undo`（[22](22-developer-api.md) §4.5）。

- **开始**：用户勾选种类（默认全勾）后开始。Shell 去掉 Kario 上没有去处的种类（没有 phone-smsd 时的短信、没有 telephonyd 时的通话记录）。Wi-Fi 连着（有 IPv4 地址）、热点关着时走 Wi-Fi 路：在 Wi-Fi 的地址上听，二维码带这个网络的名称（`wifi`）；否则走热点路：开着就用，关着就打开、结束时关掉（Shell 在会话中途退出时留个记号，下一个 Shell 启动时把它关掉，收了一半的文件也删掉）。只绑在这一个 IPv4 地址的 47800 端口，子网以外的连接断开。开发机上 Wi-Fi 和热点是模拟的，接收端听 `127.0.0.1`。
- **改用热点**：Wi-Fi 路上旧手机还没连上时，页面上的「改用热点」（`h`，也在选项里；`switch.start` 带 `hotspot`）结束这次会话，在热点上重新开始，换成热点的码（打开热点时 Wi-Fi 随之断开，结束后回来）；旧手机已经连上的会话不换。公共、酒店、办公室的 Wi-Fi 常常隔离设备，旧手机连不到 Kario 的地址，这时就用它。
- **网络变了**：会话每 2 秒看一次它听的网络（Wi-Fi 只问连着哪个网络和地址，不扫描）。Wi-Fi 断开（连着两次看到都断着才算，漫游时一次断开不算）、换到别的网络、地址变了，或者热点被关掉（飞行模式），会话就结束：监听关掉、令牌作废、自己打开的热点关掉，不会一直等到 30 分钟；页面最上面写着原因（「接收已停止：手机从 Wi-Fi「A」换到了「B」」这样），已经收到的留着（这次导入可以撤销，结束方式记成 `network`），Wi-Fi 路上还有「改用热点接收」从热点重新开始。旧手机那边连接断开，再扫新的码。
- **二维码**：设置的页面打开就是二维码（就在说明下面，`qr:` 图片节点，标成安全界面，和飞钱的付款码同一条路：像素渲染器画成帧图，文本渲染器用半格字符画），下面是「等待旧手机…」和怎么连：热点路写着「经 Kario 的热点接收」和热点的名称、密码（手动连时用）；Wi-Fi 路写着「经 Wi-Fi 接收」和网络名、「旧手机要连同一个 Wi-Fi 网络」，以及旧手机连不上时为什么、怎么办（改用热点）；最后是安全界面的说明。页面换到另一个状态（等着、在收、结束）时像新开的页面一样从头显示。配对 URL 约 210 字节，是 53 个模块宽的码，文本渲染器 40 列的屏幕画不下（被裁掉，扫不了）；只能在像素渲染器上扫。
- **进度**：每类收下、跳过、失败的条数，和旧手机说它有的条数；旧手机的品牌、型号和系统。结束后列出这次导入每类加了多少、几项失败。
- **去处**：

  | 种类 | 去哪里 |
  |------|--------|
  | 通讯录 | Shell 的通讯录（`contacts` 包读 vCard：FN / N / ORG 的名字、TEL 的号码和类型、EMAIL 的邮箱、ADR 的地址、内嵌的照片 PHOTO（JPEG、PNG、GIF，至多 1 MB，原样存）、NOTE、NICKNAME、ORG 的单位和 TITLE 的职位、BDAY 的生日和 ANNIVERSARY / X-ANNIVERSARY / iPhone 标成纪念日的 X-ABDATE（不带年份的也认，iPhone 的 X-APPLE-OMIT-YEAR 也是）、URL 的网址；`CATEGORIES` 不用）；没有名字也没有号码的算失败；同名、号码都已有的跳过；导进来的联系人进默认的通讯录（设置 › 账号 › 新联系人存到；没有账号时只在手机上），在账号的通讯录里的 20 秒后随同步传到服务器（[19](19-core-apps.md) §1.1） |
  | 通话记录 | telephonyd 的 `log.import`（[04](04-ui-language-telephonyd.md)）：号码去掉空格、横线、括号和点，要 3–20 个数字、`*`、`#`（`+` 在头），否则失败；`in` 记成接听、`out` 拨出、`missed` 未接、`rejected` 拒接；旧手机的名字不用（Kario 按通讯录显示）。按时间排进通话记录，上限 200 条照旧，比最新 200 条还早的留不下（算跳过）；同一号码、方向、时间的已有就跳过 |
  | 短信 | phone-smsd 的 `import`（[32](32-messaging.md)）：已读未读照旧手机的，发出的记成已发送，标成导入的，不发通知；按时间排进会话；同一会话、方向、时间、正文的已有就跳过 |
  | 日历 | 这次会话的事件先存成「文件」里的一个 `.ics`（「日历 <日期>.ics」，`VCALENDAR` 包着这些 `VEVENT`，不带 `VTIMEZONE`，每收一批重写一次，至多 16 MB），会话结束后交给日历应用（[19](19-core-apps.md) §1）在后台导入，通知里报数；撤销时先让日历拿掉这一次导入的，再删这个文件。没有日历应用时文件留在「文件」里 |
  | 照片、视频 | 「文件」：收的时候放在「文件」目录里的暂存处，收完改名进去（不读进内存），一个至多 4 GiB；名字照「文件」的规矩至多 64 个字，重名的加「 (2)」这样的编号；时间是拍摄时间。蓝牙发送和网页的文件框会把文件整个读进内存，超过 16 MB 的文件不列在那里 |

- **撤销**：会话的记录（`<状态目录>/shell/switch.json`）只有它加的联系人、短信、通话记录和文件的 ID，重启后也能撤销；只留最近一次加了东西的导入。「撤销这次导入」（先确认）按 ID 删联系人和文件，让 phone-smsd 的 `delete_imported` 和 telephonyd 的 `log.delete` 删导入的短信和通话记录（只删标成导入的）；哪一边一时删不了（服务不在）就留着它的 ID，可以再撤销一次。导入后改过的联系人照样删掉。
- **没做的**：§6 的网页；iPhone 的短信（它的 MAP 不给，§8）；Wi-Fi 路在真的路由器（客户端隔离、双频同名网络、IPv6）上**未上真机**；开始之前就选热点（现在先在 Wi-Fi 上开始，再「改用热点」）；文本渲染器上画得下的二维码。
## 8. 经蓝牙迁移（旧手机上不装应用）
设置 › 我的设备 › 从旧手机迁移 › 「经蓝牙迁移」（`b`）：旧手机上什么都不装，Kario 当蓝牙的**客户端**去读它。电话本和通话记录用 PBAP（Kario 是 PCE；安卓手机和 iPhone 都有给车机用的 PBAP 服务端），短信用 MAP（Kario 是 MCE；安卓手机有 MAP 服务端，iPhone 的只给车机发新消息的通知，读不到以前的短信）。读来的交给 §7 接收端的同一个去处（`shell/switch_bt.go` 用 `switchSink`）：同样认出 Kario 上已有的、同样的记录、同样的「撤销这次导入」。照片、视频、日历不走这条路。OBEX 是 Kario 自己的（`obex` 包）：镜像里没有 obexd（它要 libical 和 ICU，39 MB；`yocto/meta-phone` 的 bluez5 bbappend 去掉了 `obex-profiles`），所以不用 obexd 的 `org.bluez.obex.Client1`、`PhonebookAccess1`、`MessageAccess1`，客户端也写在 `obex` 包里，和服务端一样跑在 BlueZ 交来的套接字上（Profile1）。
1. **配对**：先在设置 › 蓝牙里和旧手机配对（核对配对码）。Kario 发布 PBAP 客户端（PCE）的服务记录（BlueZ 的外部 profile，`Role` 是 `server`：只有记录，没有通道），旧手机看得到 Kario 是电话本的客户端；MAP 客户端的 profile 是 `client`，Kario 不发布短信服务。
2. **选手机**：「经蓝牙迁移」页列出已配对的设备，选一台；勾选的种类里只取通讯录、通话记录、短信（Kario 上没有去处的照样去掉）。`switch.start` 带 `bluetooth`（设备地址），见 [22](22-developer-api.md) §4.5。配对时 BlueZ 读到的服务（`UUIDs`）里没有电话本或短信服务的，那几类记成「旧手机不经蓝牙提供这些」。
3. **旧手机上允许**：Shell 经 BlueZ 连上（`Device1.ConnectProfile`，PBAP 服务端 0x112F、MAP 服务端 0x1132），发 OBEX 的 CONNECT（Target 是 PBAP、MAP 的 UUID）。旧手机这时问它的用户（安卓弹出读通讯录和通话记录、读短信的请求；iPhone 照车机的做法，要在蓝牙设置里给这台设备打开同步通讯录，**未验证**），CONNECT 的回答等用户点完才来：Kario 等 90 秒，页面写着「请在「…」上允许」，没有二维码。拒绝（Forbidden 之类）或没回答，那几类记成「旧手机上没有允许」。之后每个回答至多等 30 秒，等不到就断开，已经收下的留着。
4. **读电话本**：先问每个对象有多少条（`MaxListCount` 为 0，回答里的 `PhonebookSize`；页面上是「x / y」），再 100 条一页地读（`ListStartOffset`、`MaxListCount`）。要 vCard 2.1（车机都要 2.1，手机写 2.1 最稳；回 3.0 的照读），属性选择器只要 VERSION、FN、N、TEL、ORG、NOTE、X-IRMC-CALL-DATETIME（不要照片）。`telecom/pb.vcf` 的 0 号是手机自己的名片（PBAP 这样规定）：读按句柄排的列表（`x-bt/vcard-listing`，只要 1 条），第一张是 `0.vcf` 就不导入它；不能列表的手机照规定当作有。通话记录读 `ich`（接听）、`och`（拨出）、`mch`（未接），三个都没有时读 `cch`；类型看 `X-IRMC-CALL-DATETIME` 的参数（2.1 的 `;MISSED`，3.0 的 `;TYPE=MISSED`），没有就看文件夹；时间不带时区的按 Kario 的时区，带 `Z` 或 UTC 偏移的照读。PBAP 没有通话时长（记成 0），安卓（AOSP 的写法）把拒接的电话也算在接听里。服务端是 PBAP 1.2 或以后的（服务记录里的版本，BlueZ 在 NewConnection 里给）才在 CONNECT 里带 `PbapSupportedFeatures`（下载、浏览）。
5. **读短信**：进 `telecom/msg`，读 `inbox`、`sent` 的列表（`x-bt/MAP-msg-listing`，100 条一页，`FilterMessageType` 去掉邮件），每条再 GetMessage（`x-bt/message`，`Attachment` 为 0、`Charset` 为 UTF-8，这两个 MAP 规定必带）。号码、时间、已读未读取列表里的（收到的取发件人，发出的取收件人，几个收件人取第一个；列表里没有号码时取 bMessage 里 vCard 的），正文是 bMessage 里 `BEGIN:MSG` 到 `END:MSG` 之间的（不信 `LENGTH`，手机算法不一；分成几段的接起来），彩信取 MIME 里的 `text/plain` 部分。照 §2.2 的规矩检查（号码 64 字节以内、正文不空、64 KB 以内、有时间），不合的算失败。iPhone 的短信服务列表是空的、没有 `sent`，页面写「经蓝牙只过来了一部分」。
6. **交给去处**：每页一批交给 `switchSink`（通讯录、通话记录、短信），和 §7 一样认出 Kario 上已有的（跳过）；同一次迁移里同一条再来（不认 `ListStartOffset` 的手机每页都给全部）按内容的 SHA-256 跳过，不重复计数。去处一时不在（phone-smsd、telephonyd 重启）时这一批隔一秒再交，交 3 次还不行就算失败。
7. **结束、撤销**：读完（或用户按「停止」：链路马上关掉）后，这次导入和 §7 的一样记进 `switch.json`（只有 ID，外加每类没全过来的原因），页面列出各类的数量，可以「撤销这次导入」。什么都没过来时页面写原因（没有允许、手机不经蓝牙提供、Kario 上都有了）。日志只记种类、数量和设备地址。
### 8.1 OBEX 的互通
| 项 | 做法 |
|----|------|
| OBEX over L2CAP（GOEP 2.0） | BlueZ 交来 L2CAP（`SOCK_SEQPACKET`）时一次读写一个 OBEX 包，包不超过链路的发送 MTU（`BT_SNDMTU`）和对方 CONNECT 里说的大小；RFCOMM 上是字节流 |
| 单响应模式（SRM） | 只在 L2CAP 上用。客户端在一个操作的第一个包里带 `SRM: enable`，对方的第一个回答也带时，GET 不再一个回答一个请求，PUT 只等最后一个包的回答；对方回答里的 `SRMP: wait` 让 PUT 等下一个回答。服务端（车机读 Kario 的电话本、推文件）一样：GET 的回答连着发完（请求里的 `SRMP: wait` 让它等下一个请求），PUT 只回第一个和最后一个包；中途拒收（太大）时回一次错误，这个操作后面的包不再回答 |
| Connection ID、Target、Who | 客户端的 CONNECT 带 Target，之后每个请求的第一个头是对方给的 Connection ID（对方没给就不带）；回答里的 Who 不检查（有的手机不带）。服务端给有 Target 的会话一个 Connection ID，请求不带的照收（有的车机不带），带了别的回 Service Unavailable（0xD3） |
| PBAP 的特性位 | 服务端是 1.2 或以后的时，CONNECT 带 `PbapSupportedFeatures` = 0x00000003（下载、浏览）；更早的不带应用参数 |
| vCard 属性选择器 | 客户端要 VERSION、FN、N、TEL、ORG、NOTE、X-IRMC-CALL-DATETIME（第 0、1、2、7、16、17、28 位）。Kario 的服务端照车机的选择器写：2.1 里只写选中的和 N、TEL，3.0 里总有 FN，没选通话时间就不写它；没有选择器或为 0 时全写 |
| 手机的毛病 | 不认 `MaxListCount` 为 0（直接给全部）、不认偏移（每页都给全部或第一页）、不给 `PhonebookSize`、要 2.1 给 3.0、没有 0 号名片、vCard 折行和 quoted-printable 的软换行、2.1 的 `AGENT` 里套名片、不严格的列表 XML（大小写、HTML 实体、没闭合）、MAP 的时间带或不带 UTC 偏移、bMessage 的 `LENGTH` 不对、正文分段：都照读 |
| 没做的 | OBEX 认证（对方要认证时回 Unauthorized，算没有允许）；GetMessage 会不会把旧手机上的短信标成已读**未验证**；有几个短信服务实例的手机（安卓另有邮件的实例）BlueZ 连第一个记录，**未上真机** |
### 8.2 测试
- `obex` 包：客户端对着照协议写的脚本对端，逐字节查 CONNECT（Target，1.2 的服务端才带特性位）、Connection ID 在第一个、拉电话本的类型、名字和应用参数（属性选择器按位）、MAP 的 SETPATH 和 GetMessage 必带的参数、只在 L2CAP 上带 SRM；SRM 的 GET（一个请求收完）、对方不开 SRM 时退回一问一答、PUT 的 SRMP 等待；Kario 服务端的 SRM（PUT 两个回答、GET 一个请求、中途拒收后会话照样对得上、请求里的 SRMP 等待）、Connection ID 不对回 0xD3、属性选择器；旧手机不回答时 CONNECT 超时、拒绝、对象超过上限。解析：安卓和 iPhone 写法的 vCard（`obex/btphone/fixtures/`）的拆分、通话记录、时间格式、短信列表、bMessage（收、发、彩信、分段、`native` 编码、坏的）。模糊测试 `FuzzOldPhone`（旧手机怎么回答，拉取都结束、不超上限）、`FuzzParsers`（[17](17-app-sandbox.md) §9）。
- `obex/btphone`：模拟的旧手机（安卓：2.1、quoted-printable、0 号名片、收件箱两条短信一条彩信、已发送一条；iPhone：3.0、没有 0 号名片、短信服务的列表是空的、没有 `sent`；可以设成不认偏移），它的用户可以允许、拒绝、不回答。客户端对着它在字节流和 L2CAP 那样的包链路上读全部。**录下的会话**（`obex/btphone/testdata/*.session`：L2CAP 上的 PBAP 会话和 RFCOMM 上的 MAP 会话，逐包十六进制，`-update` 重录）回放时客户端发的每个包都要和录的一样；以后从真手机抓的（btmon）照这个格式放进来就能回放。现在这两个会话是对着模拟的手机录的，不是真手机的。
- Shell（`shell/switch_bt_test.go`，假的 phone-smsd 和 telephonyd）：安卓旧手机整个迁移（不要它自己的名片、隐藏的号码算失败、彩信的文字、未读、带 UTC 偏移的时间、日志里没有内容），撤销，撤销后再迁、再迁一次都已有；iPhone（短信只过来一部分）；旧手机上拒绝电话本、允许短信；等允许时按停止；没有这些服务的耳机、没配对的设备、没有 phone-smsd 时。
- 开发机自测「换机（蓝牙）」：模拟的安卓旧手机和 iPhone（`bt-oldphone`；它的用户的回答是 `bt-oldphone-allow`），设置里配对、经蓝牙迁移、查通讯录、短信、最近通话、撤销 ✓。QEMU 上的「换机（模拟器，蓝牙）」：第二个 btvirt 控制器扮旧手机，BlueZ 上真的走配对、SDP、ConnectProfile 和 RFCOMM（BlueZ 交来的都是 RFCOMM 连接，L2CAP 上的单响应模式在 QEMU 上没走到），QEMU 上通过 ✓（2026-11-19；2026-11-20 一度只在设了 `KARIO_SELFTEST_BT_OLDPHONE` 时跑：btvirt 有时死于 SIGBUS，是它的主循环调用已删掉的定时器的回调，层里打上 BlueZ 的修复后又一直跑，见 [13](13-prototype.md)）；之前让它失败的是设置 › 蓝牙页面上过时的刷新，见 [13](13-prototype.md)。
- **未上真机**：没有和真的安卓手机、iPhone 试过；它们的允许框、iPhone 让不让一台手机（不是车机）读电话本、真实的数据量和速度都没验证。
- **撤销**：会话的记录（`<状态目录>/shell/switch.json`）只有它加的联系人、短信、通话记录和文件的 ID，重启后也能撤销；只留最近一次加了东西的导入。「撤销这次导入」（先确认）按 ID 删联系人和文件，让 phone-smsd 的 `delete_imported` 和 telephonyd 的 `log.delete` 删导入的短信和通话记录（只删标成导入的）；哪一边一时删不了（服务不在）就留着它的 ID，可以再撤销一次。导入后改过的联系人照样删掉。登录了通讯录账号时，已经传到服务器上的这些联系人在下一次同步时也从服务器上删掉（服务器上没人改过它们的话；改过的按「改动胜过删除」回到手机上，[19](19-core-apps.md) §1.1）。
- **没做的**：§6 的网页；iOS 的通话记录、短信（蓝牙 PBAP、MAP）；Wi-Fi 路在真的路由器（客户端隔离、双频同名网络、IPv6）上**未上真机**；开始之前就选热点（现在先在 Wi-Fi 上开始，再「改用热点」）；文本渲染器上画得下的二维码。
