package main

// 用 C 服务端产出的 fixtures 做读路径对拍：
//  1. 对象层：expected/objects.jsonl 里每个对象复算 ID，并检查"规范化 JSON"能重现落盘字节
//  2. 树层：repo -> head commit -> root fs，展开目录树，核对文件大小与块引用
//  3. 加密层：enc_version=2 库用口令解出仓库密钥，解密块，比对明文
//
// 与 C 服务端零共享代码，只依赖 fixtures 目录。

import (
	"archive/tar"
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type repoEntry struct {
	RepoID     string `json:"repo_id"`
	OriginRepo string `json:"origin_repo_id"`
	Name       string `json:"name"`
	Owner      string `json:"owner"`
	Encrypted  int    `json:"encrypted"`
	Type       string `json:"type"`
	HeadCommit string `json:"head_commit_id"`
}

type expectedObject struct {
	Type       string `json:"type"`
	RepoID     string `json:"repo_id"`
	ID         string `json:"id"`
	Stored     int64  `json:"stored_bytes"`
	Content    int64  `json:"content_bytes"`
	Compressed bool   `json:"compressed"`
	SHA256     string `json:"sha256_content"`
}

type stats struct {
	checked      int
	failed       int
	repos        int
	files        int
	dirs         int
	blocksOK     int
	canonicalOK  int
	canonicalBad int
}

var (
	fixturesDir = flag.String("fixtures", "", "fixtures v1 目录（含 manifest.json/repos.json/expected/storage.tar）")
	cryptPasswd = flag.String("passwd", "", "加密库口令；非空时做解密校验，例如 fixtures 里用的是 123")
	verbose     = flag.Bool("v", false, "打印每个库的目录树")
)

func main() {
	flag.Parse()
	if *fixturesDir == "" {
		usage()
	}
	if err := run(*fixturesDir); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: fixturesverify -fixtures fixtures/v1 [-passwd 123] [-v]")
	fmt.Fprintln(os.Stderr, "提示: storage.tar 需先解包，本工具读 <fixtures>/storage/")
	os.Exit(2)
}

func run(dir string) error {
	storage := filepath.Join(dir, "storage")
	if _, err := os.Stat(storage); err != nil {
		// 只有 storage.tar 时自动解到临时目录，省掉手工解包
		tarPath := filepath.Join(dir, "storage.tar")
		if _, terr := os.Stat(tarPath); terr != nil {
			return fmt.Errorf("找不到 %s 或 %s", storage, tarPath)
		}
		tmp, err := os.MkdirTemp("", "fixtures-storage-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if err := extractTar(tarPath, tmp); err != nil {
			return err
		}
		storage = filepath.Join(tmp, "storage")
	}
	store, err := NewObjectStore(storage)
	if err != nil {
		return err
	}

	repos, err := loadRepos(filepath.Join(dir, "repos.json"))
	if err != nil {
		return err
	}

	st := &stats{}
	if err := verifyObjects(store, filepath.Join(dir, "expected", "objects.jsonl"), st); err != nil {
		return err
	}

	plainFiles := map[string]int64{}
	for _, r := range repos {
		if r.Owner == "System" || r.HeadCommit == "" {
			continue
		}
		if err := verifyRepo(store, r, st, plainFiles); err != nil {
			return err
		}
	}

	if *cryptPasswd != "" {
		if err := verifyEncryptedRepo(store, repos, *cryptPasswd); err != nil {
			return err
		}
	}

	fmt.Printf("\n== 汇总 ==\n对象 %d 个（失败 %d），库 %d 个，目录 %d，文件 %d\n",
		st.checked, st.failed, st.repos, st.dirs, st.files)
	fmt.Printf("规范化 JSON 重现：成功 %d，失败 %d\n", st.canonicalOK, st.canonicalBad)
	if st.failed != 0 || st.canonicalBad != 0 {
		return fmt.Errorf("存在 %d 个对象 ID 不一致 / %d 个规范化不一致", st.failed, st.canonicalBad)
	}
	return nil
}

func loadRepos(path string) ([]repoEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var repos []repoEntry
	if err := json.Unmarshal(data, &repos); err != nil {
		return nil, fmt.Errorf("解析 repos.json: %w", err)
	}
	return repos, nil
}

func verifyObjects(store *ObjectStore, path string, st *stats) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		var rec expectedObject
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return fmt.Errorf("objects.jsonl 第 %d 行解析失败: %w", line, err)
		}
		st.checked++

		data, compressed, err := store.Read(rec.Type, rec.RepoID, "1", rec.ID)
		if err != nil {
			fmt.Printf("  [缺失] %s %s: %v\n", rec.Type, rec.ID, err)
			st.failed++
			continue
		}
		var got string
		if rec.Type == "commits" {
			var c Commit
			if err := json.Unmarshal(data, &c); err != nil {
				fmt.Printf("  [commit 解析失败] %s: %v\n", rec.ID, err)
				st.failed++
				continue
			}
			got = ComputeCommitID(c)
		} else {
			got = Sha1Hex(data)
		}
		if got != rec.ID {
			fmt.Printf("  [ID 不一致] %s %s -> %s\n", rec.Type, rec.ID, got)
			st.failed++
		}
		if compressed != rec.Compressed {
			fmt.Printf("  [压缩标记不符] %s %s\n", rec.Type, rec.ID)
			st.failed++
		}
		if rec.Type == "blocks" {
			st.blocksOK++
		}
		if rec.Type == "fs" {
			canon, err := CanonicalJSON(data)
			if err != nil {
				fmt.Printf("  [无法规范化] fs %s: %v\n", rec.ID, err)
				st.canonicalBad++
			} else if Sha1Hex(canon) == rec.ID {
				st.canonicalOK++
			} else {
				fmt.Printf("  [规范化不符] fs %s\n", rec.ID)
				st.canonicalBad++
			}
		}
	}
	return sc.Err()
}

