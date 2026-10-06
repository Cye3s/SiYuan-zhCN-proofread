package main

// 保序 JSON 处理：解析 zh-CN.json → 修改 → 导出，key 顺序必须与原文件完全一致。
// 标准库 map 会按字母序重排，自建保序结构：Token 流解析 + 按原序序列化。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Node 表示一个 JSON 对象节点，保存 key 顺序，值分三类：
//   - Nodes: 嵌套对象
//   - Strs:  字符串叶子（可编辑的目标）
//   - Raws:  其它类型（数字/bool/null/数组）原样保留，导出时逐字节还原
type Node struct {
	Keys  []string                   `json:"-"`
	Nodes map[string]*Node           `json:"-"`
	Strs  map[string]string          `json:"-"`
	Raws  map[string]json.RawMessage `json:"-"`
}

// NewNode 新建保序节点
func NewNode() *Node {
	return &Node{
		Nodes: map[string]*Node{},
		Strs:  map[string]string{},
		Raws:  map[string]json.RawMessage{},
	}
}

// ---------- RFC 6901 转义 ----------
func escKey(p_key string) string {
	v_key := strings.ReplaceAll(p_key, "~", "~0")
	return strings.ReplaceAll(v_key, "/", "~1")
}

func unescKey(p_key string) string {
	v_key := strings.ReplaceAll(p_key, "~1", "/")
	return strings.ReplaceAll(v_key, "~0", "~")
}

// ---------- 解析（保序） ----------
// ParseOrdered 解析 JSON 对象为保序 Node，只深入字符串叶子与嵌套对象，
// 其它类型存 RawMessage 原样保留。
func ParseOrdered(p_data []byte) (*Node, error) {
	v_dec := json.NewDecoder(bytes.NewReader(p_data))
	v_root, v_err := v_dec.Token()
	if v_err != nil {
		return nil, v_err
	}
	v_delim, v_isDelim := v_root.(json.Delim)
	if !v_isDelim || v_delim != '{' {
		return nil, fmt.Errorf("根节点不是 JSON 对象")
	}
	return parseObject(v_dec)
}

// parseObject 假定 '{' 已被消费
func parseObject(p_dec *json.Decoder) (*Node, error) {
	v_node := NewNode()
	for p_dec.More() {
		v_keyToken, v_err := p_dec.Token()
		if v_err != nil {
			return nil, v_err
		}
		v_key, v_isStr := v_keyToken.(string)
		if !v_isStr {
			return nil, fmt.Errorf("非法 key: %v", v_keyToken)
		}
		var v_raw json.RawMessage
		if v_err = p_dec.Decode(&v_raw); v_err != nil {
			return nil, v_err
		}
		v_node.Keys = append(v_node.Keys, v_key)
		switch {
		case len(v_raw) > 0 && v_raw[0] == '{':
			v_child, v_err := ParseOrdered(v_raw)
			if v_err != nil {
				return nil, v_err
			}
			v_node.Nodes[v_key] = v_child
		case len(v_raw) > 0 && v_raw[0] == '"':
			var v_str string
			if v_err = json.Unmarshal(v_raw, &v_str); v_err != nil {
				return nil, v_err
			}
			v_node.Strs[v_key] = v_str
		default:
			v_node.Raws[v_key] = v_raw
		}
	}
	// 消费收尾的 '}'
	if _, v_err := p_dec.Token(); v_err != nil {
		return nil, v_err
	}
	return v_node, nil
}

// ---------- 序列化（按原序） ----------
func marshalString(p_str string) string {
	var v_buf bytes.Buffer
	v_enc := json.NewEncoder(&v_buf)
	v_enc.SetEscapeHTML(false) // 不转义 HTML 字符，与原文件一致
	_ = v_enc.Encode(p_str)
	return strings.TrimRight(v_buf.String(), "\n")
}

// WriteTo 按 Keys 顺序写出对象（p_cur 为当前层级，p_indent 为单层缩进字符串）
func (v_node *Node) WriteTo(p_buf *bytes.Buffer, p_indent string, p_cur int) {
	v_pad := strings.Repeat(p_indent, p_cur)
	v_padInner := strings.Repeat(p_indent, p_cur+1)
	p_buf.WriteString("{\n")
	for v_idx, v_key := range v_node.Keys {
		p_buf.WriteString(v_padInner)
		p_buf.WriteString(marshalString(v_key))
		p_buf.WriteString(": ")
		switch {
		case v_node.Nodes[v_key] != nil:
			v_node.Nodes[v_key].WriteTo(p_buf, p_indent, p_cur+1)
		default:
			if v_str, v_ok := v_node.Strs[v_key]; v_ok {
				p_buf.WriteString(marshalString(v_str))
			} else if v_raw, v_ok := v_node.Raws[v_key]; v_ok {
				p_buf.Write(v_raw)
			}
		}
		if v_idx < len(v_node.Keys)-1 {
			p_buf.WriteString(",")
		}
		p_buf.WriteString("\n")
	}
	p_buf.WriteString(v_pad)
	p_buf.WriteString("}")
}

