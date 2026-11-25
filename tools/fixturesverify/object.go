package main

// 对象层：定位、解压、复算 ID。
//
// 这些规则来自 C 服务端源码实测（seafile-server 6b13248，fixtures v1 全量对拍）：
//   - 对象路径是 <kind>/<repo_id>/<id 前 2 位>/<id 后 38 位>；只读文件名只有 38 位。
//   - block 存原文，ID = SHA1(内容)。
//   - fs / commit 对象 zlib 压缩存储（首字节 0x78），ID 算解压后的字节。
//   - version=1 的库按固定 8MB 切块，CDC 只用于 version=0 的库。

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	BlockSizeV1 = 8 * 1024 * 1024
	emptySha1   = "0000000000000000000000000000000000000000"
)

// ObjectStore 只读访问 seafile-data/storage。
type ObjectStore struct {
	root string
}

func NewObjectStore(root string) (*ObjectStore, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s 不是目录", root)
	}
	return &ObjectStore{root: root}, nil
}

// Path 返回对象磁盘路径，兼容带版本目录的老布局。
func (s *ObjectStore) Path(kind, repoID, version, id string) (string, error) {
	if len(id) != 40 {
		return "", fmt.Errorf("对象 ID 长度不是 40: %q", id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", fmt.Errorf("对象 ID 非十六进制 %q: %w", id, err)
	}
	cands := []string{filepath.Join(s.root, kind, repoID, id[:2], id[2:])}
	if version != "" {
		cands = append(cands, filepath.Join(s.root, kind, repoID, version, id[:2], id[2:]))
	}
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if repoID == "" {
		// blocks 在 objects.jsonl 里不带 repo_id，全库扫一遍
		matches, _ := filepath.Glob(filepath.Join(s.root, kind, "*", id[:2], id[2:]))
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("%s 对象 %s 在多个库里出现，需指定 repo", kind, id)
		}
	}
	return "", fmt.Errorf("%s 对象 %s 不在 repo %s 里", kind, id, repoID)
}

// Read 读取对象内容并自动解压，返回内容与是否压缩。
func (s *ObjectStore) Read(kind, repoID, version, id string) ([]byte, bool, error) {
	p, err := s.Path(kind, repoID, version, id)
	if err != nil {
		return nil, false, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, false, err
	}
	if len(raw) > 0 && raw[0] == 0x78 {
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, true, fmt.Errorf("解压 %s 失败: %w", p, err)
		}
		defer zr.Close()
		data, err := io.ReadAll(zr)
		if err != nil {
			return nil, true, fmt.Errorf("解压 %s 失败: %w", p, err)
		}
		return data, true, nil
	}
	return raw, false, nil
}

// ObjectID 从磁盘路径反推 ID：父目录 2 位 + 文件名 38 位。
func ObjectID(path string) string {
	parent := filepath.Base(filepath.Dir(path))
	if len(parent) != 2 {
		return filepath.Base(path)
	}
	return parent + filepath.Base(path)
}

func Sha1Hex(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

// Dirent 是目录项。Mode 用十进制整数表示（目录 = 040000 八进制 = 16384）。
type Dirent struct {
	ID    string `json:"id"`
	Mode  int64  `json:"mode"`
	Mtime int64  `json:"mtime"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
}

// FsObject 是 type=1 文件对象或 type=3 目录对象。
type FsObject struct {
	Type     int      `json:"type"`
	Version  int      `json:"version"`
	BlockIDs []string `json:"block_ids"`
	Size     int64    `json:"size"`
	Dirents  []Dirent `json:"dirents"`
}

func IsDirMode(mode int64) bool { return mode&(1<<14) != 0 }

// Commit 是提交对象。JSON 里创建者字段叫 creator（不是 creator_id）。
type Commit struct {
	CommitID     string  `json:"commit_id"`
	RootID       string  `json:"root_id"`
	RepoID       string  `json:"repo_id"`
	CreatorName  *string `json:"creator_name"`
	Creator      string  `json:"creator"`
	Description  string  `json:"description"`
	Ctime        int64   `json:"ctime"`
	ParentID     string  `json:"parent_id"`
	SecondParent string  `json:"second_parent_id"`
	RepoName     string  `json:"repo_name"`
	Version      int     `json:"version"`
	Encrypted    string  `json:"encrypted"`
	EncVersion   int     `json:"enc_version"`
	Magic        string  `json:"magic"`
	Key          string  `json:"key"`
}

// ComputeCommitID 复刻 common/commit-mgr.c:compute_commit_id。
// SHA1( root_id[41] ‖ creator_id[41] ‖ (creator_name+'\0' 若字段存在) ‖ desc+'\0' ‖ hton64(ctime) )
func ComputeCommitID(c Commit) string {
	buf := &bytes.Buffer{}
	buf.Write(fixed41(c.RootID))
	buf.Write(fixed41(c.Creator))
	if c.CreatorName != nil {
		buf.WriteString(*c.CreatorName)
		buf.WriteByte(0)
	}
	buf.WriteString(c.Description)
	buf.WriteByte(0)
	buf.Write(be64(uint64(c.Ctime)))
	return Sha1Hex(buf.Bytes())
}

func fixed41(s string) []byte {
	b := []byte(s)
	out := make([]byte, 41)
	copy(out, b)
	return out
}

func be64(v uint64) []byte {
	return []byte{byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
		byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// LoadFsObject 读取并解析 fs 对象。
func (s *ObjectStore) LoadFsObject(repoID, version, id string) (FsObject, error) {
	var obj FsObject
	data, _, err := s.Read("fs", repoID, version, id)
	if err != nil {
		return obj, err
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return obj, fmt.Errorf("解析 fs 对象 %s 失败: %w", id, err)
	}
	return obj, nil
}

// LoadCommit 读取并解析 commit 对象。
func (s *ObjectStore) LoadCommit(repoID, version, id string) (Commit, error) {
	var c Commit
	data, _, err := s.Read("commits", repoID, version, id)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("解析 commit %s 失败: %w", id, err)
	}
	return c, nil
}

// TreeEntry 是展开后的树节点。
type TreeEntry struct {
	Path  string
	Size  int64
	IsDir bool
	ID    string
}

// ExpandTree 递归展开目录树。files 与 dirs 都返回，path 以 "/" 开头。
func (s *ObjectStore) ExpandTree(repoID string, rootID string) ([]TreeEntry, error) {
	var out []TreeEntry
	var walk func(id, prefix string, depth int) error
	walk = func(id, prefix string, depth int) error {
		if id == emptySha1 {
			// 空目录/空文件的 fs 对象不落盘，ID 为全 0
			return nil
		}
		if depth > 64 {
			return fmt.Errorf("目录层级超过 64，可能有环: %s", prefix)
		}
		obj, err := s.LoadFsObject(repoID, "1", id)
		if err != nil {
			return err
		}
		if obj.Type == 1 {
			out = append(out, TreeEntry{Path: prefix, Size: obj.Size, ID: id})
			return nil
		}
		for _, de := range obj.Dirents {
			p := prefix + "/" + de.Name
			if IsDirMode(de.Mode) {
				out = append(out, TreeEntry{Path: p, Size: 0, IsDir: true, ID: de.ID})
				if err := walk(de.ID, p, depth+1); err != nil {
					return err
				}
			} else {
				out = append(out, TreeEntry{Path: p, Size: de.Size, ID: de.ID})
			}
		}
		return nil
	}
	if rootID == strings.Repeat("0", 40) {
		return out, nil
	}
	if err := walk(rootID, "", 0); err != nil {
		return nil, err
	}
	return out, nil
}