func verifyRepo(store *ObjectStore, r repoEntry, st *stats, plainFiles map[string]int64) error {
	commit, err := store.LoadCommit(r.RepoID, "1", r.HeadCommit)
	if err != nil {
		return fmt.Errorf("库 %s 读 head commit 失败: %w", r.Name, err)
	}
	if got := ComputeCommitID(commit); got != r.HeadCommit {
		return fmt.Errorf("库 %s head commit ID 不一致: %s != %s", r.Name, got, r.HeadCommit)
	}

	storageRepo := r.RepoID
	if r.OriginRepo != "" {
		// 虚拟库的 fs 对象存在源库里，只有 commit 在自己名下
		storageRepo = r.OriginRepo
	}
	tree, err := store.ExpandTree(storageRepo, commit.RootID)
	if err != nil {
		return fmt.Errorf("库 %s 展开目录树失败: %w", r.Name, err)
	}
	st.repos++

	for _, e := range tree {
		if e.IsDir {
			st.dirs++
			continue
		}
		st.files++
		if strings.HasPrefix(e.Path, "archive/") || !strings.Contains(e.Path, "big-40mb") {
			plainFiles[e.Path] = e.Size
		}
		if err := verifyFileBlocks(store, storageRepo, e, r.Encrypted == 1); err != nil {
			return fmt.Errorf("库 %s 文件 %s: %w", r.Name, e.Path, err)
		}
	}

	fmt.Printf("库 %-12s %s: 树 %d 项（目录 %d，文件 %d）head=%s\n",
		r.Name, short(r.RepoID), len(tree), countDir(tree), countFile(tree), short(r.HeadCommit))

	if *verbose {
		names := make([]string, 0, len(tree))
		for _, e := range tree {
			names = append(names, e.Path)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Println("   ", n)
		}
	}
	return nil
}

