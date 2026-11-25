# fixturesverify

用 C 服务端产出的 fixtures 做**读路径对拍**，是服务端核心从 C 迁到 Go 的第一道验收门禁。

与 C 代码零共享实现，只读 fixtures 目录：不给 Go 侧留"照抄 C 实现"的余地，等价性要靠独立复现来证明。

## 跑法

```bash
# fixtures v1：llwsb001/seafile-compat-tests 仓库的 fixtures/v1（storage.tar 需一并带上）
go run . -fixtures /path/to/fixtures/v1 -passwd 123

# 打印每个库的目录树
go run . -fixtures /path/to/fixtures/v1 -passwd 123 -v
```

`storage/` 不存在时会把同目录的 `storage.tar` 自动解到临时目录，不用手工解包。

退出码 0 = 全部一致；非 0 表示有对象 ID 不一致或解密失败，输出会指明具体对象。

## 校验三层

| 层 | 内容 | v1 实测 |
|---|---|---|
| 对象层 | `expected/objects.jsonl` 里 113 个对象逐个复算 ID；fs 对象额外验证"规范化 JSON 能重现落盘字节" | 113/113，规范化 78/78 |
| 树层 | repo → head commit → root fs → 递归展开，核对文件大小、块引用、块 SHA1 | 3 个库、15 目录、8 文件 |
| 加密层 | enc_version=2 用口令解出仓库密钥，解密块并比对明文 | `secret.txt` 26 字节解出 |

## 对拍中确认的口径（写代码照这个来）

1. 对象路径 `<kind>/<repo_id>/<id 前 2 位>/<id 后 38 位>`。文件名只有 38 位，**取 ID 必须拼回父目录那 2 位**。
2. block ID = SHA1(内容原文)；version=1 的库固定 8MB 切块，CDC 只用于 version=0。
3. fs 对象 ID = SHA1(解压后的 JSON)，序列化口径 jansson `json_dumps(JSON_SORT_KEYS)`：键排序、`", "`、`": "`、中文不转义。
4. commit ID = SHA1(root_id[41] ‖ creator_id[41] ‖ creator_name+`\0` ‖ desc+`\0` ‖ hton64(ctime))，JSON 字段叫 `creator`。
5. fs/commit 对象 zlib 压缩，block 不压缩；ID 算解压后内容。
6. 空目录/空文件的 fs ID 是全 0（EMPTY_SHA1），不对应磁盘对象。
7. 虚拟库的 fs 对象存在源库里，只有 commit 在自己名下——所以看 `repos.json` 的 `origin_repo_id`。

## 加密库参数（enc_version=2）

固定 salt `da 90 45 c3 06 c7 cc 26`，PBKDF2-HMAC-SHA256 1000 次派生 32 字节 key、再 10 次派生 16 字节 iv；commit 里的 `key` 是 48 字节密文，用 AES-256-CBC 解出 32 字节 secret，再派生出真正的 enc_key/enc_iv；block 用 AES-256-CBC 加密，块 ID 是密文的 SHA1。

## 下一步

- 接 HTTP 层：读路径的 token / 下载 URL（当前只做对象层）
- enc_version 3/4（每库随机 salt、argon2 口令哈希）
- 冲突 commit（second_parent）场景，等 fixtures v2
