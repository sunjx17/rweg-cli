package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"rweg/internal/hashline"
	"rweg/internal/ranges"
)

// Served 是一个文件的"模型已见"锚点镜像。
//
// hashes[i] 是第 i+1 行的锚点；空串表示该行**未 serve**。
// 这是 §7 校验链第 1、2 步的唯一依据。
type Served struct {
	// Path 归一化后的绝对路径（Windows 上小写）。
	Path string
	// Hashes 长度恒等于被读取时的行数。
	Hashes []string
	// Canons 是 serve 时的 canon，用于格式漂移检测（§7.1 第 4 步）。
	Canons []string
	// LineCount 是快照时的行数。
	LineCount int
	// CanonVer 记录本条记录用的 canon 算法版本。
	CanonVer int
	// Stride 记录本条记录用的探测步长。
	Stride int
}

// IndexOf 返回锚点首次出现的 1-based 行号。
//
// ⚠️ **找不到必须报错，不能"选一个"**（参考实现 ADR-0018exact-boundary 规则）：
// 锚点重复时无法判断用户想改哪一处，静默选第一处就是静默写错行。
func (s *Served) IndexOf(hash string) (int, bool) {
	for i, h := range s.Hashes {
		if h == hash {
			return i + 1, true
		}
	}
	return 0, false
}

// Has 报告锚点是否在已 serve 的行里。
func (s *Served) Has(hash string) bool {
	_, ok := s.IndexOf(hash)
	return ok
}

// IndexSet 返回所有出现该锚点的行号（用于诊断"重复锚点"）。
func (s *Served) IndexSet(hash string) []int {
	var out []int
	for i, h := range s.Hashes {
		if h == hash {
			out = append(out, i+1)
		}
	}
	return out
}

// CanonOf 返回第 line 行（1-based）serve 时的 canon。
func (s *Served) CanonOf(line int) (string, bool) {
	i := line - 1
	if i < 0 || i >= len(s.Canons) {
		return "", false
	}
	return s.Canons[i], true
}

// NormalizePath 归一化路径用于数据库主键。
//
// Windows 上大小写不敏感（`D:\A\x.ts` 与 `d:\a\x.ts` 是同一个文件），
// 统一小写才不会因为大小写差异查不到 served 记录。
// 斜杠也统一，`a/b.ts` 与 `a\b.ts` 视为同一路径。
func NormalizePath(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(filepath.FromSlash(p))
	}
	return p
}

// GetServed 读取某路径的 served 记录。返回 (nil, nil) 表示从未 serve 过。
func (d *DB) GetServed(path string) (*Served, error) {
	key := NormalizePath(path)
	var canonVer, stride, lineCount int
	var raw, rawCanons sql.NullString

	err := d.sql.QueryRow(
		`SELECT hashes, canons, line_count, canon_ver, stride FROM served WHERE path = ?`, key).
		Scan(&raw, &rawCanons, &lineCount, &canonVer, &stride)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil // 从未 serve 过，调用方据此报 E_NEVER_READ
	case err != nil:
		return nil, fmt.Errorf("get served %s: %w", path, err)
	}

	// 算法参数可能缺失（早期版本建的库），回落到默认值
	if canonVer <= 0 {
		canonVer = hashline.DefaultCanonVersion
	}
	if stride <= 0 {
		stride = hashline.DefaultProbeStride
	}

	var hashesArr []string
	if err := json.Unmarshal([]byte(raw.String), &hashesArr); err != nil {
		return nil, fmt.Errorf("decode served hashes %s: %w", path, err)
	}
	var canonsArr []string
	if rawCanons.Valid && rawCanons.String != "" {
		if err := json.Unmarshal([]byte(rawCanons.String), &canonsArr); err != nil {
			return nil, fmt.Errorf("decode served canons %s: %w", path, err)
		}
	}

	return &Served{
		Path:      key,
		Hashes:    hashesArr,
		Canons:    canonsArr,
		LineCount: lineCount,
		CanonVer:  canonVer,
		Stride:    stride,
	}, nil
}