// Serialize 全量输出（顶层无缩进），末尾带换行（与原文件一致）
func (v_node *Node) Serialize(p_indent string) []byte {
	var v_buf bytes.Buffer
	v_node.WriteTo(&v_buf, p_indent, 0)
	v_buf.WriteString("\n")
	return v_buf.Bytes()
}

// ---------- flatten / pointer ----------
// Flatten 递归展平，产出 {json_pointer: 字符串叶子值}（顺序无关，仅供检索）
func Flatten(p_node *Node, p_prefix string, p_out map[string]string) map[string]string {
	for _, v_key := range p_node.Keys {
		v_ptr := p_prefix + "/" + escKey(v_key)
		if v_child, v_ok := p_node.Nodes[v_key]; v_ok {
			Flatten(v_child, v_ptr, p_out)
		} else if v_str, v_ok := p_node.Strs[v_key]; v_ok {
			p_out[v_ptr] = v_str
		}
	}
	return p_out
}

// FlattenOrder 保序展平，仅产出指针序列（顺序与文件中 key 出现顺序一致，供表格排序）
func FlattenOrder(p_node *Node, p_prefix string, p_out *[]string) {
	for _, v_key := range p_node.Keys {
		v_ptr := p_prefix + "/" + escKey(v_key)
		if v_child, v_ok := p_node.Nodes[v_key]; v_ok {
			FlattenOrder(v_child, v_ptr, p_out)
		} else if _, v_ok := p_node.Strs[v_key]; v_ok {
			*p_out = append(*p_out, v_ptr)
		}
	}
}

// SetByPointer 按 pointer 回写字符串叶子；路径不存在返回 false
func (v_node *Node) SetByPointer(p_ptr string, p_val string) bool {
	v_parts := strings.Split(strings.TrimPrefix(p_ptr, "/"), "/")
	if len(v_parts) == 0 || v_parts[0] == "" {
		return false
	}
	v_cur := v_node
	for _, v_part := range v_parts[:len(v_parts)-1] {
		v_child, v_ok := v_cur.Nodes[unescKey(v_part)]
		if !v_ok {
			return false
		}
		v_cur = v_child
	}
	v_leaf := unescKey(v_parts[len(v_parts)-1])
	if _, v_ok := v_cur.Strs[v_leaf]; !v_ok {
		return false
	}
	v_cur.Strs[v_leaf] = p_val
	return true
}

// HasPath 判断 pointer 路径是否存在（含叶子存在性）
func (v_node *Node) HasPath(p_ptr string) bool {
	v_parts := strings.Split(strings.TrimPrefix(p_ptr, "/"), "/")
	if len(v_parts) == 0 || v_parts[0] == "" {
		return false
	}
	v_cur := v_node
	for _, v_part := range v_parts[:len(v_parts)-1] {
		v_child, v_ok := v_cur.Nodes[unescKey(v_part)]
		if !v_ok {
			return false
		}
		v_cur = v_child
	}
	v_leaf := unescKey(v_parts[len(v_parts)-1])
	_, v_ok := v_cur.Strs[v_leaf]
	return v_ok
}

var (
	p_indentSpacesRe = regexp.MustCompile(`^([ ]+)\S`)
	p_indentTabRe    = regexp.MustCompile(`^(\t+)\S`)
	p_digitRe        = regexp.MustCompile(`^\d+$`)
	p_placeholderRe  = regexp.MustCompile(`\{\}|\{\w+\}|%\w+`)
)

// DisplayKey pointer -> 美化显示：/_kernel/2 -> _kernel_2；/button/save -> button.save
func DisplayKey(p_ptr string) string {
	v_parts := strings.Split(strings.TrimPrefix(p_ptr, "/"), "/")
	if len(v_parts) == 0 || v_parts[0] == "" {
		return p_ptr
	}
	v_out := unescKey(v_parts[0])
	for _, v_part := range v_parts[1:] {
		v_part = unescKey(v_part)
		if p_digitRe.MatchString(v_part) {
			v_out += "_" + v_part
		} else {
			v_out += "." + v_part
		}
	}
	return v_out
}

// DetectIndent 探测语言文件缩进（支持空格与 TAB，返回一层缩进字符串）
func DetectIndent(p_content string) string {
	v_lines := strings.Split(p_content, "\n")
	for _, v_line := range v_lines[1:] {
		if v_match := p_indentSpacesRe.FindStringSubmatch(v_line); v_match != nil {
			return v_match[1]
		}
	}
	for _, v_line := range v_lines[1:] {
		if v_match := p_indentTabRe.FindStringSubmatch(v_line); v_match != nil {
			return v_match[1]
		}
	}
	return "  "
}

// PlaceholderMismatch 占位符数量/类型与简中不一致时返回 true
func PlaceholderMismatch(p_fix, p_cn string) bool {
	v_fixList := p_placeholderRe.FindAllString(p_fix, -1)
	v_cnList := p_placeholderRe.FindAllString(p_cn, -1)
	if len(v_fixList) != len(v_cnList) {
		return true
	}
	sort.Strings(v_fixList)
	sort.Strings(v_cnList)
	for v_idx := range v_fixList {
		if v_fixList[v_idx] != v_cnList[v_idx] {
			return true
		}
	}
	return false
}
