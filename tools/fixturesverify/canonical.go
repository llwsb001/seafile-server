package main

// jansson 兼容的规范化 JSON：json_dumps(obj, JSON_SORT_KEYS) 的输出格式。
//
// 用来证明"对象 ID = SHA1(规范化后的字节)"这条契约在 Go 侧也能复现，
// 而不是只靠"哈希落盘字节"。规则：
//   - 对象键按字典序排序
//   - 分隔符 ", " 与 ": "（逗号后、冒号后各一个空格）
//   - 不做 ASCII 转义，中文按 UTF-8 原样输出
//   - 数字保持原始字面量（json.Number）

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func CanonicalJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("JSON 后有多余内容: %w", err)
	}
	var buf bytes.Buffer
	if err := encodeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeCanonical(buf *bytes.Buffer, v interface{}) error {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(string(val))
	case float64:
		buf.WriteString(strconv.FormatFloat(val, 'g', -1, 64))
	case string:
		buf.WriteString(quoteString(val))
	case []interface{}:
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteString(", ")
			}
			if err := encodeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteString(", ")
			}
			buf.WriteString(quoteString(k))
			buf.WriteString(": ")
			if err := encodeCanonical(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("不支持的类型 %T", v)
	}
	return nil
}

func quoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