// ServeLines 把指定行标记为已 serve，并记录锚点。
//
// 这是 read 的核心写操作：**多范围的所有行都会被标记**，
// 所以任意 serve 过的锚点都能用于后续 edit（plan §6.2）。
//
// anchors 必须是**全文件**的锚点数组（长度 == 文件行数），
// 本方法只挑 lines 里的那些写进 served。
func (d *DB) ServeLines(path string, lines *ranges.Set, anchors, canons []string, canonVer, stride int) error {
	if lines.Count() == 0 {
		return nil
	}
	key := NormalizePath(path)
	now := time.Now().Unix()

	// 已有记录则合并（保留之前 serve 过、这次没读的行）
	prev, err := d.GetServed(key)
	if err != nil {
		return err
	}
	out := make([]string, len(anchors))
	var outCanon []string
	if prev != nil && len(prev.Hashes) == len(anchors) {
		copy(out, prev.Hashes)
		outCanon = make([]string, len(anchors))
		copy(outCanon, prev.Canons)
	} else {
		// 行数变了（文件被外部改过）→ 旧记录失效，从头来
		outCanon = make([]string, len(anchors))
	}

	for line := 1; line <= len(anchors); line++ {
		if !lines.Has(line) {
			continue
		}
		out[line-1] = anchors[line-1]
		if line-1 < len(canons) {
			outCanon[line-1] = canons[line-1]
		}
	}

	hb, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("encode served: %w", err)
	}
	cb, err := json.Marshal(outCanon)
	if err != nil {
		return fmt.Errorf("encode canons: %w", err)
	}

	_, err = d.sql.Exec(
		`INSERT INTO served (path, hashes, canons, line_count, canon_ver, stride, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   hashes = excluded.hashes,
		   canons = excluded.canons,
		   line_count = excluded.line_count,
		   canon_ver = excluded.canon_ver,
		   stride = excluded.stride,
		   updated_at = excluded.updated_at`,
		key, string(hb), string(cb), len(anchors), canonVer, stride, now)
	if err != nil {
		return fmt.Errorf("serve %s: %w", path, err)
	}

	// 刻意**不在这里**写 snapshot 缓存。
	// snapshot 的键是「行内容指纹」，只有调用方才知道原始行内容；
	// 这里拿到的是锚点数组，用它算指纹会与调用方算出���键不一致，
	// 结果是同一份内容在 snapshot 表里出现两行。
	// 缓存写入由调用方用 PutSnapshotByFingerprint 负责。
	return nil
}

// PutServed 整体覆写某路径的 served 记录（edit / write 用）。
//
// 与 ServeLines 的区别：ServeLines 是"在已有记录上补几行"，本方法是
// "这份数组就是新的全部真相"。edit 之后行号会整体位移，必须整份替换；
// write 之后整个文件换掉，也必须整份替换。
//
// hashes 里的空串表示该行仍未 serve —— 这是 fail-closed 的载体，
// **不要**把空串过滤掉（长度必须与文件行数一致）。
func (d *DB) PutServed(path string, hashes, canons []string, canonVer, stride int) error {
	key := NormalizePath(path)
	hb, err := json.Marshal(hashes)
	if err != nil {
		return fmt.Errorf("encode served: %w", err)
	}
	cb, err := json.Marshal(canons)
	if err != nil {
		return fmt.Errorf("encode canons: %w", err)
	}
	_, err = d.sql.Exec(
		`INSERT INTO served (path, hashes, canons, line_count, canon_ver, stride, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   hashes = excluded.hashes,
		   canons = excluded.canons,
		   line_count = excluded.line_count,
		   canon_ver = excluded.canon_ver,
		   stride = excluded.stride,
		   updated_at = excluded.updated_at`,
		key, string(hb), string(cb), len(hashes), canonVer, stride, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("put served %s: %w", path, err)
	}
	return nil
}

// PruneSnapshotsExcept 删掉某路径下除 keep 指纹之外的全部快照缓存。
//
// write 会整份换掉文件内容，旧指纹的快照永远不会再命中——
// 留着只会让库无限增长，所以顺手清掉。
func (d *DB) PruneSnapshotsExcept(path, keep string) error {
	_, err := d.sql.Exec(
		`DELETE FROM snapshot WHERE path = ? AND checksum <> ?`, NormalizePath(path), keep)
	if err != nil {
		return fmt.Errorf("prune snapshots %s: %w", path, err)
	}
	return nil
}