// verifyFileBlocks 校验文件的所有块都在、且 ID 与内容 SHA1 一致。
func verifyFileBlocks(store *ObjectStore, repoID string, e TreeEntry, encrypted bool) error {
	if e.ID == emptySha1 {
		if e.Size != 0 {
			return fmt.Errorf("空文件 ID 但大小 %d", e.Size)
		}
		return nil
	}
	obj, err := store.LoadFsObject(repoID, "1", e.ID)
	if err != nil {
		return err
	}
	if obj.Type != 1 {
		return fmt.Errorf("目录项 %s 指向的 fs 对象 type=%d，应为 1", e.Path, obj.Type)
	}
	var total int64
	for _, bid := range obj.BlockIDs {
		data, _, err := store.Read("blocks", repoID, "1", bid)
		if err != nil {
			return fmt.Errorf("块 %s 缺失: %w", bid, err)
		}
		if got := Sha1Hex(data); got != bid {
			return fmt.Errorf("块 %s SHA1 不一致 -> %s", bid, got)
		}
		total += int64(len(data))
	}
	// 加密库的块是密文，长度含填充，只比明文大小会误报
	if !encrypted && total != obj.Size && obj.Size != 0 {
		return fmt.Errorf("块总大小 %d 与 fs 记录 %d 不符", total, obj.Size)
	}
	return nil
}

func verifyEncryptedRepo(store *ObjectStore, repos []repoEntry, passwd string) error {
	for _, r := range repos {
		if r.Encrypted != 1 || r.HeadCommit == "" {
			continue
		}
		commit, err := store.LoadCommit(r.RepoID, "1", r.HeadCommit)
		if err != nil {
			return err
		}
		key, err := UnlockRepo(passwd, commit.EncVersion, commit.Key)
		if err != nil {
			return fmt.Errorf("库 %s 解密钥失败（口令或算法不符）: %w", r.Name, err)
		}
		storageRepo := r.RepoID
		if r.OriginRepo != "" {
			// 虚拟库的 fs 对象存在源库里，只有 commit 在自己名下
			storageRepo = r.OriginRepo
		}
		tree, err := store.ExpandTree(storageRepo, commit.RootID)
		if err != nil {
			return err
		}
		var checked int
		for _, e := range tree {
			if e.IsDir {
				continue
			}
			obj, err := store.LoadFsObject(r.RepoID, "1", e.ID)
			if err != nil {
				return err
			}
			if obj.Type != 1 {
				continue
			}
			var plain []byte
			for _, bid := range obj.BlockIDs {
				ct, _, err := store.Read("blocks", r.RepoID, "1", bid)
				if err != nil {
					return err
				}
				pt, err := key.DecryptBlock(ct)
				if err != nil {
					return fmt.Errorf("库 %s 块 %s 解密失败: %w", r.Name, bid, err)
				}
				plain = append(plain, pt...)
			}
			if int64(len(plain)) != obj.Size {
				return fmt.Errorf("库 %s 文件 %s 解密后 %d 字节，fs 记录 %d",
					r.Name, e.Path, len(plain), obj.Size)
			}
			fmt.Printf("加密库 %s: %s 解密成功，%d 字节，内容=%q\n",
				r.Name, e.Path, len(plain), truncate(string(plain), 40))
			checked++
		}
		if checked == 0 {
			return fmt.Errorf("库 %s 没有可校验的加密文件", r.Name)
		}
	}
	return nil
}

func countDir(tree []TreeEntry) int {
	n := 0
	for _, e := range tree {
		if e.IsDir {
			n++
		}
	}
	return n
}

func countFile(tree []TreeEntry) int {
	return len(tree) - countDir(tree)
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// extractTar 解开 storage.tar（只支持普通文件与目录）。
func extractTar(tarPath, dst string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("tar 中含可疑路径 %q", hdr.Name)
		}
		target := filepath.Join(dst, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			w, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, tr); err != nil {
				w.Close()
				return err
			}
			w.Close()
		}
	}
}