// GetSnapshot 按 (path, 内容指纹, canon 版本) 取缓存的锚点数组。
//
// 用「行内容指纹」而不是「锚点数组的 checksum」做键，
// 因为查缓存时锚点还没算出来——否则每次 read 都要先算一遍锚点才能查缓存，
// 缓存就白加了。
func (d *DB) GetSnapshot(path, fingerprint string, canonVer int) ([]string, bool, error) {
	var hashes string
	err := d.sql.QueryRow(
		`SELECT hashes FROM snapshot WHERE path = ? AND checksum = ? AND canon_ver = ?`,
		NormalizePath(path), fingerprint, canonVer).Scan(&hashes)
	switch {
	case err == sql.ErrNoRows:
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("get snapshot: %w", err)
	}
	var arr []string
	if err := json.Unmarshal([]byte(hashes), &arr); err != nil {
		return nil, false, fmt.Errorf("decode snapshot: %w", err)
	}
	return arr, true, nil
}

// GetSnapshotByFingerprint 是 GetSnapshot 的语义别名，读起来更清楚。
func (d *DB) GetSnapshotByFingerprint(path, fingerprint string, canonVer int) ([]string, bool, error) {
	return d.GetSnapshot(path, fingerprint, canonVer)
}

func (d *DB) putSnapshotByFingerprint(path string, anchors []string, fingerprint string, canonVer int) error {
	hb, err := json.Marshal(anchors)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO snapshot (path, checksum, line_count, hashes, canon_ver, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path, checksum, canon_ver) DO UPDATE SET created_at = excluded.created_at`,
		path, fingerprint, len(anchors), string(hb), canonVer, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("put snapshot: %w", err)
	}
	return nil
}

// PutSnapshotByFingerprint 让上层在算出锚点后回填缓存（带自定义指纹）。
func (d *DB) PutSnapshotByFingerprint(path string, anchors []string, fingerprint string, canonVer int) error {
	return d.putSnapshotByFingerprint(NormalizePath(path), anchors, fingerprint, canonVer)
}

// RetiredFor 返回该路径下被释放、禁止再分配的锚点集合。
func (d *DB) RetiredFor(path string) (map[string]bool, error) {
	rows, err := d.sql.Query(`SELECT hash FROM retired WHERE path = ?`, NormalizePath(path))
	if err != nil {
		return nil, fmt.Errorf("query retired: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, rows.Err()
}

// Retire 把一批锚点标记为"已释放、禁止再分配"。
//
// 背景：edit 删掉/替换了几行后，那些锚点模型可能还持有。
// 若允许重新分配到相同内容的新行，模型会拿着旧锚点改到新行上去——
// 静默写错行。宁可槽位浪费，也不能错配。
func (d *DB) Retire(path string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	key := NormalizePath(path)
	now := time.Now().Unix()
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("retire begin: %w", err)
	}
	defer tx.Rollback()

	// 去重后批量插入
	seen := map[string]bool{}
	uniq := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		uniq = append(uniq, h)
	}
	sort.Strings(uniq) // 稳定插入顺序，便于复现
	for _, h := range uniq {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO retired (path, hash, freed_at) VALUES (?, ?, ?)`,
			key, h, now); err != nil {
			return fmt.Errorf("retire %s: %w", h, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("retire commit: %w", err)
	}
	return nil
}

// ForgetPath 清掉某路径的全部痕迹（write 覆盖整文件时用）。
func (d *DB) ForgetPath(path string) error {
	key := NormalizePath(path)
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM served  WHERE path = ?`,
		`DELETE FROM retired WHERE path = ?`,
		`DELETE FROM snapshot WHERE path = ?`,
	} {
		if _, err := tx.Exec(q, key); err != nil {
			return fmt.Errorf("forget %s: %w", key, err)
		}
	}
	return tx.Commit()
}

// ServedPaths 列出所有有 served 记录的路径（诊断与 store 单测用）。
func (d *DB) ServedPaths() ([]string, error) {
	rows, err := d.sql.Query(`SELECT path FROM served ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
